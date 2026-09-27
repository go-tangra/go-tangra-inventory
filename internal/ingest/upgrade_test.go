package ingest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/releases"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrades"
)

type nopAuditor struct{}

func (nopAuditor) Record(context.Context, audit.Event) error { return nil }

var debAmd64 = agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "deb"}

// upgradeHarness is the ingest harness with releases 4.4.0 and 4.5.0 stored
// and the upgrade service attached.
type upgradeHarness struct {
	*harness
	keys agentrelease.Keyring
	rel  *releases.Service
	upg  *upgrades.Service
	data map[string][]byte // version -> deb artifact bytes
}

func newUpgradeHarness(t *testing.T, maxDownloads int) *upgradeHarness {
	t.Helper()
	h := newHarness(t, 0)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := agentrelease.Keyring{"test-key": pub}
	dir := t.TempDir()
	data := map[string][]byte{}
	for _, v := range []string{"4.4.0", "4.5.0"} {
		d := filepath.Join(dir, v)
		_ = os.MkdirAll(d, 0o755)
		content := bytes.Repeat([]byte("agent-"+v+"-"), 2000) // > 3 chunks of 4 KiB... sized below
		data[v] = content
		sum := sha256.Sum256(content)
		_ = os.WriteFile(filepath.Join(d, "agent.deb"), content, 0o644)
		m := agentrelease.Manifest{Schema: 1, Version: v, CreatedAt: time.Now().UTC().Truncate(time.Second), KeyID: "test-key",
			Artifacts: []agentrelease.Artifact{{OS: "linux", Arch: "amd64", InstallType: "deb", File: "agent.deb", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}}}
		b := m.Encode()
		_ = os.WriteFile(filepath.Join(d, agentrelease.ManifestFile), b, 0o644)
		_ = os.WriteFile(filepath.Join(d, agentrelease.SignatureFile), []byte(agentrelease.EncodeSignature(ed25519.Sign(priv, b))), 0o644)
	}
	rel := releases.New(h.mem, keys, nopAuditor{}, slog.New(slog.DiscardHandler), releases.Config{BundleDir: dir, KeepVersions: 5, ChunkBytes: 4096})
	if n := rel.SeedBundles(context.Background()); n != 2 {
		t.Fatalf("seeded %d", n)
	}
	upg := upgrades.New(h.mem, rel, h.reg, events.HubPublisher{}, upgrades.Config{RequestTTL: time.Hour, ProgressTimeout: 15 * time.Minute})
	h.srv.WithUpgrades(upg, rel, maxDownloads, time.Minute)
	return &upgradeHarness{harness: h, keys: keys, rel: rel, upg: upg, data: data}
}

