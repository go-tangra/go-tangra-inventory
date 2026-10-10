package moduledelivery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestCreatePushesToOnlineAgent (T005): an item references the host, its
// agent, the module and version; the MODULE command carries ids only and
// the item is delivered; audit rows hold no material.
func TestCreatePushesToOnlineAgent(t *testing.T) {
	f := newFix(t)
	h, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	if !v.Created || v.HostID != h.ID || v.AgentID != a.ID || v.Module != "sms-gw" || v.Version != "1.2.0" || v.Hostname != "gw-1" ||
		!v.AgentOnline || v.State != store.DeliveryDelivered || v.HookExitCode != -1 || v.Attempt != 1 || v.Source != "gateway" ||
		v.DeliveryID != did(1) || !v.ExpiresAt.Equal(t0.Add(24*time.Hour)) {
		t.Fatalf("view = %+v", v)
	}
	if len(f.reg.delivered) != 1 {
		t.Fatalf("pushed %d commands", len(f.reg.delivered))
	}
	cmd := f.reg.delivered[0]
	if cmd.Type != registry.CommandModule || cmd.Module == nil || *cmd.Module != (registry.ModulePayload{ItemID: v.ID, Module: "sms-gw", Attempt: 1}) ||
		cmd.Certificate != nil || cmd.Upgrade != nil {
		t.Fatalf("command = %+v", cmd)
	}
	if f.count("module_delivery_created") != 1 || f.count("module_delivery_delivered") != 1 {
		t.Fatalf("audit = %v", f.mem.AuditRows())
	}
	for _, r := range f.mem.AuditRows() {
		if r.Action == "module_delivery_created" && (r.Detail["requested_by"] != "spiffe://example.org/svc/gateway" || r.ActorID != "gateway") {
			t.Fatalf("created row = %+v", r)
		}
	}
	if f.met.transitions["pending:"] != 1 || f.met.transitions["delivered:"] != 1 {
		t.Fatalf("metrics = %v", f.met.transitions)
	}
}

// TestCreateIdempotentReplay (T005): the same delivery id answers the
// stored item (created false); another payload under the id is refused; an
// upper-case id is the same id.
func TestCreateIdempotentReplay(t *testing.T) {
	f := newFix(t)
	h, _ := f.modHost(t, "gw-1", false)
	v := f.create(t, did(1), "gw-1")
	if v.State != store.DeliveryPending || v.AgentOnline || len(f.reg.delivered) != 0 {
		t.Fatalf("offline create = %+v", v)
	}
	r := f.req(strings.ToUpper(did(1)), strings.ToUpper(h.ID))
	again, err := f.svc.Create(ctx, r)
	if err != nil || again.Created || again.ID != v.ID {
		t.Fatalf("replay = %+v %v", again, err)
	}
	for name, mut := range map[string]func(*Request){
		"module":  func(r *Request) { r.Module = "mail" },
		"version": func(r *Request) { r.Version = "1.3.0" },
	} {
		r := f.req(did(1), h.ID)
		mut(&r)
		var ie *InvalidError
		if _, err := f.svc.Create(ctx, r); !errors.As(err, &ie) || ie.Field != "delivery_id" {
			t.Errorf("%s under the same id: %v", name, err)
		}
	}
	h2, _ := f.modHost(t, "gw-2", false)
	var ie *InvalidError
	if _, err := f.svc.Create(ctx, f.req(did(1), h2.ID)); !errors.As(err, &ie) {
		t.Fatalf("other host under the same id: %v", err)
	}
	if f.count("module_delivery_created") != 1 {
		t.Fatal("replay wrote a second item")
	}
}

