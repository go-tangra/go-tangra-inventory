package teleportjoin

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestCreatePushesToOnlineAgent: an item references the host, its agent
// and the cluster; the TELEPORT_JOIN command carries ids only and the item
// is delivered; audit rows hold no material.
func TestCreatePushesToOnlineAgent(t *testing.T) {
	f := newFix(t)
	h, a := f.tpHost(t, "node-1", true)
	v := f.create(t, eid(1), "node-1")
	if !v.Created || v.HostID != h.ID || v.AgentID != a.ID || v.ClusterID != cluster || v.Hostname != "node-1" ||
		!v.AgentOnline || v.State != store.DeliveryDelivered || v.Attempt != 1 || v.Source != "teleport" ||
		v.EnrollmentID != eid(1) || !v.ExpiresAt.Equal(t0.Add(24*time.Hour)) || v.NodeHostUUID != "" {
		t.Fatalf("view = %+v", v)
	}
	if len(f.reg.delivered) != 1 {
		t.Fatalf("pushed %d commands", len(f.reg.delivered))
	}
	cmd := f.reg.delivered[0]
	if cmd.Type != registry.CommandTeleportJoin || cmd.TeleportJoin == nil ||
		*cmd.TeleportJoin != (registry.TeleportJoinPayload{ItemID: v.ID, Attempt: 1}) || cmd.Module != nil || cmd.Certificate != nil {
		t.Fatalf("command = %+v", cmd)
	}
	if f.count("teleport_join_created") != 1 || f.count("teleport_join_delivered") != 1 {
		t.Fatalf("audit = %v", f.mem.AuditRows())
	}
	for _, r := range f.mem.AuditRows() {
		if r.Action == "teleport_join_created" && (r.Detail["requested_by"] != "spiffe://example.org/svc/teleport" || r.ActorID != "teleport" ||
			r.Detail["cluster_id"] != cluster) {
			t.Fatalf("created row = %+v", r)
		}
	}
	if f.met.transitions["pending:"] != 1 || f.met.transitions["delivered:"] != 1 {
		t.Fatalf("metrics = %v", f.met.transitions)
	}
}

// TestCreateIdempotentReplay: the same enrollment id answers the stored
// item (created false); another host or cluster under the id is refused;
// an upper-case id is the same id.
func TestCreateIdempotentReplay(t *testing.T) {
	f := newFix(t)
	h, _ := f.tpHost(t, "node-1", false)
	v := f.create(t, eid(1), "node-1")
	if v.State != store.DeliveryPending || v.AgentOnline || len(f.reg.delivered) != 0 {
		t.Fatalf("offline create = %+v", v)
	}
	r := f.req(strings.ToUpper(eid(1)), strings.ToUpper(h.ID))
	r.ClusterID = strings.ToUpper(cluster)
	again, err := f.svc.Create(ctx, r)
	if err != nil || again.Created || again.ID != v.ID {
		t.Fatalf("replay = %+v %v", again, err)
	}
	r = f.req(eid(1), h.ID)
	r.ClusterID = other
	var ie *InvalidError
	if _, err := f.svc.Create(ctx, r); !errors.As(err, &ie) || ie.Field != "enrollment_id" {
		t.Fatalf("other cluster under the same id: %v", err)
	}
	h2, _ := f.tpHost(t, "node-2", false)
	if _, err := f.svc.Create(ctx, f.req(eid(1), h2.ID)); !errors.As(err, &ie) {
		t.Fatalf("other host under the same id: %v", err)
	}
	if f.count("teleport_join_created") != 1 {
		t.Fatal("replay wrote a second item")
	}
}

