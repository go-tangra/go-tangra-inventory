package certdelivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const (
	tenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	other  = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
	cert1  = "cert-1"
)

// t0 is inside the validity window of the certmaterial test fixtures.
var t0 = time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

// fixture reads a shared certmaterial test vector.
func fixture(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "certmaterial", "testdata", name)) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeLCM serves bundles per certificate id.
type fakeLCM struct {
	mu      sync.Mutex
	certs   map[string]lcmclient.Bundle
	errs    map[string]error
	calls   []string // certID|includeKey
	served  [][]byte // key slices handed out (to check they were wiped)
	noKeyOK bool     // serve without a key even when asked for one
}

func (f *fakeLCM) Download(_ context.Context, tenantID, certID string, includeKey bool) (lcmclient.Bundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%s|%s|%v", tenantID, certID, includeKey))
	b, ok := f.certs[certID]
	if err := f.errs[certID]; err != nil {
		return b, err
	}
	if !ok || tenantID != tenant {
		return lcmclient.Bundle{}, lcmclient.ErrNotFound
	}
	if !includeKey || f.noKeyOK {
		b.KeyPEM = nil
	} else {
		b.KeyPEM = append([]byte(nil), b.KeyPEM...)
		f.served = append(f.served, b.KeyPEM)
	}
	return b, nil
}

// bundle builds an lcm bundle from the fixtures (leaf, key).
func bundle(t testing.TB, leaf, key string, notAfter time.Time) lcmclient.Bundle {
	return lcmclient.Bundle{CertPEM: fixture(t, leaf), ChainPEM: fixture(t, "chain.pem"), KeyPEM: []byte(fixture(t, key)),
		NotAfter: notAfter, Status: lcmclient.StatusActive}
}

