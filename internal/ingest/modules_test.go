package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/moduledelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// bundleSource renders data for every request (as the gateway would).
type bundleSource struct {
	data []byte
	gate chan struct{} // when set, RenderModuleBundle waits for it
	mu   sync.Mutex
	reqs []*inventoryv1.RenderModuleBundleRequest
}

type chunkReader struct {
	grpc.ClientStream
	msgs []*inventoryv1.ModuleBundleChunk
}

func (c *chunkReader) Recv() (*inventoryv1.ModuleBundleChunk, error) {
	if len(c.msgs) == 0 {
		return nil, io.EOF
	}
	m := c.msgs[0]
	c.msgs = c.msgs[1:]
	return m, nil
}

func (b *bundleSource) RenderModuleBundle(_ context.Context, in *inventoryv1.RenderModuleBundleRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[inventoryv1.ModuleBundleChunk], error) {
	if b.gate != nil {
		<-b.gate
	}
	b.mu.Lock()
	b.reqs = append(b.reqs, in)
	b.mu.Unlock()
	sum := sha256.Sum256(b.data)
	msgs := []*inventoryv1.ModuleBundleChunk{{Part: &inventoryv1.ModuleBundleChunk_Header{Header: &inventoryv1.ModuleBundleHeader{
		Module: "sms-gw", Version: "1.2.0", Size: int64(len(b.data)), Sha256: hex.EncodeToString(sum[:])}}}}
	for off := 0; off < len(b.data); off += 1 << 20 {
		end := min(off+1<<20, len(b.data))
		msgs = append(msgs, &inventoryv1.ModuleBundleChunk{Part: &inventoryv1.ModuleBundleChunk_Chunk{
			Chunk: &inventoryv1.ArtifactChunk{Offset: int64(off), Data: append([]byte(nil), b.data[off:end]...)}}})
	}
	return &chunkReader{msgs: msgs}, nil
}

// wipeWatch records the bundles the edge got, to check they are zeroed.
type wipeWatch struct {
	ModuleDelivery
	mu      sync.Mutex
	bundles []*moduledelivery.Bundle
}

func (w *wipeWatch) Fetch(ctx context.Context, a store.Agent, itemID string) (*moduledelivery.Bundle, error) {
	b, err := w.ModuleDelivery.Fetch(ctx, a, itemID)
	if b != nil {
		w.mu.Lock()
		w.bundles = append(w.bundles, b)
		w.mu.Unlock()
	}
	return b, err
}

type moduleHarness struct {
	*harness
	src   *bundleSource
	svc   *moduledelivery.Service
	watch *wipeWatch
}

func newModuleHarness(t *testing.T, maxFetches int, plaintext bool) *moduleHarness {
	t.Helper()
	h := newHarness(t, 0)
	data := bytes.Repeat([]byte("zip-with-secrets."), (5<<20)/17) // > 2 chunks
	src := &bundleSource{data: data}
	svc := moduledelivery.New(h.mem, src, h.reg, moduledelivery.Config{Enabled: true, ReportTimeout: 30 * time.Minute, SourceTimeout: time.Minute})
	w := &wipeWatch{ModuleDelivery: svc}
	h.srv.WithModuleDelivery(w, maxFetches, plaintext)
	return &moduleHarness{harness: h, src: src, svc: svc, watch: w}
}

// enrollModuleAgent enrolls an agent with module.v1 for hostname.
func (h *moduleHarness) enrollModuleAgent(t *testing.T, hostname string) (context.Context, store.Agent, string) {
	t.Helper()
	id, cred := h.mintAndEnroll(t, certTenant, hostname)
	host, err := h.mem.ResolveHost(context.Background(), certTenant, store.Host{Hostname: hostname, HardwareUUID: "uuid-" + hostname})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.mem.TouchAgent(context.Background(), id, "4.8.0", host.ID, time.Now())
	_ = h.mem.SetAgentPlatform(context.Background(), id, "linux", "amd64", "deb", []string{store.CapModuleV1}, time.Now())
	ctx := h.authedCtx(t, id, cred)
	a, _ := AgentFromContext(ctx)
	return ctx, a, host.ID
}