// TestCreateValidation: every field is checked.
func TestCreateValidation(t *testing.T) {
	f := newFix(t)
	h, _ := f.tpHost(t, "node-1", false)
	cases := map[string]func(*Request){
		"tenant_id":          func(r *Request) { r.TenantID = "acme" },
		"source":             func(r *Request) { r.Source = "Teleport" },
		"enrollment_id":      func(r *Request) { r.EnrollmentID = "enr-1" },
		"host_id":            func(r *Request) { r.HostID = "web" },
		"cluster_id":         func(r *Request) { r.ClusterID = "prod" },
		"expires_at past":    func(r *Request) { r.ExpiresAt = f.now },
		"expires_at too far": func(r *Request) { r.ExpiresAt = f.now.Add(store.MaxTeleportTTL + time.Second) },
	}
	for name, mut := range cases {
		r := f.req(eid(1), h.ID)
		mut(&r)
		var ie *InvalidError
		if _, err := f.svc.Create(ctx, r); !errors.As(err, &ie) {
			t.Errorf("%s: %v", name, err)
		}
	}
	r := f.req(eid(1), h.ID)
	r.ExpiresAt = f.now.Add(store.MaxTeleportTTL)
	if _, err := f.svc.Create(ctx, r); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if (&InvalidError{Field: "x"}).Error() != "invalid x" {
		t.Fatal("message")
	}
}

// TestCreateRefusesIneligibleHosts: the reason token is the error message,
// nothing is stored and the refusal is audited. Windows and old agents are
// refused; a Linux agent that announces teleport.v1 is eligible even when
// it disabled Teleport locally (it reports disabled_locally).
func TestCreateRefusesIneligibleHosts(t *testing.T) {
	f := newFix(t)
	f.host(t, "bare")
	f.host(t, "twice")
	f.agentFor(t, "twice", "linux", "4.9.0", true, store.CapTeleportV1)
	f.agentFor(t, "twice", "linux", "4.9.0", true, store.CapTeleportV1)
	f.host(t, "win")
	f.agentFor(t, "win", "windows", "4.9.0", true, store.CapTeleportV1)
	f.host(t, "old")
	f.agentFor(t, "old", "linux", "4.8.0", true, store.CapModuleV1)
	f.tpHost(t, "gone", true)
	if err := f.mem.RetireHost(ctx, tenant, f.hosts["gone"].ID); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"bare": "no_agent", "twice": "ambiguous_agent", "win": "not_supported_platform",
		"old": "upgrade_required", "gone": "retired"} {
		_, err := f.svc.Create(ctx, f.req(eid(len(name)), f.hosts[name].ID))
		var ie *IneligibleError
		if !errors.As(err, &ie) || ie.Reason != want || err.Error() != want {
			t.Errorf("%s: %v, want %s", name, err, want)
		}
	}
	if n := f.count("teleport_join_created"); n != 0 {
		t.Fatalf("stored %d items for ineligible hosts", n)
	}
	if f.count("teleport_join_refused") != 5 || f.met.refused["no_agent"] != 1 {
		t.Fatalf("refusals = %v", f.met.refused)
	}
	// An unknown host is NotFound.
	if _, err := f.svc.Create(ctx, f.req(eid(9), store.NewID())); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("unknown host: %v", err)
	}
}

// TestCreateSupersedes: a newer enrollment of the same host and cluster
// supersedes the older active item (even one installing); another cluster
// is untouched.
func TestCreateSupersedes(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	old := f.create(t, eid(1), "node-1")
	r := f.req(eid(2), f.hosts["node-1"].ID)
	r.ClusterID = other
	second, err := f.svc.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	next := f.create(t, eid(3), "node-1")
	if f.item(t, old.ID).State != store.DeliverySuperseded || f.item(t, next.ID).State != store.DeliveryDelivered ||
		f.item(t, second.ID).State != store.DeliveryDelivered {
		t.Fatal("supersede")
	}
	if f.count("teleport_join_superseded") != 1 || f.met.transitions["superseded:"] != 1 {
		t.Fatalf("superseded audit/metrics: %v", f.met.transitions)
	}
}

// conflictRepo answers the first CreateTeleportItem with ErrConflict.
type conflictRepo struct {
	*memstore.Mem
	conflicts int
	before    func() // runs before the conflict is reported
}

