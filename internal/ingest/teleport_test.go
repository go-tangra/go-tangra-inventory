package ingest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/teleportjoin"
)

const tpCluster = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c88"

// joinSource renders join material for every request (as the teleport
// module would).
type joinSource struct {
	gate chan struct{} // when set, RenderTeleportJoin waits for it
	mu   sync.Mutex
	reqs []*inventoryv1.RenderTeleportJoinRequest
}

func (j *joinSource) RenderTeleportJoin(_ context.Context, in *inventoryv1.RenderTeleportJoinRequest, _ ...grpc.CallOption) (*inventoryv1.TeleportJoinMaterial, error) {
	if j.gate != nil {
		<-j.gate
	}
	j.mu.Lock()
	j.reqs = append(j.reqs, in)
	j.mu.Unlock()
	return &inventoryv1.TeleportJoinMaterial{Token: "join-token-secret", ProxyAddr: "tp.example.com:443",
		CaPins: []string{"sha256:" + fmt.Sprintf("%064x", 7)}, TeleportVersion: "17.4.2", EnrollmentId: in.GetEnrollmentId()}, nil
}

type teleportHarness struct {
	*harness
	src *joinSource
	svc *teleportjoin.Service
}

func newTeleportHarness(t *testing.T, maxFetches int, plaintext bool) *teleportHarness {
	t.Helper()
	h := newHarness(t, 0)
	src := &joinSource{}
	svc := teleportjoin.New(h.mem, src, h.reg, teleportjoin.Config{Enabled: true, ReportTimeout: 30 * time.Minute, SourceTimeout: time.Minute})
	h.srv.WithTeleportJoin(svc, maxFetches, plaintext)
	return &teleportHarness{harness: h, src: src, svc: svc}
}

// enrollTeleportAgent enrolls an agent with teleport.v1 for hostname.
func (h *teleportHarness) enrollTeleportAgent(t *testing.T, hostname string) (context.Context, store.Agent, string) {
	t.Helper()
	id, cred := h.mintAndEnroll(t, certTenant, hostname)
	host, err := h.mem.ResolveHost(context.Background(), certTenant, store.Host{Hostname: hostname, HardwareUUID: "uuid-" + hostname})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.mem.TouchAgent(context.Background(), id, "4.9.0", host.ID, time.Now())
	_ = h.mem.SetAgentPlatform(context.Background(), id, "linux", "amd64", "deb", []string{store.CapTeleportV1}, time.Now())
	ctx := h.authedCtx(t, id, cred)
	a, _ := AgentFromContext(ctx)
	return ctx, a, host.ID
}