func (h *moduleHarness) deliver(t *testing.T, n int, hostID string) string {
	t.Helper()
	v, err := h.svc.Create(context.Background(), moduledelivery.Request{TenantID: certTenant, Source: "gateway",
		DeliveryID: "0190f7c2-6a3e-7c1a-9b2e-00000000000" + string(rune('0'+n)), HostID: hostID, Module: "sms-gw", Version: "1.2.0",
		ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

// bundleStream collects FetchModuleBundle messages.
type bundleStream struct {
	fakeServerStream
	mu      sync.Mutex
	msgs    []*inventoryv1.ModuleBundleChunk
	failAt  int // Send number (1-based) that fails; 0 = none
	sends   int
	release chan struct{}
}

func (b *bundleStream) Send(m *inventoryv1.ModuleBundleChunk) error {
	if b.release != nil {
		<-b.release
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sends++
	if b.sends == b.failAt {
		return errors.New("broken pipe")
	}
	// gRPC serialises the message inside Send; keep a copy, as the edge
	// zeroes its buffer once the handler returns.
	b.msgs = append(b.msgs, proto.Clone(m).(*inventoryv1.ModuleBundleChunk))
	return nil
}

// TestFetchModuleBundleStreams (T007/T010): the header names the item and
// the bundle; chunks of at most 1 MiB are contiguous and add up to the
// size and digest; the source got the verified agent's host; the buffer is
// zeroed once the handler returned; the report is accepted.
func TestFetchModuleBundleStreams(t *testing.T) {
	h := newModuleHarness(t, 10, false)
	ctx, _, hostID := h.enrollModuleAgent(t, "gw-1")
	itemID := h.deliver(t, 1, hostID)
	st := &bundleStream{fakeServerStream: fakeServerStream{ctx: ctx}}
	if err := h.srv.FetchModuleBundle(&inventoryv1.FetchModuleBundleRequest{ItemId: itemID}, st); err != nil {
		t.Fatal(err)
	}
	hdr := st.msgs[0].GetHeader()
	if hdr == nil || hdr.GetItemId() != itemID || hdr.GetModule() != "sms-gw" || hdr.GetVersion() != "1.2.0" ||
		hdr.GetSize() != int64(len(h.src.data)) || hdr.GetDeliveryId() == "" {
		t.Fatalf("header = %v", hdr)
	}
	var got []byte
	for _, m := range st.msgs[1:] {
		c := m.GetChunk()
		if c == nil || c.GetOffset() != int64(len(got)) || len(c.GetData()) > 1<<20 {
			t.Fatalf("chunk at %d", len(got))
		}
		got = append(got, c.GetData()...)
	}
	sum := sha256.Sum256(got)
	if !bytes.Equal(got, h.src.data) || hex.EncodeToString(sum[:]) != hdr.GetSha256() || len(st.msgs) != 6 {
		t.Fatalf("bundle mismatch (%d messages)", len(st.msgs))
	}
	if h.src.reqs[0].GetHostId() != hostID || h.src.reqs[0].GetItemId() != itemID || h.src.reqs[0].GetTenantId() != certTenant {
		t.Fatalf("source request = %v", h.src.reqs[0])
	}
	if b := h.watch.bundles[0]; b.Data != nil {
		t.Fatal("bundle not wiped")
	}
	resp, err := h.srv.ReportModule(ctx, &inventoryv1.ReportModuleRequest{ItemId: itemID, State: "installed", HookExitCode: 0})
	if err != nil || !resp.GetAccepted() {
		t.Fatalf("report: %v %v", resp, err)
	}
	resp, err = h.srv.ReportModule(ctx, &inventoryv1.ReportModuleRequest{ItemId: itemID, State: "installed", HookExitCode: 0})
	if err != nil || !resp.GetAccepted() {
		t.Fatalf("duplicate report: %v %v", resp, err)
	}
	if _, err := h.srv.ReportModule(ctx, &inventoryv1.ReportModuleRequest{ItemId: itemID, State: "done"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad report: %v", err)
	}
	// Send failures end the stream (header, then a chunk).
	for _, at := range []int{1, 2} {
		st := &bundleStream{fakeServerStream: fakeServerStream{ctx: ctx}, failAt: at}
		if err := h.srv.FetchModuleBundle(&inventoryv1.FetchModuleBundleRequest{ItemId: h.deliver(t, 2+at, hostID)}, st); err == nil {
			t.Fatalf("send failure %d hidden", at)
		}
	}
}

// TestModuleEdgeGuards (T007): credential required; another agent's item
// is NotFound; the plaintext edge refuses and audits; a disabled relay or
// none is FailedPrecondition disabled_on_server.
func TestModuleEdgeGuards(t *testing.T) {
	h := newModuleHarness(t, 10, false)
	ctx, _, hostID := h.enrollModuleAgent(t, "gw-1")
	other, _, _ := h.enrollModuleAgent(t, "gw-2")
	itemID := h.deliver(t, 1, hostID)
	fetch := func(c context.Context, id string) error {
		return h.srv.FetchModuleBundle(&inventoryv1.FetchModuleBundleRequest{ItemId: id}, &bundleStream{fakeServerStream: fakeServerStream{ctx: c}})
	}
	if err := fetch(context.Background(), itemID); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no credential: %v", err)
	}
	if _, err := h.srv.ReportModule(context.Background(), &inventoryv1.ReportModuleRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("report without credential: %v", err)
	}
	if err := fetch(other, itemID); status.Code(err) != codes.NotFound {
		t.Fatalf("other agent: %v", err)
	}
	if err := fetch(ctx, "x"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed: %v", err)
	}
	if len(h.src.reqs) != 0 {
		t.Fatal("rendered for a refused fetch")
	}
	// Plaintext edge without allow_plaintext_ingest: refused and audited.
	h2 := newModuleHarness(t, 10, true)
	ctx2, a2, host2 := h2.enrollModuleAgent(t, "gw-1")
	item2 := h2.deliver(t, 1, host2)
	err := h2.srv.FetchModuleBundle(&inventoryv1.FetchModuleBundleRequest{ItemId: item2}, &bundleStream{fakeServerStream: fakeServerStream{ctx: ctx2}})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("plaintext: %v", err)
	}
	found := false
	for _, r := range h2.mem.AuditRows() {
		if r.Action == "module_delivery_refused" && r.Reason == "plaintext" && r.ActorID == a2.ID {
			found = true
		}
	}
	if !found || len(h2.src.reqs) != 0 {
		t.Fatal("plaintext refusal not audited (or rendered)")
	}
	// No relay attached / disabled relay.
	bare := newHarness(t, 0)
	id, cred := bare.mintAndEnroll(t, certTenant, "x")
	bctx := bare.authedCtx(t, id, cred)
	if err := bare.srv.FetchModuleBundle(&inventoryv1.FetchModuleBundleRequest{}, &bundleStream{fakeServerStream: fakeServerStream{ctx: bctx}}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no edge: %v", err)
	}
	if _, err := bare.srv.ReportModule(bctx, &inventoryv1.ReportModuleRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no edge report: %v", err)
	}
	h.srv.moduleEdge.svc = moduledelivery.New(h.mem, nil, h.reg, moduledelivery.Config{})
	err = fetch(ctx, itemID)
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || st.Message() != "disabled_on_server" {
		t.Fatalf("disabled: %v", err)
	}
	if code := status.Code(moduleStatus(errors.New("x"))); code != codes.Unavailable {
		t.Fatal(code)
	}
}

// TestModuleFetchCaps (T010): one fetch per agent, a global cap.
func TestModuleFetchCaps(t *testing.T) {
	h := newModuleHarness(t, 1, false)
	ctx1, _, host1 := h.enrollModuleAgent(t, "gw-1")
	ctx2, _, host2 := h.enrollModuleAgent(t, "gw-2")
	item1 := h.deliver(t, 1, host1)
	item2 := h.deliver(t, 2, host2)
	h.src.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.srv.FetchModuleBundle(&inventoryv1.FetchModuleBundleRequest{ItemId: item1}, &bundleStream{fakeServerStream: fakeServerStream{ctx: ctx1}})
	}()
	for {
		h.srv.moduleEdge.mu.Lock()
		n := len(h.srv.moduleEdge.slots)
		h.srv.moduleEdge.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := h.srv.FetchModuleBundle(&inventoryv1.FetchModuleBundleRequest{ItemId: item1}, &bundleStream{fakeServerStream: fakeServerStream{ctx: ctx1}}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("per agent: %v", err)
	}
	if err := h.srv.FetchModuleBundle(&inventoryv1.FetchModuleBundleRequest{ItemId: item2}, &bundleStream{fakeServerStream: fakeServerStream{ctx: ctx2}}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("global: %v", err)
	}
	close(h.src.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := h.srv.FetchModuleBundle(&inventoryv1.FetchModuleBundleRequest{ItemId: item2}, &bundleStream{fakeServerStream: fakeServerStream{ctx: ctx2}}); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if New(nil, nil, nil, nil, 0, "").WithModuleDelivery(nil, 0, false).moduleEdge.slots == nil {
		t.Fatal("cap floor")
	}
}

// TestStreamCommandsModuleReplay (T010): a delivery created while the agent
// was offline is replayed on connect (ids only); a live one is pushed.
func TestStreamCommandsModuleReplay(t *testing.T) {
	h := newModuleHarness(t, 5, false)
	ctx, a, hostID := h.enrollModuleAgent(t, "gw-1")
	itemID := h.deliver(t, 1, hostID)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fs := &fakeStream{ctx: sctx, sent: make(chan *inventoryv1.Command, 16)}
	go func() {
		_ = h.srv.StreamCommands(&inventoryv1.StreamRequest{AgentVersion: "4.8.0", Capabilities: []string{"module.v1"},
			Platform: &inventoryv1.AgentPlatform{Os: "linux", Arch: "amd64", InstallType: "deb"}}, fs)
	}()
	c := <-fs.sent
	if c.GetType() != inventoryv1.CommandType_COMMAND_TYPE_MODULE || c.GetModule().GetItemId() != itemID ||
		c.GetModule().GetModule() != "sms-gw" || c.GetModule().GetAttempt() != 1 || c.GetCertificate() != nil {
		t.Fatalf("replayed = %v", c)
	}
	waitOnline(t, h.reg, a.ID, true)
	r := moduledelivery.Request{TenantID: certTenant, Source: "gateway", DeliveryID: "0190f7c2-6a3e-7c1a-9b2e-000000000099", HostID: hostID,
		Module: "mail", Version: "1.0.0", ExpiresAt: time.Now().Add(time.Hour)}
	v, err := h.svc.Create(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if c = <-fs.sent; c.GetModule().GetItemId() != v.ID {
		t.Fatalf("live = %v", c)
	}
	h.mem.FailNext("ListActiveModuleItemsForAgent")
	if h.srv.moduleCommands(ctx, a) != nil {
		t.Fatal("commands on error")
	}
}

func TestCommandToPBModule(t *testing.T) {
	pb := commandToPB(registry.Command{ID: "i1", Type: registry.CommandModule, Module: &registry.ModulePayload{ItemID: "i1", Module: "sms-gw", Attempt: 2}})
	if pb.GetType() != inventoryv1.CommandType_COMMAND_TYPE_MODULE || pb.GetModule().GetItemId() != "i1" || pb.GetModule().GetModule() != "sms-gw" ||
		pb.GetModule().GetAttempt() != 2 {
		t.Fatalf("pb = %v", pb)
	}
}
