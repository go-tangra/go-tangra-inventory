package ingest

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protoreflect"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/certdelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const certTenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

func certFixture(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "certmaterial", "testdata", name)) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ingestLCM serves cert-1 (RSA fixture) and can be slowed down.
type ingestLCM struct {
	t     testing.TB
	gate  chan struct{} // when set, Download waits for it
	keys  [][]byte
	mu    sync.Mutex
	fails error
}

func (l *ingestLCM) Download(_ context.Context, _, _ string, includeKey bool) (lcmclient.Bundle, error) {
	if l.gate != nil {
		<-l.gate
	}
	if l.fails != nil {
		return lcmclient.Bundle{}, l.fails
	}
	b := lcmclient.Bundle{CertPEM: certFixture(l.t, "rsa.crt"), ChainPEM: certFixture(l.t, "chain.pem"),
		NotAfter: time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC), Status: lcmclient.StatusActive}
	if includeKey {
		b.KeyPEM = []byte(certFixture(l.t, "rsa.pkcs8.key"))
		l.mu.Lock()
		l.keys = append(l.keys, b.KeyPEM)
		l.mu.Unlock()
	}
	return b, nil
}

type certHarness struct {
	*harness
	lcm *ingestLCM
	svc *certdelivery.Service
}

func newCertHarness(t *testing.T, maxFetches int, plaintext bool) *certHarness {
	t.Helper()
	h := newHarness(t, 0)
	lcm := &ingestLCM{t: t}
	svc := certdelivery.New(h.mem, lcm, h.reg, events.HubPublisher{},
		certdelivery.Config{Enabled: true, PendingTTL: 168 * time.Hour, ReportTimeout: 15 * time.Minute})
	h.srv.WithCertDelivery(svc, maxFetches, plaintext)
	return &certHarness{harness: h, lcm: lcm, svc: svc}
}

// enrollCertAgent enrolls an agent with cert.v1 for hostname and returns
// its context, agent and host id.
func (h *certHarness) enrollCertAgent(t *testing.T, hostname string) (context.Context, store.Agent, string) {
	t.Helper()
	id, cred := h.mintAndEnroll(t, certTenant, hostname)
	host, err := h.mem.ResolveHost(context.Background(), certTenant, store.Host{Hostname: hostname, HardwareUUID: "uuid-" + hostname})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.mem.TouchAgent(context.Background(), id, "4.7.0", host.ID, time.Now())
	_ = h.mem.SetAgentPlatform(context.Background(), id, "linux", "amd64", "deb", []string{store.CapCertV1}, time.Now())
	ctx := h.authedCtx(t, id, cred)
	a, _ := AgentFromContext(ctx)
	return ctx, a, host.ID
}

// deliver creates a delivery of cert-1 to hostID.
func (h *certHarness) deliver(t *testing.T, key, hostID string) string {
	t.Helper()
	v, err := h.svc.Create(context.Background(), certdelivery.Request{TenantID: certTenant, Source: "deployer", RequestedBy: "spiffe://x/svc/deployer",
		IdempotencyKey: key, Trigger: "manual", CertificateID: "cert-1", Name: "www", KeyPolicy: "require", HostIDs: []string{hostID}})
	if err != nil {
		t.Fatal(err)
	}
	return v.Items[0].ID
}