// agentCtx enrolls an agent reporting version and returns its auth context.
func (h *upgradeHarness) agentCtx(t *testing.T, tenant, hostname, version string) (context.Context, string) {
	t.Helper()
	id, cred := h.mintAndEnroll(t, tenant, hostname)
	if err := h.mem.TouchAgent(context.Background(), id, version, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx := h.authedCtx(t, id, cred)
	a, _ := h.mem.GetAgentByID(context.Background(), id)
	return withAgent(ctx, a), id
}

// connect opens StreamCommands with platform/capabilities and returns the
// commands received right after connecting; the stream stays open until
// cancel is called.
func (h *upgradeHarness) connect(t *testing.T, ctx context.Context, req *inventoryv1.StreamRequest) (<-chan *inventoryv1.Command, context.CancelFunc) {
	t.Helper()
	sctx, cancel := context.WithCancel(ctx)
	fs := &fakeStream{ctx: sctx, sent: make(chan *inventoryv1.Command, 16)}
	go func() { _ = h.srv.StreamCommands(req, fs) }()
	t.Cleanup(cancel)
	return fs.sent, cancel
}

func streamReq(version string, caps ...string) *inventoryv1.StreamRequest {
	return &inventoryv1.StreamRequest{AgentVersion: version,
		Platform: &inventoryv1.AgentPlatform{Os: "linux", Arch: "amd64", InstallType: "deb"}, Capabilities: caps}
}

// refresh re-reads the agent row onto the context (auth does this per call).
func (h *upgradeHarness) refresh(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	a, _ := AgentFromContext(ctx)
	fresh, err := h.mem.GetAgentByID(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return withAgent(ctx, fresh)
}

func TestStreamStoresPlatformAndDeliversPending(t *testing.T) {
	h := newUpgradeHarness(t, 4)
	ctx, id := h.agentCtx(t, "tenant-a", "node-1", "4.4.0")
	// An upgrade requested while the agent is offline stays pending ...
	_, cancel := h.connect(t, ctx, streamReq("4.4.0", "upgrade.v1", "BAD CAP", strings.Repeat("x", 40)))
	waitOnline(t, h.reg, id, true)
	a, _ := h.mem.GetAgentByID(context.Background(), id)
	if a.OS != "linux" || a.Arch != "amd64" || a.InstallType != "deb" || len(a.Capabilities) != 1 || a.Capabilities[0] != "upgrade.v1" || a.PlatformSeenAt.IsZero() {
		t.Fatalf("platform = %+v", a)
	}
	cancel()
	waitOnline(t, h.reg, id, false)
	res, err := h.upg.Request(context.Background(), "tenant-a", upgrades.Actor{Kind: upgrades.ActorUser, ID: "u"}, []string{id}, false)
	if err != nil || len(res.Created) != 1 || res.Created[0].State != store.UpgradePending {
		t.Fatalf("request = %+v %v", res, err)
	}
	// ... and is delivered on the next connect.
	sent, _ := h.connect(t, ctx, streamReq("4.4.0", "upgrade.v1"))
	select {
	case cmd := <-sent:
		if cmd.GetType() != inventoryv1.CommandType_COMMAND_TYPE_UPGRADE || cmd.GetUpgrade().GetRequestId() != res.Created[0].ID ||
			cmd.GetUpgrade().GetTargetVersion() != "4.5.0" || cmd.GetUpgrade().GetAllowDowngrade() {
			t.Fatalf("command = %v", cmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending upgrade not delivered on connect")
	}
	// Invalid platform values are dropped, not stored.
	ctx2, id2 := h.agentCtx(t, "tenant-a", "node-2", "4.4.0")
	h.connect(t, ctx2, &inventoryv1.StreamRequest{Platform: &inventoryv1.AgentPlatform{Os: "plan9", Arch: "mips", InstallType: "snap"}})
	waitOnline(t, h.reg, id2, true)
	a2, _ := h.mem.GetAgentByID(context.Background(), id2)
	if a2.OS != "" || a2.Arch != "" || a2.InstallType != "" || a2.Capabilities != nil {
		t.Fatalf("invalid platform stored: %+v", a2)
	}
	// A live registry push carries the payload too.
	ok, _ := h.reg.Deliver(context.Background(), id, registry.Command{ID: "c2", Type: registry.CommandUpgrade,
		Upgrade: &registry.UpgradePayload{RequestID: "r2", TargetVersion: "4.5.0", AllowDowngrade: true}})
	if !ok {
		t.Fatal("not delivered")
	}
	select {
	case cmd := <-sent:
		if cmd.GetUpgrade().GetRequestId() != "r2" || !cmd.GetUpgrade().GetAllowDowngrade() {
			t.Fatalf("pushed = %v", cmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push not forwarded")
	}
}

func TestCheckAgentUpdate(t *testing.T) {
	h := newUpgradeHarness(t, 4)
	ctx, id := h.agentCtx(t, "tenant-a", "node-1", "4.4.0")
	h.connect(t, ctx, streamReq("4.4.0", "upgrade.v1"))
	waitOnline(t, h.reg, id, true)
	p := &inventoryv1.AgentPlatform{Os: "linux", Arch: "amd64", InstallType: "deb"}
	resp, err := h.srv.CheckAgentUpdate(ctx, &inventoryv1.CheckAgentUpdateRequest{CurrentVersion: "4.4.0", Platform: p})
	if err != nil || !resp.GetAvailable() || resp.GetTargetVersion() != "4.5.0" || resp.GetRequestId() != "" {
		t.Fatalf("check = %v %v", resp, err)
	}
	resp, err = h.srv.CheckAgentUpdate(ctx, &inventoryv1.CheckAgentUpdateRequest{CurrentVersion: "4.4.0", Platform: p, Apply: true})
	if err != nil || resp.GetRequestId() == "" {
		t.Fatalf("apply = %v %v", resp, err)
	}
	u, _ := h.mem.GetAgentUpgrade(context.Background(), "tenant-a", resp.GetRequestId())
	if u.Origin != store.OriginAgent || u.RequestedBy != id {
		t.Fatalf("request = %+v", u)
	}
	_, err = h.srv.CheckAgentUpdate(ctx, &inventoryv1.CheckAgentUpdateRequest{CurrentVersion: "4.4.0", Platform: &inventoryv1.AgentPlatform{Os: "plan9"}})
	requireCode(t, err, codes.InvalidArgument)
	_, err = h.srv.CheckAgentUpdate(context.Background(), &inventoryv1.CheckAgentUpdateRequest{})
	requireCode(t, err, codes.Unauthenticated)
	h.mem.FailNext("GetUpgradePolicy")
	_, err = h.srv.CheckAgentUpdate(ctx, &inventoryv1.CheckAgentUpdateRequest{CurrentVersion: "4.4.0", Platform: p})
	requireCode(t, err, codes.Unavailable)
}

// dlStream collects DownloadAgentRelease messages; gate blocks Send until
// closed (concurrency tests).
type dlStream struct {
	fakeServerStream
	mu   sync.Mutex
	msgs []*inventoryv1.DownloadAgentReleaseResponse
	gate chan struct{}
}

type fakeServerStream struct{ ctx context.Context }

func (f fakeServerStream) Context() context.Context     { return f.ctx }
func (f fakeServerStream) SetHeader(metadata.MD) error  { return nil }
func (f fakeServerStream) SendHeader(metadata.MD) error { return nil }
func (f fakeServerStream) SetTrailer(metadata.MD)       {}
func (f fakeServerStream) SendMsg(any) error            { return nil }
func (f fakeServerStream) RecvMsg(any) error            { return nil }

func (d *dlStream) Send(m *inventoryv1.DownloadAgentReleaseResponse) error {
	if d.gate != nil {
		<-d.gate
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.msgs = append(d.msgs, m)
	return nil
}

func TestDownloadAgentRelease(t *testing.T) {
	h := newUpgradeHarness(t, 4)
	ctx, id := h.agentCtx(t, "tenant-a", "node-1", "4.4.0")
	h.connect(t, ctx, streamReq("4.4.0", "upgrade.v1"))
	waitOnline(t, h.reg, id, true)
	ctx = h.refresh(t, ctx)
	res, _ := h.upg.Request(context.Background(), "tenant-a", upgrades.Actor{Kind: upgrades.ActorUser, ID: "u"}, []string{id}, false)
	reqID := res.Created[0].ID

	st := &dlStream{fakeServerStream: fakeServerStream{ctx: ctx}}
	if err := h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{RequestId: reqID, Version: "4.5.0"}, st); err != nil {
		t.Fatal(err)
	}
	hdr := st.msgs[0].GetHeader()
	if hdr == nil {
		t.Fatal("first message is not the header")
	}
	m, err := h.keys.Verify(hdr.GetManifest(), hdr.GetSignature(), hdr.GetKeyId())
	if err != nil || hdr.GetFile() != "agent.deb" || hdr.GetSize() != int64(len(h.data["4.5.0"])) {
		t.Fatalf("header = %v %v", hdr, err)
	}
	a, _ := m.Artifact(debAmd64)
	v := agentrelease.NewArtifactVerifier(a)
	var next int64
	for _, msg := range st.msgs[1:] {
		c := msg.GetChunk()
		if c == nil || c.GetOffset() != next || len(c.GetData()) > 4096 {
			t.Fatalf("chunk at %d: %v", next, c)
		}
		next += int64(len(c.GetData()))
		_, _ = v.Write(c.GetData())
	}
	if err := v.Finish(); err != nil {
		t.Fatalf("downloaded artifact: %v", err)
	}
	// The own current version (rollback package) needs no request.
	st = &dlStream{fakeServerStream: fakeServerStream{ctx: ctx}}
	if err := h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{Version: "4.4.0"}, st); err != nil || st.msgs[0].GetHeader() == nil {
		t.Fatalf("rollback package = %v", err)
	}
	cases := []struct {
		name string
		ctx  context.Context
		req  *inventoryv1.DownloadAgentReleaseRequest
		code codes.Code
	}{
		{"no credential", context.Background(), &inventoryv1.DownloadAgentReleaseRequest{RequestId: reqID, Version: "4.5.0"}, codes.Unauthenticated},
		{"version != target", ctx, &inventoryv1.DownloadAgentReleaseRequest{RequestId: reqID, Version: "4.4.9"}, codes.NotFound},
		{"no request", ctx, &inventoryv1.DownloadAgentReleaseRequest{Version: "4.5.0"}, codes.NotFound},
		{"unknown request", ctx, &inventoryv1.DownloadAgentReleaseRequest{RequestId: "nope", Version: "4.5.0"}, codes.NotFound},
	}
	for _, c := range cases {
		err := h.srv.DownloadAgentRelease(c.req, &dlStream{fakeServerStream: fakeServerStream{ctx: c.ctx}})
		if got := codeOf(err); got != c.code {
			t.Errorf("%s: code = %v (%v), want %v", c.name, got, err, c.code)
		}
	}
	// Another agent's request: NotFound and audited.
	other, otherID := h.agentCtx(t, "tenant-a", "node-2", "4.4.0")
	n := len(h.mem.AuditRows())
	err = h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{RequestId: reqID, Version: "4.5.0"}, &dlStream{fakeServerStream: fakeServerStream{ctx: other}})
	requireCode(t, err, codes.NotFound)
	rows := h.mem.AuditRows()
	if len(rows) != n+1 || rows[n].Action != "agent_upgrade_refused" || rows[n].ActorID != otherID {
		t.Fatalf("refusal audit = %+v", rows[n:])
	}
	// Another tenant's agent: NotFound too.
	foreign, _ := h.agentCtx(t, "tenant-b", "node-3", "4.4.0")
	err = h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{RequestId: reqID, Version: "4.5.0"}, &dlStream{fakeServerStream: fakeServerStream{ctx: foreign}})
	requireCode(t, err, codes.NotFound)
	// An agent without a stored platform gets NotFound (no artifact for it).
	err = h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{Version: "4.4.0"}, &dlStream{fakeServerStream: fakeServerStream{ctx: other}})
	requireCode(t, err, codes.NotFound)
	// Inactive request: FailedPrecondition.
	if _, err := h.srv.ReportUpgrade(ctx, &inventoryv1.ReportUpgradeRequest{RequestId: reqID, State: "failed", Reason: "disk_full"}); err != nil {
		t.Fatal(err)
	}
	err = h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{RequestId: reqID, Version: "4.5.0"}, &dlStream{fakeServerStream: fakeServerStream{ctx: ctx}})
	requireCode(t, err, codes.FailedPrecondition)
	// A store failure while streaming chunks is Unavailable.
	h.mem.FailNext("ReadArtifactChunk")
	err = h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{Version: "4.4.0"}, &dlStream{fakeServerStream: fakeServerStream{ctx: ctx}})
	requireCode(t, err, codes.Unavailable)
	h.mem.FailNext("GetAgentUpgrade")
	err = h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{RequestId: reqID, Version: "4.5.0"}, &dlStream{fakeServerStream: fakeServerStream{ctx: ctx}})
	requireCode(t, err, codes.Unavailable)
}

