package teleportjoin

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

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const (
	tenant  = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	other   = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
	cluster = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c88"
	nodeID  = "5b0c7a4e-2f61-4c1e-9d3a-0e4f6a7b8c9d"
	secret  = "s3cr3t-join-token-value"
)

var (
	t0      = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	ctx     = context.Background()
	errBoom = errors.New("boom")
)

// fakeSource renders material or fails.
type fakeSource struct {
	mu       sync.Mutex
	callErr  error // RenderTeleportJoin fails
	mutate   func(*invv1.TeleportJoinMaterial)
	reqs     []*invv1.RenderTeleportJoinRequest
	deadline bool   // the call carried a deadline
	during   func() // runs inside RenderTeleportJoin
}

func material() *invv1.TeleportJoinMaterial {
	return &invv1.TeleportJoinMaterial{Token: secret, ProxyAddr: "teleport.example.com:443",
		CaPins:          []string{"sha256:" + fmt.Sprintf("%064x", 1)},
		TeleportVersion: "17.4.2", Labels: map[string]string{"tangra.io/os": "debian"}, TokenExpiresAt: t0.Add(time.Hour).Unix()}
}

func (f *fakeSource) RenderTeleportJoin(ctx context.Context, in *invv1.RenderTeleportJoinRequest, _ ...grpc.CallOption) (*invv1.TeleportJoinMaterial, error) {
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
	m := material()
	m.EnrollmentId = in.GetEnrollmentId()
	if f.mutate != nil {
		f.mutate(m)
	}
	return m, nil
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
	f := &fix{mem: memstore.New(), src: &fakeSource{}, reg: &fakeRegistry{online: map[string]bool{}},
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
	h, err := f.mem.ResolveHost(ctx, tenant, store.Host{Hostname: name, MachineID: "mid-" + name, Status: store.HostActive, OSName: "Debian",
		OSVersion: "12", OSArch: "amd64", Manufacturer: "Dell Inc.", Model: "R650", Tags: map[string]string{"env": "prod"}})
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

// tpHost adds a host with a teleport.v1 agent.
func (f *fix) tpHost(t *testing.T, name string, online bool) (store.Host, store.Agent) {
	t.Helper()
	h := f.host(t, name)
	return h, f.agentFor(t, name, "linux", "4.9.0", online, store.CapUpgradeV1, store.CapTeleportV1)
}

func (f *fix) req(enrollmentID, hostID string) Request {
	return Request{TenantID: tenant, Source: "teleport", RequestedBy: "spiffe://example.org/svc/teleport", EnrollmentID: enrollmentID,
		HostID: hostID, ClusterID: cluster, ExpiresAt: f.now.Add(24 * time.Hour)}
}

func (f *fix) item(t *testing.T, id string) store.TeleportJoinItem {
	t.Helper()
	i, err := f.mem.GetTeleportItem(ctx, tenant, id)
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

// create creates an enrollment of host name and returns the item.
func (f *fix) create(t *testing.T, enrollmentID, name string) View {
	t.Helper()
	v, err := f.svc.Create(ctx, f.req(enrollmentID, f.hosts[name].ID))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// fetched creates an item for host name and fetches it.
func (f *fix) fetched(t *testing.T, n int, name string) (View, store.Agent) {
	t.Helper()
	v := f.create(t, eid(n), name)
	a := f.agent[name]
	if _, err := f.svc.Fetch(ctx, a, v.ID); err != nil {
		t.Fatal(err)
	}
	return v, a
}

func eid(n int) string { return fmt.Sprintf("0190f7c2-6a3e-7c1a-9b2e-%012d", n) }

func notFound(msg string) error { return status.Error(codes.NotFound, msg) }

// noSecret fails when any audit row carries the join token.
func (f *fix) noSecret(t *testing.T) {
	t.Helper()
	for _, r := range f.mem.AuditRows() {
		if s := fmt.Sprint(r.Detail); containsSecret(s) || containsSecret(r.Reason) {
			t.Fatalf("audit row %s carries the token: %v", r.Action, r.Detail)
		}
	}
}

func containsSecret(s string) bool {
	for i := 0; i+len(secret) <= len(s); i++ {
		if s[i:i+len(secret)] == secret {
			return true
		}
	}
	return false
}

// raceRepo runs onList before ListAgents (a change racing the fetch).
type raceRepo struct {
	*memstore.Mem
	onList func()
}

func (r *raceRepo) ListAgents(ctx2 context.Context, tenantID string) ([]store.Agent, error) {
	if r.onList != nil {
		r.onList()
	}
	return r.Mem.ListAgents(ctx2, tenantID)
}

// staleRepo answers ListStaleTeleportItems with a fixed list.
type staleRepo struct {
	*memstore.Mem
	stale []store.TeleportJoinItem
}

func (r *staleRepo) ListStaleTeleportItems(context.Context, time.Time, time.Time, int) ([]store.TeleportJoinItem, error) {
	return r.stale, nil
}