// TestCreateValidation (T005): every field is checked.
func TestCreateValidation(t *testing.T) {
	f := newFix(t)
	h, _ := f.modHost(t, "gw-1", false)
	cases := map[string]func(*Request){
		"tenant_id":            func(r *Request) { r.TenantID = "acme" },
		"source":               func(r *Request) { r.Source = "Gateway" },
		"delivery_id":          func(r *Request) { r.DeliveryID = "join-1" },
		"host_id":              func(r *Request) { r.HostID = "web" },
		"module":               func(r *Request) { r.Module = "../etc" },
		"module upper":         func(r *Request) { r.Module = "SMS" },
		"version":              func(r *Request) { r.Version = "v1.2.0" },
		"version long":         func(r *Request) { r.Version = "1.2.3-" + strings.Repeat("a", 60) },
		"expires_at past":      func(r *Request) { r.ExpiresAt = f.now },
		"expires_at too far":   func(r *Request) { r.ExpiresAt = f.now.Add(store.MaxModuleTTL + time.Second) },
		"version empty":        func(r *Request) { r.Version = "" },
		"module with dot":      func(r *Request) { r.Module = "a.b" },
		"module leading dash":  func(r *Request) { r.Module = "-a" },
		"module 64 characters": func(r *Request) { r.Module = strings.Repeat("a", 64) },
	}
	for name, mut := range cases {
		r := f.req(did(1), h.ID)
		mut(&r)
		var ie *InvalidError
		if _, err := f.svc.Create(ctx, r); !errors.As(err, &ie) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The gateway pattern admits digit-led names and pre-release versions.
	r := f.req(did(1), h.ID)
	r.Module, r.Version, r.ExpiresAt = "3cx", "2.0.0-rc.1+build.5", f.now.Add(store.MaxModuleTTL)
	if _, err := f.svc.Create(ctx, r); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if (&InvalidError{Field: "x"}).Error() != "invalid x" {
		t.Fatal("message")
	}
}

// TestCreateRefusesIneligibleHosts (T005): the reason token is the error
// message, nothing is stored and the refusal is audited.
func TestCreateRefusesIneligibleHosts(t *testing.T) {
	f := newFix(t)
	f.host(t, "bare")
	f.host(t, "twice")
	f.agentFor(t, "twice", "linux", "4.8.0", true, store.CapModuleV1)
	f.agentFor(t, "twice", "linux", "4.8.0", true, store.CapModuleV1)
	f.host(t, "win")
	f.agentFor(t, "win", "windows", "4.8.0", true)
	f.host(t, "old")
	f.agentFor(t, "old", "linux", "4.7.0", true, store.CapCertV1)
	f.host(t, "off")
	f.agentFor(t, "off", "linux", "4.8.0", true)
	f.modHost(t, "gone", true)
	if err := f.mem.RetireHost(ctx, tenant, f.hosts["gone"].ID); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"bare": "no_agent", "twice": "ambiguous_agent", "win": "not_supported_platform",
		"old": "upgrade_required", "off": "disabled_on_host", "gone": "retired"} {
		_, err := f.svc.Create(ctx, f.req(did(len(name)), f.hosts[name].ID))
		var ie *IneligibleError
		if !errors.As(err, &ie) || ie.Reason != want || err.Error() != want {
			t.Errorf("%s: %v, want %s", name, err, want)
		}
	}
	if n := f.count("module_delivery_created"); n != 0 {
		t.Fatalf("stored %d items for ineligible hosts", n)
	}
	if f.count("module_delivery_refused") != 6 || f.met.refused["no_agent"] != 1 {
		t.Fatalf("refusals = %v", f.met.refused)
	}
	// An unknown host is NotFound.
	if _, err := f.svc.Create(ctx, f.req(did(9), store.NewID())); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("unknown host: %v", err)
	}
}

// TestCreateSupersedes (T005): a new delivery for the same host and module
// supersedes the older active item; another module is untouched.
func TestCreateSupersedes(t *testing.T) {
	f := newFix(t)
	f.modHost(t, "gw-1", true)
	old := f.create(t, did(1), "gw-1")
	r := f.req(did(2), f.hosts["gw-1"].ID)
	r.Module = "mail"
	mail, err := f.svc.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	next := f.create(t, did(3), "gw-1")
	if f.item(t, old.ID).State != store.DeliverySuperseded || f.item(t, next.ID).State != store.DeliveryDelivered ||
		f.item(t, mail.ID).State != store.DeliveryDelivered {
		t.Fatal("supersede")
	}
	if f.count("module_delivery_superseded") != 1 || f.met.transitions["superseded:"] != 1 {
		t.Fatalf("superseded audit/metrics: %v", f.met.transitions)
	}
}

// conflictRepo answers the first CreateModuleItem with ErrConflict.
type conflictRepo struct {
	*memstore.Mem
	conflicts int
	before    func() // runs before the conflict is reported
}

func (c *conflictRepo) CreateModuleItem(ctx2 context.Context, n repo.NewModuleItem) ([]store.ModuleDeliveryItem, error) {
	if c.conflicts > 0 {
		c.conflicts--
		if c.before != nil {
			c.before()
		}
		return nil, repo.ErrConflict
	}
	return c.Mem.CreateModuleItem(ctx2, n)
}

// TestCreateConflicts: a concurrent create of the same id is replayed; a
// concurrent active item is superseded on the second try; a second conflict
// is returned.
func TestCreateConflicts(t *testing.T) {
	f := newFix(t)
	h, _ := f.modHost(t, "gw-1", false)
	cr := &conflictRepo{Mem: f.mem, conflicts: 1}
	f.svc.repo = cr
	// Retried: the second attempt stores it.
	v, err := f.svc.Create(ctx, f.req(did(1), h.ID))
	if err != nil || !v.Created {
		t.Fatalf("retry: %+v %v", v, err)
	}
	// The same id appeared meanwhile: replayed.
	cr.conflicts = 1
	cr.before = func() {
		r := f.req(did(2), h.ID)
		_, _ = f.mem.CreateModuleItem(ctx, f.svc.build(r, Actor{Kind: "service", ID: "gateway"}, f.agent["gw-1"].ID))
	}
	v, err = f.svc.Create(ctx, f.req(did(2), h.ID))
	if err != nil || v.Created || v.DeliveryID != did(2) {
		t.Fatalf("concurrent same id: %+v %v", v, err)
	}
	cr.conflicts, cr.before = 2, nil
	if _, err := f.svc.Create(ctx, f.req(did(3), h.ID)); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("second conflict: %v", err)
	}
}