func codeOf(err error) codes.Code { return status.Code(err) }

func TestDownloadCaps(t *testing.T) {
	h := newUpgradeHarness(t, 1)
	ctx, id := h.agentCtx(t, "tenant-a", "node-1", "4.4.0")
	h.connect(t, ctx, streamReq("4.4.0", "upgrade.v1"))
	waitOnline(t, h.reg, id, true)
	ctx = h.refresh(t, ctx)
	gate := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{Version: "4.4.0"}, &dlStream{fakeServerStream: fakeServerStream{ctx: ctx}, gate: gate})
	}()
	waitFor(t, func() bool { return h.srv.activeDownloads() == 1 })
	// Second stream of the same agent.
	err := h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{Version: "4.4.0"}, &dlStream{fakeServerStream: fakeServerStream{ctx: ctx}})
	requireCode(t, err, codes.ResourceExhausted)
	// Global cap (1) reached by another agent.
	ctx2, id2 := h.agentCtx(t, "tenant-a", "node-2", "4.4.0")
	h.connect(t, ctx2, streamReq("4.4.0", "upgrade.v1"))
	waitOnline(t, h.reg, id2, true)
	ctx2 = h.refresh(t, ctx2)
	err = h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{Version: "4.4.0"}, &dlStream{fakeServerStream: fakeServerStream{ctx: ctx2}})
	requireCode(t, err, codes.ResourceExhausted)
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if h.srv.activeDownloads() != 0 {
		t.Fatal("slot not released")
	}
	// The stream deadline ends a stalled download.
	h.srv.downloadDeadline = 50 * time.Millisecond
	gate = make(chan struct{})
	err = h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{Version: "4.4.0"}, &dlStream{fakeServerStream: fakeServerStream{ctx: ctx}, gate: closeAfter(gate, 120*time.Millisecond)})
	requireCode(t, err, codes.DeadlineExceeded)
}