// fakeRegistry records pushes; online agents receive them.
type fakeRegistry struct {
	mu        sync.Mutex
	online    map[string]bool
	delivered []registry.Command
	to        []string
	failPush  error
	failList  error
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
	f.to = append(f.to, agentID)
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

// fakePub records realtime events.
type fakePub struct {
	mu       sync.Mutex
	payloads []map[string]any
}

func (f *fakePub) Publish(_ context.Context, _, typ string, payload any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := payload.(map[string]any)
	p["type"] = typ
	f.payloads = append(f.payloads, p)
}

// countMetrics counts observations.
type countMetrics struct {
	mu          sync.Mutex
	transitions map[string]int
	refused     map[string]int
	lcm         map[string]int
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
func (c *countMetrics) LCM(_ time.Duration, outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lcm[outcome]++
}

// faultRepo wraps the memstore to inject errors memstore cannot produce.
type faultRepo struct {
	*memstore.Mem
	createErrs []error // returned by the next CreateCertDelivery calls
	getByKey   []error // returned by the next GetCertDeliveryByKey calls (nil = real call)
	extendErr  error
	updateErr  map[int]error // nth UpdateCertItem call (1-based) fails
	updates    int
}

func (f *faultRepo) CreateCertDelivery(ctx context.Context, n repo.NewCertDelivery) ([]store.CertDeliveryItem, error) {
	if len(f.createErrs) > 0 {
		err := f.createErrs[0]
		f.createErrs = f.createErrs[1:]
		return nil, err
	}
	return f.Mem.CreateCertDelivery(ctx, n)
}

func (f *faultRepo) GetCertDeliveryByKey(ctx context.Context, tenantID, source, key string) (store.CertDelivery, error) {
	if len(f.getByKey) > 0 {
		err := f.getByKey[0]
		f.getByKey = f.getByKey[1:]
		if err != nil {
			return store.CertDelivery{}, err
		}
	}
	return f.Mem.GetCertDeliveryByKey(ctx, tenantID, source, key)
}

func (f *faultRepo) ExtendCertDelivery(ctx context.Context, tenantID, id string, at time.Time) error {
	if f.extendErr != nil {
		return f.extendErr
	}
	return f.Mem.ExtendCertDelivery(ctx, tenantID, id, at)
}

func (f *faultRepo) UpdateCertItem(ctx context.Context, tenantID, id string, fn func(*store.CertDeliveryItem) (repo.CertItemChange, error)) (store.CertDeliveryItem, error) {
	f.updates++
	if err := f.updateErr[f.updates]; err != nil {
		return store.CertDeliveryItem{}, err
	}
	return f.Mem.UpdateCertItem(ctx, tenantID, id, fn)
}

var errBoom = errors.New("boom")

type fix struct {
	mem   *memstore.Mem
	repo  *faultRepo
	lcm   *fakeLCM
	reg   *fakeRegistry
	pub   *fakePub
	met   *countMetrics
	svc   *Service
	now   time.Time
	hosts map[string]store.Host  // by hostname
	agent map[string]store.Agent // by hostname
}

func newFix(t *testing.T) *fix {
	t.Helper()
	f := &fix{mem: memstore.New(), lcm: &fakeLCM{certs: map[string]lcmclient.Bundle{}, errs: map[string]error{}},
		reg: &fakeRegistry{online: map[string]bool{}}, pub: &fakePub{},
		met: &countMetrics{transitions: map[string]int{}, refused: map[string]int{}, lcm: map[string]int{}},
		now: t0, hosts: map[string]store.Host{}, agent: map[string]store.Agent{}}
	f.mem.Now = func() time.Time { return f.now }
	f.repo = &faultRepo{Mem: f.mem, updateErr: map[int]error{}}
	f.lcm.certs[cert1] = bundle(t, "rsa.crt", "rsa.pkcs8.key", time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC))
	ids := 0
	f.svc = New(f.repo, f.lcm, f.reg, f.pub, Config{Enabled: true, PendingTTL: 168 * time.Hour, ReportTimeout: 15 * time.Minute, InstanceID: "inv-1"})
	f.svc.now = func() time.Time { return f.now }
	f.svc.newID = func() string { ids++; return fmt.Sprintf("00000000-0000-7000-8000-%012d", ids) }
	f.svc.SetMetrics(f.met)
	return f
}

// host adds a host with tags in tenant.
func (f *fix) host(t *testing.T, name string, tags map[string]string) store.Host {
	t.Helper()
	h, err := f.mem.ResolveHost(context.Background(), tenant, store.Host{Hostname: name, MachineID: "mid-" + name, Status: store.HostActive})
	if err != nil {
		t.Fatal(err)
	}
	if tags != nil {
		if err := f.mem.SetHostTags(context.Background(), tenant, h.ID, tags); err != nil {
			t.Fatal(err)
		}
	}
	h, _ = f.mem.GetHost(context.Background(), tenant, h.ID)
	f.hosts[name] = h
	return h
}

// agentFor enrolls an agent for host name with os, version and capabilities.
func (f *fix) agentFor(t *testing.T, name, os, version string, online bool, caps ...string) store.Agent {
	t.Helper()
	h := f.hosts[name]
	a := store.Agent{ID: store.NewID(), TenantID: tenant, HostID: h.ID, AgentVersion: version, EnrolledAt: t0, LastSeen: t0}
	if err := f.mem.CreateAgent(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := f.mem.SetAgentPlatform(context.Background(), a.ID, os, "amd64", "deb", caps, t0); err != nil {
		t.Fatal(err)
	}
	a, _ = f.mem.GetAgent(context.Background(), tenant, a.ID)
	f.agent[name] = a
	f.reg.online[a.ID] = online
	return a
}

// webHost adds a host with a cert.v1 agent.
func (f *fix) webHost(t *testing.T, name string, online bool) (store.Host, store.Agent) {
	t.Helper()
	h := f.host(t, name, map[string]string{"role": "web"})
	return h, f.agentFor(t, name, "linux", "4.7.0", online, store.CapUpgradeV1, store.CapCertV1)
}

func (f *fix) req(key string, ids ...string) Request {
	return Request{TenantID: tenant, Source: "deployer", RequestedBy: "spiffe://example.org/svc/deployer", IdempotencyKey: key,
		ConfigurationID: "cfg-1", Trigger: store.TriggerManual, CertificateID: cert1, Name: "www", KeyPolicy: store.KeyPolicyRequire,
		HostIDs: ids}
}

func (f *fix) item(t *testing.T, id string) store.CertDeliveryItem {
	t.Helper()
	i, err := f.mem.GetCertItem(context.Background(), tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// actions lists the audit actions written so far.
func (f *fix) actions() []string {
	var out []string
	for _, r := range f.mem.AuditRows() {
		out = append(out, r.Action)
	}
	return out
}

func (f *fix) count(action string) int {
	n := 0
	for _, a := range f.actions() {
		if a == action {
			n++
		}
	}
	return n
}

// itemFor returns the view item of host name.
func itemFor(v DeliveryView, hostID string) ItemView {
	for _, i := range v.Items {
		if i.HostID == hostID {
			return i
		}
	}
	return ItemView{}
}

var ctx = context.Background()

func listReq() listquery.Request { return listquery.Request{} }

func repoFilter() repo.CertItemFilter { return repo.CertItemFilter{} }