// TestCreateStoreErrors: storage failures surface unchanged.
func TestCreateStoreErrors(t *testing.T) {
	for _, method := range []string{"GetModuleItemByDelivery", "GetHost", "ListAgents", "CreateModuleItem", "GetModuleItem"} {
		f := newFix(t)
		h, _ := f.modHost(t, "gw-1", false)
		f.mem.FailNext(method)
		if _, err := f.svc.Create(ctx, f.req(did(1), h.ID)); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
	// A failed push leaves the item pending.
	f := newFix(t)
	f.modHost(t, "gw-1", true)
	f.reg.failPush = errBoom
	if v := f.create(t, did(1), "gw-1"); v.State != store.DeliveryPending {
		t.Fatalf("failed push: %+v", v)
	}
}

func TestCreateDisabled(t *testing.T) {
	f := newFix(t)
	f.svc.cfg.Enabled = false
	if f.svc.Enabled() {
		t.Fatal("enabled")
	}
	if _, err := f.svc.Create(ctx, Request{}); !errors.Is(err, ErrDisabled) || err.Error() != "disabled_on_server" {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.Get(ctx, tenant, "gateway", did(1)); !errors.Is(err, ErrDisabled) {
		t.Fatalf("get: %v", err)
	}
	if _, _, err := f.svc.ListTargets(ctx, tenant, "", 0); !errors.Is(err, ErrDisabled) {
		t.Fatalf("targets: %v", err)
	}
	if _, err := f.svc.Fetch(ctx, store.Agent{}, did(1)); !errors.Is(err, ErrDisabled) {
		t.Fatalf("fetch: %v", err)
	}
	if _, err := f.svc.Report(ctx, store.Agent{}, Report{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("report: %v", err)
	}
	if cmds, err := f.svc.OnConnect(ctx, store.Agent{}); cmds != nil || err != nil {
		t.Fatalf("on connect: %v %v", cmds, err)
	}
	if e, fl, err := f.svc.Sweep(ctx); e+fl != 0 || err != nil {
		t.Fatalf("sweep: %v", err)
	}
}

// TestGet: by the source's delivery id only; the view carries the hostname
// and the agent's presence.
func TestGet(t *testing.T) {
	f := newFix(t)
	f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	got, err := f.svc.Get(ctx, strings.ToUpper(tenant), "gateway", strings.ToUpper(did(1)))
	if err != nil || got.ID != v.ID || got.Hostname != "gw-1" || !got.AgentOnline || got.Created {
		t.Fatalf("get = %+v %v", got, err)
	}
	for name, err := range map[string]error{
		"other source": func() error { _, e := f.svc.Get(ctx, tenant, "deployer", did(1)); return e }(),
		"other tenant": func() error { _, e := f.svc.Get(ctx, other, "gateway", did(1)); return e }(),
		"malformed":    func() error { _, e := f.svc.Get(ctx, tenant, "gateway", "x"); return e }(),
	} {
		if !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Presence unknown (registry error) and a deleted host: still answered.
	f.reg.failOn = errBoom
	if err := f.mem.DeleteHost(ctx, tenant, f.hosts["gw-1"].ID); err != nil {
		t.Fatal(err)
	}
	got, err = f.svc.Get(ctx, tenant, "gateway", did(1))
	if err != nil || got.AgentOnline || got.Hostname != "" {
		t.Fatalf("degraded get = %+v %v", got, err)
	}
}

// TestRefusalThrottle: one audited row per actor and reason per window,
// the next one carrying the suppressed count; old entries are dropped.
func TestRefusalThrottle(t *testing.T) {
	f := newFix(t)
	actor := Actor{Kind: "service", ID: "deployer"}
	f.svc.Refuse(ctx, tenant, actor, RefusedSourceNotAllowed)
	f.svc.Refuse(ctx, tenant, actor, RefusedSourceNotAllowed)
	f.svc.Refuse(ctx, tenant, Actor{Kind: "service", ID: "x"}, RefusedSourceNotAllowed)
	f.now = f.now.Add(refusalWindow)
	f.svc.Refuse(ctx, tenant, actor, RefusedSourceNotAllowed)
	f.now = f.now.Add(7 * refusalWindow)
	f.svc.Refuse(ctx, tenant, actor, RefusedNotFound)
	rows := f.mem.AuditRows()
	if len(rows) != 4 || rows[2].Detail["suppressed"] != 1 || rows[0].SubjectID != "" {
		t.Fatalf("rows = %+v", rows)
	}
	if f.met.refused[RefusedSourceNotAllowed] != 4 || len(f.svc.refused) != 1 {
		t.Fatalf("metrics %v / map %d", f.met.refused, len(f.svc.refused))
	}
}

func TestNilMetrics(t *testing.T) {
	var o observer
	o.transition("a", "b")
	o.refused("a")
	o.source(time.Second, "ok")
}