func closeAfter(ch chan struct{}, d time.Duration) chan struct{} {
	go func() { time.Sleep(d); close(ch) }()
	return ch
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestReportUpgrade(t *testing.T) {
	h := newUpgradeHarness(t, 4)
	ctx, id := h.agentCtx(t, "tenant-a", "node-1", "4.4.0")
	h.connect(t, ctx, streamReq("4.4.0", "upgrade.v1"))
	waitOnline(t, h.reg, id, true)
	ctx = h.refresh(t, ctx)
	res, _ := h.upg.Request(context.Background(), "tenant-a", upgrades.Actor{Kind: upgrades.ActorUser, ID: "u"}, []string{id}, false)
	reqID := res.Created[0].ID
	for name, req := range map[string]*inventoryv1.ReportUpgradeRequest{
		"unknown state":  {RequestId: reqID, State: "exploded"},
		"unknown reason": {RequestId: reqID, State: "failed", Reason: "gremlins"},
		"long detail":    {RequestId: reqID, State: "failed", Reason: "busy", Detail: strings.Repeat("d", 257)},
	} {
		_, err := h.srv.ReportUpgrade(ctx, req)
		if codeOf(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, state := range []string{"downloading", "installing"} {
		if r, err := h.srv.ReportUpgrade(ctx, &inventoryv1.ReportUpgradeRequest{RequestId: reqID, State: state, FromVersion: "4.4.0", ToVersion: "4.5.0"}); err != nil || !r.GetAccepted() {
			t.Fatalf("%s = %v %v", state, r, err)
		}
	}
	// succeeded from a connection still on the old version: ignored.
	r, err := h.srv.ReportUpgrade(ctx, &inventoryv1.ReportUpgradeRequest{RequestId: reqID, State: "succeeded", ToVersion: "4.5.0"})
	if err != nil || r.GetAccepted() {
		t.Fatalf("succeeded on the old version = %v %v", r, err)
	}
	_ = h.mem.TouchAgent(context.Background(), id, "4.5.0", "", time.Now())
	ctx = h.refresh(t, ctx)
	r, err = h.srv.ReportUpgrade(ctx, &inventoryv1.ReportUpgradeRequest{RequestId: reqID, State: "succeeded", ToVersion: "4.5.0"})
	if err != nil || !r.GetAccepted() {
		t.Fatalf("succeeded = %v %v", r, err)
	}
	other, _ := h.agentCtx(t, "tenant-a", "node-2", "4.4.0")
	_, err = h.srv.ReportUpgrade(other, &inventoryv1.ReportUpgradeRequest{RequestId: reqID, State: "failed", Reason: "busy"})
	requireCode(t, err, codes.NotFound)
	_, err = h.srv.ReportUpgrade(context.Background(), &inventoryv1.ReportUpgradeRequest{RequestId: reqID, State: "failed"})
	requireCode(t, err, codes.Unauthenticated)
	h.mem.FailNext("GetAgentUpgrade")
	_, err = h.srv.ReportUpgrade(ctx, &inventoryv1.ReportUpgradeRequest{RequestId: reqID, State: "failed", Reason: "busy"})
	requireCode(t, err, codes.Unavailable)
}

func TestUpgradeRPCsWithoutService(t *testing.T) {
	h := newHarness(t, 0)
	id, cred := h.mintAndEnroll(t, "tenant-a", "n")
	ctx := h.authedCtx(t, id, cred)
	a, _ := h.mem.GetAgentByID(context.Background(), id)
	ctx = withAgent(ctx, a)
	_, err := h.srv.CheckAgentUpdate(ctx, &inventoryv1.CheckAgentUpdateRequest{})
	requireCode(t, err, codes.Unimplemented)
	_, err = h.srv.ReportUpgrade(ctx, &inventoryv1.ReportUpgradeRequest{})
	requireCode(t, err, codes.Unimplemented)
	err = h.srv.DownloadAgentRelease(&inventoryv1.DownloadAgentReleaseRequest{}, &dlStream{fakeServerStream: fakeServerStream{ctx: ctx}})
	requireCode(t, err, codes.Unimplemented)
}

func TestRegistryCommandJSON(t *testing.T) {
	// The Valkey registry moves commands as JSON: the upgrade payload survives.
	in := registry.Command{ID: "c", Type: registry.CommandUpgrade, Upgrade: &registry.UpgradePayload{RequestID: "r", TargetVersion: "4.5.0", AllowDowngrade: true}}
	b, _ := json.Marshal(in)
	var out registry.Command
	if err := json.Unmarshal(b, &out); err != nil || out.Upgrade == nil || *out.Upgrade != *in.Upgrade || out.Type != "upgrade" {
		t.Fatalf("round trip = %+v %v", out, err)
	}
	if got := commandToPB(registry.Command{ID: "x", Type: "refresh"}); got.GetType() != inventoryv1.CommandType_COMMAND_TYPE_REFRESH || got.GetUpgrade() != nil {
		t.Fatalf("refresh = %v", got)
	}
}