// TestFetchAndReportCertificate (T035): bundle mapping; report; errors.
func TestFetchAndReportCertificate(t *testing.T) {
	h := newCertHarness(t, 10, false)
	ctx, _, hostID := h.enrollCertAgent(t, "web-1")
	itemID := h.deliver(t, "job-1", hostID)
	b, err := h.srv.FetchCertificate(ctx, &inventoryv1.FetchCertificateRequest{ItemId: itemID})
	if err != nil {
		t.Fatal(err)
	}
	if b.GetItemId() != itemID || b.GetName() != "www" || b.GetCertificateId() != "cert-1" || !b.GetHasKey() ||
		!strings.Contains(b.GetKeyPem(), "PRIVATE KEY") || !strings.Contains(b.GetCertPem(), "CERTIFICATE") || !strings.Contains(b.GetChainPem(), "CERTIFICATE") ||
		len(b.GetFingerprintSha256()) != 64 || b.GetSerial() == "" || b.GetCommonName() != "www.example.com" || len(b.GetDnsNames()) != 2 ||
		len(b.GetIpAddresses()) != 2 || b.GetNotBefore() == 0 || b.GetNotAfter() <= b.GetNotBefore() || b.GetIsRenewal() || b.GetRerunHook() {
		t.Fatalf("bundle = %v", b)
	}
	// The lcm key buffer is zeroed once the handler returned.
	if bytes.ContainsFunc(h.lcm.keys[0], func(r rune) bool { return r != 0 }) {
		t.Fatal("key buffer not zeroed")
	}
	resp, err := h.srv.ReportCertificate(ctx, &inventoryv1.ReportCertificateRequest{ItemId: itemID, State: "installed",
		FingerprintSha256: b.GetFingerprintSha256(), Serial: b.GetSerial(), HookExitCode: -1})
	if err != nil || !resp.GetAccepted() {
		t.Fatalf("report: %v %v", resp, err)
	}
	resp, err = h.srv.ReportCertificate(ctx, &inventoryv1.ReportCertificateRequest{ItemId: itemID, State: "failed", HookExitCode: -1})
	if err != nil || resp.GetAccepted() {
		t.Fatalf("terminal report: %v %v", resp, err)
	}
	for _, c := range []struct {
		err  error
		code codes.Code
	}{
		{nil, codes.NotFound}, // terminal item
	} {
		_, err := h.srv.FetchCertificate(ctx, &inventoryv1.FetchCertificateRequest{ItemId: itemID})
		if status.Code(err) != c.code {
			t.Fatalf("terminal fetch: %v", err)
		}
	}
	if _, err := h.srv.FetchCertificate(ctx, &inventoryv1.FetchCertificateRequest{ItemId: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := h.srv.ReportCertificate(ctx, &inventoryv1.ReportCertificateRequest{ItemId: itemID, State: "bogus"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad report: %v", err)
	}
	if _, err := h.srv.ReportCertificate(ctx, &inventoryv1.ReportCertificateRequest{ItemId: "0190f7c2-6a3e-7c1a-9b2e-000000000009", State: "failed", HookExitCode: -1}); status.Code(err) != codes.NotFound {
		t.Fatalf("foreign report: %v", err)
	}
	// Server-side failures: FailedPrecondition with the reason; lcm down: Unavailable.
	item2 := h.deliver(t, "job-2", hostID)
	h.lcm.fails = lcmclient.ErrUnavailable
	if _, err := h.srv.FetchCertificate(ctx, &inventoryv1.FetchCertificateRequest{ItemId: item2}); status.Code(err) != codes.Unavailable {
		t.Fatalf("lcm down: %v", err)
	}
	h.lcm.fails = lcmclient.ErrNoKey
	if _, err := h.srv.FetchCertificate(ctx, &inventoryv1.FetchCertificateRequest{ItemId: item2}); status.Code(err) != codes.FailedPrecondition ||
		!strings.Contains(err.Error(), "key_unavailable") {
		t.Fatalf("no key: %v", err)
	}
}

func TestCertificateEdgeGuards(t *testing.T) {
	// No credential on the context.
	h := newCertHarness(t, 1, false)
	if _, err := h.srv.FetchCertificate(context.Background(), &inventoryv1.FetchCertificateRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	if _, err := h.srv.ReportCertificate(context.Background(), &inventoryv1.ReportCertificateRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	// Not attached: disabled.
	plain := newHarness(t, 0)
	ctx := withAgent(context.Background(), store.Agent{ID: "a", TenantID: certTenant})
	if _, err := plain.srv.FetchCertificate(ctx, &inventoryv1.FetchCertificateRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err := plain.srv.ReportCertificate(ctx, &inventoryv1.ReportCertificateRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if plain.srv.certCommands(ctx, store.Agent{}) != nil {
		t.Fatal("commands without the relay")
	}
	// Plaintext edge without allow_plaintext_ingest: refused and audited.
	h = newCertHarness(t, 1, true)
	actx, a, hostID := h.enrollCertAgent(t, "web-1")
	itemID := h.deliver(t, "job-1", hostID)
	if _, err := h.srv.FetchCertificate(actx, &inventoryv1.FetchCertificateRequest{ItemId: itemID}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("plaintext: %v", err)
	}
	found := false
	for _, r := range h.mem.AuditRows() {
		if r.Action == "cert_delivery_refused" && r.Reason == "plaintext" && r.ActorID == a.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("plaintext refusal not audited")
	}
	// Disabled relay: FailedPrecondition.
	h = newCertHarness(t, 1, false)
	actx, _, _ = h.enrollCertAgent(t, "web-1")
	h.srv.certEdge.svc = certdelivery.New(h.mem, nil, h.reg, events.HubPublisher{}, certdelivery.Config{})
	if _, err := h.srv.FetchCertificate(actx, &inventoryv1.FetchCertificateRequest{ItemId: "x"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("disabled: %v", err)
	}
	if code := status.Code(certStatus(errors.New("x"))); code != codes.Unavailable {
		t.Fatal(code)
	}
}

// TestCertificateFetchCaps (T035): one fetch per agent, a global cap.
func TestCertificateFetchCaps(t *testing.T) {
	h := newCertHarness(t, 1, false)
	ctx1, _, host1 := h.enrollCertAgent(t, "web-1")
	ctx2, _, host2 := h.enrollCertAgent(t, "web-2")
	item1 := h.deliver(t, "job-1", host1)
	item2 := h.deliver(t, "job-2", host2)
	h.lcm.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := h.srv.FetchCertificate(ctx1, &inventoryv1.FetchCertificateRequest{ItemId: item1})
		done <- err
	}()
	for len(h.srv.certEdge.slots) == 0 {
		time.Sleep(time.Millisecond)
	}
	if _, err := h.srv.FetchCertificate(ctx1, &inventoryv1.FetchCertificateRequest{ItemId: item1}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("per agent: %v", err)
	}
	if _, err := h.srv.FetchCertificate(ctx2, &inventoryv1.FetchCertificateRequest{ItemId: item2}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("global: %v", err)
	}
	close(h.lcm.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := h.srv.FetchCertificate(ctx2, &inventoryv1.FetchCertificateRequest{ItemId: item2}); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if New(nil, nil, nil, nil, 0, "").WithCertDelivery(nil, 0, false).certEdge.slots == nil {
		t.Fatal("cap floor")
	}
}

// TestStreamCommandsCertificateReplay (T059): upgrade replay, then
// certificate replay (ids only), then live commands.
func TestStreamCommandsCertificateReplay(t *testing.T) {
	h := newCertHarness(t, 5, false)
	ctx, a, hostID := h.enrollCertAgent(t, "web-1")
	_ = h.reg // agent offline while the delivery is created
	itemID := h.deliver(t, "job-1", hostID)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fs := &fakeStream{ctx: sctx, sent: make(chan *inventoryv1.Command, 16)}
	go func() {
		_ = h.srv.StreamCommands(&inventoryv1.StreamRequest{AgentVersion: "4.7.0", Capabilities: []string{"cert.v1"},
			Platform: &inventoryv1.AgentPlatform{Os: "linux", Arch: "amd64", InstallType: "deb"}}, fs)
	}()
	c := <-fs.sent
	if c.GetType() != inventoryv1.CommandType_COMMAND_TYPE_CERTIFICATE || c.GetCertificate().GetItemId() != itemID ||
		c.GetCertificate().GetName() != "www" || c.GetCommandId() != itemID || c.GetUpgrade() != nil {
		t.Fatalf("replayed = %v", c)
	}
	waitOnline(t, h.reg, a.ID, true)
	// Live: a new delivery is pushed on the open stream.
	item2 := h.deliver(t, "job-2", hostID)
	c = <-fs.sent
	if c.GetCertificate().GetItemId() != item2 {
		t.Fatalf("live = %v", c)
	}
	// OnConnect errors are swallowed (replayed next time).
	h.mem.FailNext("ListActiveCertItemsForAgent")
	if h.srv.certCommands(ctx, a) != nil {
		t.Fatal("commands on error")
	}
}

// TestCommandToPBCertificate maps ids only.
func TestCommandToPBCertificate(t *testing.T) {
	pb := commandToPB(registry.Command{ID: "i1", Type: registry.CommandCertificate, Certificate: &registry.CertificatePayload{ItemID: "i1", Name: "www", Attempt: 3}})
	if pb.GetType() != inventoryv1.CommandType_COMMAND_TYPE_CERTIFICATE || pb.GetCertificate().GetItemId() != "i1" || pb.GetCertificate().GetName() != "www" ||
		pb.GetCertificate().GetAttempt() != 3 {
		t.Fatalf("pb = %v", pb)
	}
}

// TestCertificateMessagesCarryNoServerControl (T069, server side): the
// command and bundle carry no path, command, environment, owner or mode
// field — what is written where and what runs is decided by the agent's
// local configuration only.
func TestCertificateMessagesCarryNoServerControl(t *testing.T) {
	forbidden := []string{"path", "dir", "file", "command", "cmd", "exec", "hook", "env", "owner", "group", "mode", "user", "script"}
	for _, md := range []protoreflect.MessageDescriptor{
		(&inventoryv1.CertificateCommand{}).ProtoReflect().Descriptor(),
		(&inventoryv1.CertificateBundle{}).ProtoReflect().Descriptor(),
		(&inventoryv1.FetchCertificateRequest{}).ProtoReflect().Descriptor(),
	} {
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			name := string(fields.Get(i).Name())
			if name == "rerun_hook" { // a boolean asking the agent to re-run its own local hook
				continue
			}
			for _, f := range forbidden {
				if strings.Contains(name, f) {
					t.Errorf("%s.%s looks like server-controlled %q", md.Name(), name, f)
				}
			}
			if fields.Get(i).Kind() == protoreflect.MessageKind {
				t.Errorf("%s.%s nests a message", md.Name(), name)
			}
		}
	}
}

// TestFetchCertificateOverGRPCNeverLogged (T035): over a real gRPC server
// the interceptor requires the agent credential, and neither slog nor the
// gRPC logger receives the bundle.
func TestFetchCertificateOverGRPCNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	var mu sync.Mutex
	w := lockedWriter{&mu, &logs}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	grpclog.SetLoggerV2(grpclog.NewLoggerV2WithVerbosity(w, w, w, 99))

	h := newCertHarness(t, 5, false)
	id, cred := h.mintAndEnroll(t, certTenant, "web-1")
	host, _ := h.mem.ResolveHost(context.Background(), certTenant, store.Host{Hostname: "web-1", HardwareUUID: "uuid-web-1"})
	_ = h.mem.TouchAgent(context.Background(), id, "4.7.0", host.ID, time.Now())
	itemID := h.deliver(t, "job-1", host.ID)

	lis := bufconn.Listen(1 << 20)
	gs := NewGRPCServer(h.srv)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	cl := inventoryv1.NewIngestServiceClient(conn)
	if _, err := cl.FetchCertificate(context.Background(), &inventoryv1.FetchCertificateRequest{ItemId: itemID}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no credential: %v", err)
	}
	actx := metadata.AppendToOutgoingContext(context.Background(), metaAgentIDKey, id, metaCredentialKey, cred)
	b, err := cl.FetchCertificate(actx, &inventoryv1.FetchCertificateRequest{ItemId: itemID})
	if err != nil || !b.GetHasKey() {
		t.Fatalf("fetch: %v %v", b, err)
	}
	if _, err := cl.ReportCertificate(actx, &inventoryv1.ReportCertificateRequest{ItemId: itemID, State: "installed",
		FingerprintSha256: b.GetFingerprintSha256(), HookExitCode: -1, Detail: b.GetKeyPem()}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	out := logs.String()
	mu.Unlock()
	if strings.Contains(out, "PRIVATE KEY") || strings.Contains(out, "BEGIN") {
		t.Fatalf("material logged: %s", out)
	}
	// The detail carrying PEM was dropped, never stored.
	if g, _ := h.mem.GetCertItem(context.Background(), certTenant, itemID); g.Detail != "" {
		t.Fatalf("detail stored: %q", g.Detail)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