func (c *conflictRepo) CreateTeleportItem(ctx2 context.Context, n repo.NewTeleportItem) ([]store.TeleportJoinItem, error) {
	if c.conflicts > 0 {
		c.conflicts--
		if c.before != nil {
			c.before()
		}
		return nil, repo.ErrConflict
	}
	return c.Mem.CreateTeleportItem(ctx2, n)
}

// TestCreateConflicts: a concurrent create of the same id is replayed; a
// concurrent active item is superseded on the second try; a second conflict
// is returned.
func TestCreateConflicts(t *testing.T) {
	f := newFix(t)
	h, _ := f.tpHost(t, "node-1", false)
	cr := &conflictRepo{Mem: f.mem, conflicts: 1}
	f.svc.repo = cr
	v, err := f.svc.Create(ctx, f.req(eid(1), h.ID))
	if err != nil || !v.Created {
		t.Fatalf("retry: %+v %v", v, err)
	}
	cr.conflicts = 1
	cr.before = func() {
		r := f.req(eid(2), h.ID)
		_, _ = f.mem.CreateTeleportItem(ctx, f.svc.build(r, Actor{Kind: "service", ID: "teleport"}, f.agent["node-1"].ID))
	}
	v, err = f.svc.Create(ctx, f.req(eid(2), h.ID))
	if err != nil || v.Created || v.EnrollmentID != eid(2) {
		t.Fatalf("concurrent same id: %+v %v", v, err)
	}
	cr.conflicts, cr.before = 2, nil
	if _, err := f.svc.Create(ctx, f.req(eid(3), h.ID)); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("second conflict: %v", err)
	}
}

