package moduledelivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const (
	tenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	other  = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
)

var (
	t0      = time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	ctx     = context.Background()
	errBoom = errors.New("boom")
)

// fakeStream replays scripted messages, then io.EOF (or err).
type fakeStream struct {
	grpc.ClientStream
	msgs []*invv1.ModuleBundleChunk
	err  error
}

func (f *fakeStream) Recv() (*invv1.ModuleBundleChunk, error) {
	if len(f.msgs) == 0 {
		if f.err != nil {
			return nil, f.err
		}
		return nil, io.EOF
	}
	m := f.msgs[0]
	f.msgs = f.msgs[1:]
	return m, nil
}

// fakeSource renders bundle (split into chunks of chunk bytes) or fails.
type fakeSource struct {
	mu       sync.Mutex
	bundle   []byte
	module   string
	chunk    int
	callErr  error // RenderModuleBundle fails
	mutate   func([]*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk
	streamEr error // after the messages
	reqs     []*invv1.RenderModuleBundleRequest
	deadline bool   // the call carried a deadline
	during   func() // runs inside RenderModuleBundle
}

func header(module string, data []byte) *invv1.ModuleBundleChunk {
	sum := sha256.Sum256(data)
	return &invv1.ModuleBundleChunk{Part: &invv1.ModuleBundleChunk_Header{Header: &invv1.ModuleBundleHeader{Module: module,
		Version: "1.2.0", Size: int64(len(data)), Sha256: hex.EncodeToString(sum[:]), DeliveryId: "ignored"}}}
}

func chunk(off int, data []byte) *invv1.ModuleBundleChunk {
	return &invv1.ModuleBundleChunk{Part: &invv1.ModuleBundleChunk_Chunk{Chunk: &invv1.ArtifactChunk{Offset: int64(off), Data: data}}}
}

func (f *fakeSource) messages() []*invv1.ModuleBundleChunk {
	msgs := []*invv1.ModuleBundleChunk{header(f.module, f.bundle)}
	n := f.chunk
	if n == 0 {
		n = 4
	}
	for off := 0; off < len(f.bundle); off += n {
		end := min(off+n, len(f.bundle))
		msgs = append(msgs, chunk(off, append([]byte(nil), f.bundle[off:end]...)))
	}
	if f.mutate != nil {
		msgs = f.mutate(msgs)
	}
	return msgs
}

func (f *fakeSource) RenderModuleBundle(ctx context.Context, in *invv1.RenderModuleBundleRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[invv1.ModuleBundleChunk], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, in)
	_, f.deadline = ctx.Deadline()
	if f.during != nil {
		f.during()
	}
	if f.callErr != nil {
		return nil, f.callErr
	}
	return &fakeStream{msgs: f.messages(), err: f.streamEr}, nil
}

// fakeRegistry records pushes; online agents receive them.
type fakeRegistry struct {
	mu        sync.Mutex
	online    map[string]bool
	delivered []registry.Command
	failPush  error
	failList  error
	failOn    error
}

func (f *fakeRegistry) Deliver(_ context.Context, agentID string, cmd registry.Command) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPush != nil {
		return false, f.failPush
	}
	if !f.online[agentID] {
		return false, nil
	}
	f.delivered = append(f.delivered, cmd)
	return true, nil
}

func (f *fakeRegistry) ListConnected(_ context.Context, tenantID string) ([]registry.ConnectedAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failList != nil {
		return nil, f.failList
	}
	var out []registry.ConnectedAgent
	for id, on := range f.online {
		if on {
			out = append(out, registry.ConnectedAgent{AgentID: id, TenantID: tenantID})
		}
	}
	return out, nil
}

func (f *fakeRegistry) IsOnline(_ context.Context, agentID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn != nil {
		return false, f.failOn
	}
	return f.online[agentID], nil
}

// countMetrics counts observations.
type countMetrics struct {
	mu          sync.Mutex
	transitions map[string]int
	refused     map[string]int
	source      map[string]int
}

func (c *countMetrics) Transition(state, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transitions[state+":"+reason]++
}