func (h *teleportHarness) enroll(t *testing.T, n int, hostID string) string {
	t.Helper()
	v, err := h.svc.Create(context.Background(), teleportjoin.Request{TenantID: certTenant, Source: "teleport",
		EnrollmentID: fmt.Sprintf("0190f7c2-6a3e-7c1a-9b2e-%012d", n), HostID: hostID, ClusterID: tpCluster, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

// TestFetchTeleportJoin: the material carries the item id; the source got
// the verified agent's host; installing and joined reports are accepted, a
// repeat is not; nothing of the token is audited.
func TestFetchTeleportJoin(t *testing.T) {
	h := newTeleportHarness(t, 10, false)
	ctx, _, hostID := h.enrollTeleportAgent(t, "node-1")
	itemID := h.enroll(t, 1, hostID)
	m, err := h.srv.FetchTeleportJoin(ctx, &inventoryv1.FetchTeleportJoinRequest{ItemId: itemID})
	if err != nil {
		t.Fatal(err)
	}
	if m.GetItemId() != itemID || m.GetToken() != "join-token-secret" || m.GetProxyAddr() != "tp.example.com:443" {
		t.Fatalf("material = %v", m)
	}
	if h.src.reqs[0].GetHostId() != hostID || h.src.reqs[0].GetItemId() != itemID || h.src.reqs[0].GetTenantId() != certTenant {
		t.Fatalf("source request = %v", h.src.reqs[0])
	}
	resp, err := h.srv.ReportTeleportJoin(ctx, &inventoryv1.ReportTeleportJoinRequest{ItemId: itemID, State: "installing"})
	if err != nil || !resp.GetAccepted() {
		t.Fatalf("installing: %v %v", resp, err)
	}
	node := "5b0c7a4e-2f61-4c1e-9d3a-0e4f6a7b8c9d"
	resp, err = h.srv.ReportTeleportJoin(ctx, &inventoryv1.ReportTeleportJoinRequest{ItemId: itemID, State: "joined", NodeHostUuid: node})
	if err != nil || !resp.GetAccepted() {
		t.Fatalf("joined: %v %v", resp, err)
	}
	resp, err = h.srv.ReportTeleportJoin(ctx, &inventoryv1.ReportTeleportJoinRequest{ItemId: itemID, State: "joined", NodeHostUuid: node})
	if err != nil || resp.GetAccepted() {
		t.Fatalf("duplicate report: %v %v", resp, err)
	}
	if _, err := h.srv.ReportTeleportJoin(ctx, &inventoryv1.ReportTeleportJoinRequest{ItemId: itemID, State: "joined"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("joined without uuid: %v", err)
	}
	for _, r := range h.mem.AuditRows() {
		if s := fmt.Sprint(r.Detail); contains(s, "join-token-secret") {
			t.Fatalf("token audited: %s %v", r.Action, r.Detail)
		}
	}
}

// TestTeleportEdgeGuards: credential required; another agent's item is
// NotFound; the plaintext edge refuses and audits; a disabled relay or
// none is FailedPrecondition disabled_on_server.
func TestTeleportEdgeGuards(t *testing.T) {
	h := newTeleportHarness(t, 10, false)
	ctx, _, hostID := h.enrollTeleportAgent(t, "node-1")
	other, _, _ := h.enrollTeleportAgent(t, "node-2")
	itemID := h.enroll(t, 1, hostID)
	fetch := func(c context.Context, id string) error {
		_, err := h.srv.FetchTeleportJoin(c, &inventoryv1.FetchTeleportJoinRequest{ItemId: id})
		return err
	}
	if err := fetch(context.Background(), itemID); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no credential: %v", err)
	}
	if _, err := h.srv.ReportTeleportJoin(context.Background(), &inventoryv1.ReportTeleportJoinRequest{}); status.Code(err) != codes.Unauthenticated {
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
	h2 := newTeleportHarness(t, 10, true)
	ctx2, a2, host2 := h2.enrollTeleportAgent(t, "node-1")
	item2 := h2.enroll(t, 1, host2)
	if _, err := h2.srv.FetchTeleportJoin(ctx2, &inventoryv1.FetchTeleportJoinRequest{ItemId: item2}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("plaintext: %v", err)
	}
	found := false
	for _, r := range h2.mem.AuditRows() {
		if r.Action == "teleport_join_refused" && r.Reason == "plaintext" && r.ActorID == a2.ID {
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
	if _, err := bare.srv.FetchTeleportJoin(bctx, &inventoryv1.FetchTeleportJoinRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no edge: %v", err)
	}
	if _, err := bare.srv.ReportTeleportJoin(bctx, &inventoryv1.ReportTeleportJoinRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no edge report: %v", err)
	}
	if bare.srv.teleportCommands(bctx, store.Agent{}) != nil {
		t.Fatal("commands without an edge")
	}
	h.srv.teleportEdge.svc = teleportjoin.New(h.mem, nil, h.reg, teleportjoin.Config{})
	err := fetch(ctx, itemID)
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || st.Message() != "disabled_on_server" {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := h.srv.ReportTeleportJoin(ctx, &inventoryv1.ReportTeleportJoinRequest{ItemId: itemID, State: "failed", Reason: "busy"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("disabled report: %v", err)
	}
	if code := status.Code(teleportStatus(errors.New("x"))); code != codes.Unavailable {
		t.Fatal(code)
	}
}

// TestTeleportFetchCaps: one fetch per agent, a global cap.
func TestTeleportFetchCaps(t *testing.T) {
	h := newTeleportHarness(t, 1, false)
	ctx1, _, host1 := h.enrollTeleportAgent(t, "node-1")
	ctx2, _, host2 := h.enrollTeleportAgent(t, "node-2")
	item1 := h.enroll(t, 1, host1)
	item2 := h.enroll(t, 2, host2)
	h.src.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := h.srv.FetchTeleportJoin(ctx1, &inventoryv1.FetchTeleportJoinRequest{ItemId: item1})
		done <- err
	}()
	for {
		h.srv.teleportEdge.mu.Lock()
		n := len(h.srv.teleportEdge.slots)
		h.srv.teleportEdge.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := h.srv.FetchTeleportJoin(ctx1, &inventoryv1.FetchTeleportJoinRequest{ItemId: item1}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("per agent: %v", err)
	}
	if _, err := h.srv.FetchTeleportJoin(ctx2, &inventoryv1.FetchTeleportJoinRequest{ItemId: item2}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("global: %v", err)
	}
	close(h.src.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := h.srv.FetchTeleportJoin(ctx2, &inventoryv1.FetchTeleportJoinRequest{ItemId: item2}); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if New(nil, nil, nil, nil, 0, "").WithTeleportJoin(nil, 0, false).teleportEdge.slots == nil {
		t.Fatal("cap floor")
	}
}

// TestStreamCommandsTeleportReplay: an enrollment created while the agent
// was offline is replayed on connect (ids only); a live one is pushed.
func TestStreamCommandsTeleportReplay(t *testing.T) {
	h := newTeleportHarness(t, 5, false)
	ctx, a, hostID := h.enrollTeleportAgent(t, "node-1")
	itemID := h.enroll(t, 1, hostID)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fs := &fakeStream{ctx: sctx, sent: make(chan *inventoryv1.Command, 16)}
	go func() {
		_ = h.srv.StreamCommands(&inventoryv1.StreamRequest{AgentVersion: "4.9.0", Capabilities: []string{"teleport.v1"},
			Platform: &inventoryv1.AgentPlatform{Os: "linux", Arch: "amd64", InstallType: "deb"}}, fs)
	}()
	c := <-fs.sent
	if c.GetType() != inventoryv1.CommandType_COMMAND_TYPE_TELEPORT_JOIN || c.GetTeleportJoin().GetItemId() != itemID ||
		c.GetTeleportJoin().GetAttempt() != 1 || c.GetModule() != nil {
		t.Fatalf("replayed = %v", c)
	}
	waitOnline(t, h.reg, a.ID, true)
	h.enroll(t, 2, hostID) // same cluster: supersedes and is pushed live
	if c = <-fs.sent; c.GetTeleportJoin().GetItemId() == itemID || c.GetTeleportJoin().GetItemId() == "" {
		t.Fatalf("live = %v", c)
	}
	h.mem.FailNext("ListActiveTeleportItemsForAgent")
	if h.srv.teleportCommands(ctx, a) != nil {
		t.Fatal("commands on error")
	}
}

func TestCommandToPBTeleportJoin(t *testing.T) {
	pb := commandToPB(registry.Command{ID: "i1", Type: registry.CommandTeleportJoin, TeleportJoin: &registry.TeleportJoinPayload{ItemID: "i1", Attempt: 2}})
	if pb.GetType() != inventoryv1.CommandType_COMMAND_TYPE_TELEPORT_JOIN || pb.GetTeleportJoin().GetItemId() != "i1" ||
		pb.GetTeleportJoin().GetAttempt() != 2 || pb.GetModule() != nil {
		t.Fatalf("pb = %v", pb)
	}
}