// TestCreateStoreErrors: storage failures surface unchanged.
func TestCreateStoreErrors(t *testing.T) {
	for _, method := range []string{"GetTeleportItemByEnrollment", "GetHost", "ListAgents", "CreateTeleportItem", "GetTeleportItem"} {
		f := newFix(t)
		h, _ := f.tpHost(t, "node-1", false)
		f.mem.FailNext(method)
		if _, err := f.svc.Create(ctx, f.req(eid(1), h.ID)); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
	// A failed push leaves the item pending.
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	f.reg.failPush = errBoom
	if v := f.create(t, eid(1), "node-1"); v.State != store.DeliveryPending {
		t.Fatalf("failed push: %+v", v)
	}
}

func TestDisabled(t *testing.T) {
	f := newFix(t)
	f.svc.cfg.Enabled = false
	if f.svc.Enabled() {
		t.Fatal("enabled")
	}
	if _, err := f.svc.Create(ctx, Request{}); !errors.Is(err, ErrDisabled) || err.Error() != "disabled_on_server" {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.GetMany(ctx, tenant, "teleport", []string{eid(1)}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("get: %v", err)
	}
	if _, err := f.svc.Cancel(ctx, tenant, "teleport", "", eid(1)); !errors.Is(err, ErrDisabled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, _, err := f.svc.ListTargets(ctx, tenant, TargetQuery{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("targets: %v", err)
	}
	if _, err := f.svc.Fetch(ctx, store.Agent{}, eid(1)); !errors.Is(err, ErrDisabled) {
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

// TestGetMany: by the source's enrollment ids only, in the order asked,
// unknown, malformed and repeated ids omitted; the view carries the
// hostname and the agent's presence.
func TestGetMany(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	f.tpHost(t, "node-2", false)
	v1 := f.create(t, eid(1), "node-1")
	v2 := f.create(t, eid(2), "node-2")
	got, err := f.svc.GetMany(ctx, strings.ToUpper(tenant), "teleport",
		[]string{strings.ToUpper(eid(2)), eid(1), eid(2), eid(9), "x"})
	if err != nil || len(got) != 2 || got[0].ID != v2.ID || got[1].ID != v1.ID || got[1].Hostname != "node-1" || !got[1].AgentOnline ||
		got[0].AgentOnline || got[0].Created {
		t.Fatalf("get = %+v %v", got, err)
	}
	if got, err := f.svc.GetMany(ctx, tenant, "other", []string{eid(1)}); err != nil || len(got) != 0 {
		t.Fatalf("other source = %+v %v", got, err)
	}
	if got, err := f.svc.GetMany(ctx, other, "teleport", []string{eid(1)}); err != nil || len(got) != 0 {
		t.Fatalf("other tenant = %+v %v", got, err)
	}
	var ie *InvalidError
	if _, err := f.svc.GetMany(ctx, tenant, "teleport", nil); !errors.As(err, &ie) || ie.Field != "enrollment_ids" {
		t.Fatalf("empty batch: %v", err)
	}
	if _, err := f.svc.GetMany(ctx, tenant, "teleport", make([]string, MaxBatch+1)); !errors.As(err, &ie) {
		t.Fatalf("oversized batch: %v", err)
	}
	for _, method := range []string{"GetTeleportItemByEnrollment", "GetTeleportItem"} {
		f.mem.FailNext(method)
		if _, err := f.svc.GetMany(ctx, tenant, "teleport", []string{eid(1)}); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
	// Presence unknown (registry error) and a deleted host: still answered.
	f.reg.failOn = errBoom
	if err := f.mem.DeleteHost(ctx, tenant, f.hosts["node-1"].ID); err != nil {
		t.Fatal(err)
	}
	got, err = f.svc.GetMany(ctx, tenant, "teleport", []string{eid(1)})
	if err != nil || len(got) != 1 || got[0].AgentOnline || got[0].Hostname != "" {
		t.Fatalf("degraded get = %+v %v", got, err)
	}
}

// TestCancel: an active item (even fetched or installing) becomes
// cancelled and audited once; a terminal item is unchanged; unknown or
// malformed ids are NotFound.
func TestCancel(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	v, a := f.fetched(t, 1, "node-1")
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.TeleportInstalling}); !ok || err != nil {
		t.Fatalf("installing: %v %v", ok, err)
	}
	got, err := f.svc.Cancel(ctx, strings.ToUpper(tenant), "teleport", "spiffe://example.org/svc/teleport", strings.ToUpper(eid(1)))
	if err != nil || got.State != store.DeliveryCancelled || got.Reason != "" || got.ID != v.ID {
		t.Fatalf("cancel = %+v %v", got, err)
	}
	again, err := f.svc.Cancel(ctx, tenant, "teleport", "", eid(1))
	if err != nil || again.State != store.DeliveryCancelled || f.count("teleport_join_cancelled") != 1 {
		t.Fatalf("cancel again = %+v %v", again, err)
	}
	// The agent's later report is ignored.
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.TeleportJoined, NodeHostUUID: nodeID}); ok || err != nil {
		t.Fatalf("report after cancel: %v %v", ok, err)
	}
	for name, id := range map[string]string{"unknown": eid(9), "malformed": "x"} {
		if _, err := f.svc.Cancel(ctx, tenant, "teleport", "", id); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.mem.FailNext("UpdateTeleportItem")
	if _, err := f.svc.Cancel(ctx, tenant, "teleport", "", eid(1)); err == nil {
		t.Fatal("update failure hidden")
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
	if len(rows) != 4 || rows[2].Detail["suppressed"] != 1 || rows[0].SubjectID != "" || rows[0].SubjectKind != "teleport_join" {
		t.Fatalf("rows = %+v", rows)
	}
	if f.met.refused[RefusedSourceNotAllowed] != 4 || len(f.svc.refused) != 1 {
		t.Fatalf("metrics %v / map %d", f.met.refused, len(f.svc.refused))
	}
}

func TestNilMetricsAndLogger(t *testing.T) {
	var o observer
	o.transition("a", "b")
	o.refused("a")
	o.source(time.Second, "ok")
	f := newFix(t)
	l := f.svc.log
	f.svc.SetLogger(nil)
	if f.svc.log != l {
		t.Fatal("nil logger replaced the current one")
	}
	nl := slog.New(slog.DiscardHandler)
	f.svc.SetLogger(nl)
	if f.svc.log != nl {
		t.Fatal("logger not installed")
	}
	if utcNow().Location() != time.UTC {
		t.Fatal("utcNow not UTC")
	}
}