func (c *countMetrics) Refused(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refused[reason]++
}

func (c *countMetrics) Source(_ time.Duration, outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.source[outcome]++
}

type fix struct {
	mem   *memstore.Mem
	src   *fakeSource
	reg   *fakeRegistry
	met   *countMetrics
	svc   *Service
	now   time.Time
	hosts map[string]store.Host
	agent map[string]store.Agent
}

func newFix(t *testing.T) *fix {
	t.Helper()
	f := &fix{mem: memstore.New(), src: &fakeSource{bundle: []byte("PK-zip-bytes-with-secrets"), module: "sms-gw"},
		reg: &fakeRegistry{online: map[string]bool{}},
		met: &countMetrics{transitions: map[string]int{}, refused: map[string]int{}, source: map[string]int{}},
		now: t0, hosts: map[string]store.Host{}, agent: map[string]store.Agent{}}
	f.mem.Now = func() time.Time { return f.now }
	ids := 0
	f.svc = New(f.mem, f.src, f.reg, Config{Enabled: true, ReportTimeout: 30 * time.Minute, SourceTimeout: 30 * time.Second, InstanceID: "inv-1"})
	f.svc.now = func() time.Time { return f.now }
	f.svc.newID = func() string { ids++; return fmt.Sprintf("00000000-0000-7000-8000-%012d", ids) }
	f.svc.SetMetrics(f.met)
	return f
}

// host adds an active host in tenant.
func (f *fix) host(t *testing.T, name string) store.Host {
	t.Helper()
	h, err := f.mem.ResolveHost(ctx, tenant, store.Host{Hostname: name, MachineID: "mid-" + name, Status: store.HostActive, OSName: "Debian"})
	if err != nil {
		t.Fatal(err)
	}
	f.hosts[name] = h
	return h
}

// agentFor enrolls an agent for host name with os, version and capabilities.
func (f *fix) agentFor(t *testing.T, name, os, version string, online bool, caps ...string) store.Agent {
	t.Helper()
	h := f.hosts[name]
	a := store.Agent{ID: store.NewID(), TenantID: tenant, HostID: h.ID, AgentVersion: version, EnrolledAt: t0, LastSeen: t0}
	if err := f.mem.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := f.mem.SetAgentPlatform(ctx, a.ID, os, "amd64", "deb", caps, t0); err != nil {
		t.Fatal(err)
	}
	a, _ = f.mem.GetAgent(ctx, tenant, a.ID)
	f.agent[name] = a
	f.reg.online[a.ID] = online
	return a
}

// modHost adds a host with a module.v1 agent.
func (f *fix) modHost(t *testing.T, name string, online bool) (store.Host, store.Agent) {
	t.Helper()
	h := f.host(t, name)
	return h, f.agentFor(t, name, "linux", "4.8.0", online, store.CapUpgradeV1, store.CapModuleV1)
}

func (f *fix) req(deliveryID, hostID string) Request {
	return Request{TenantID: tenant, Source: "gateway", RequestedBy: "spiffe://example.org/svc/gateway", DeliveryID: deliveryID,
		HostID: hostID, Module: "sms-gw", Version: "1.2.0", ExpiresAt: f.now.Add(24 * time.Hour)}
}

func (f *fix) item(t *testing.T, id string) store.ModuleDeliveryItem {
	t.Helper()
	i, err := f.mem.GetModuleItem(ctx, tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func (f *fix) count(action string) int {
	n := 0
	for _, r := range f.mem.AuditRows() {
		if r.Action == action {
			n++
		}
	}
	return n
}

// create creates a delivery to host name and returns the item.
func (f *fix) create(t *testing.T, deliveryID, name string) View {
	t.Helper()
	v, err := f.svc.Create(ctx, f.req(deliveryID, f.hosts[name].ID))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func did(n int) string { return fmt.Sprintf("0190f7c2-6a3e-7c1a-9b2e-%012d", n) }

func notFound(msg string) error { return status.Error(codes.NotFound, msg) }
