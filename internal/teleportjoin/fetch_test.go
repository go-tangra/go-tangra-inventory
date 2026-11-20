package teleportjoin

import (
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestFetchRelaysMaterial: the agent's own item is rendered for its host at
// fetch time, the item id is set, the item is fetched and counted, and
// nothing of the token is audited.
func TestFetchRelaysMaterial(t *testing.T) {
	f := newFix(t)
	h, a := f.tpHost(t, "node-1", true)
	v := f.create(t, eid(1), "node-1")
	m, err := f.svc.Fetch(ctx, a, strings.ToUpper(v.ID))
	if err != nil {
		t.Fatal(err)
	}
	if m.GetItemId() != v.ID || m.GetEnrollmentId() != eid(1) || m.GetToken() != secret || m.GetProxyAddr() != "teleport.example.com:443" {
		t.Fatalf("material = %+v", m)
	}
	if len(f.src.reqs) != 1 || f.src.reqs[0].GetHostId() != h.ID || f.src.reqs[0].GetEnrollmentId() != eid(1) ||
		f.src.reqs[0].GetItemId() != v.ID || f.src.reqs[0].GetTenantId() != tenant || !f.src.deadline {
		t.Fatalf("render requests = %+v deadline %v", f.src.reqs, f.src.deadline)
	}
	i := f.item(t, v.ID)
	if i.State != store.DeliveryFetched || i.Fetches != 1 || i.FetchedAt == nil {
		t.Fatalf("item = %+v", i)
	}
	if f.count("teleport_join_fetched") != 1 || f.met.source["ok"] != 1 {
		t.Fatalf("audit/metrics: %v", f.met.source)
	}
	for _, r := range f.mem.AuditRows() {
		if r.Action == "teleport_join_fetched" && (r.Detail["teleport_version"] != "17.4.2" || r.Detail["labels"] != 1 || r.Detail["ca_pins"] != 1) {
			t.Fatalf("fetched row = %+v", r.Detail)
		}
	}
	f.noSecret(t)
	// A second fetch (agent retry) renders fresh material and stays fetched.
	if _, err := f.svc.Fetch(ctx, a, v.ID); err != nil || f.item(t, v.ID).Fetches != 2 {
		t.Fatalf("second fetch: %v", err)
	}
	Wipe(m)
	Wipe(nil)
	if m.GetToken() != "" {
		t.Fatal("wipe kept the token")
	}
}

// TestFetchOwnItemOnly: another agent's, another tenant's, a missing, an
// expired or a spent item is NotFound and audited as refused.
func TestFetchOwnItemOnly(t *testing.T) {
	f := newFix(t)
	_, a := f.tpHost(t, "node-1", true)
	_, b := f.tpHost(t, "node-2", true)
	v := f.create(t, eid(1), "node-1")
	stranger := a
	stranger.TenantID = other
	for name, ag := range map[string]store.Agent{"other agent": b, "other tenant": stranger} {
		if _, err := f.svc.Fetch(ctx, ag, v.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.svc.Fetch(ctx, a, eid(42)); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	var ie *InvalidError
	if _, err := f.svc.Fetch(ctx, a, "x"); !errors.As(err, &ie) {
		t.Fatalf("malformed: %v", err)
	}
	// Spent budget.
	if _, err := f.mem.UpdateTeleportItem(ctx, tenant, v.ID, func(i *store.TeleportJoinItem) ([]store.AuditRow, error) {
		i.Fetches = store.MaxTeleportFetches
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) || f.met.refused[RefusedFetchLimit] != 1 {
		t.Fatalf("spent: %v %v", err, f.met.refused)
	}
	// Expired.
	v2 := f.create(t, eid(2), "node-2")
	f.now = f.now.Add(25 * time.Hour)
	if _, err := f.svc.Fetch(ctx, b, v2.ID); !errors.Is(err, repo.ErrNotFound) || f.met.refused[RefusedNotActive] != 1 {
		t.Fatalf("expired: %v %v", err, f.met.refused)
	}
	if f.met.refused[RefusedNotFound] != 3 || len(f.src.reqs) != 0 {
		t.Fatalf("refusals %v renders %d", f.met.refused, len(f.src.reqs))
	}
}

// TestFetchInstallingRefused: material already handed out for an item
// being installed is not rendered again.
func TestFetchInstallingRefused(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	v, a := f.fetched(t, 1, "node-1")
	if ok, _ := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.TeleportInstalling}); !ok {
		t.Fatal("installing refused")
	}
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) || len(f.src.reqs) != 1 {
		t.Fatalf("installing fetch: %v renders %d", err, len(f.src.reqs))
	}
}

// TestFetchAmbiguousHost: a second agent on the host since the item was
// created makes the item unsupported; nothing is rendered.
func TestFetchAmbiguousHost(t *testing.T) {
	f := newFix(t)
	_, a := f.tpHost(t, "node-1", true)
	v := f.create(t, eid(1), "node-1")
	f.agentFor(t, "node-1", "linux", "4.9.0", true, store.CapTeleportV1)
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("ambiguous: %v", err)
	}
	if i := f.item(t, v.ID); i.State != store.DeliveryUnsupported || i.Reason != store.ReasonAmbiguousAgent || len(f.src.reqs) != 0 {
		t.Fatalf("item = %+v", i)
	}
}

// TestFetchSourceRefusal: NotFound from the source fails the item
// (source_refused); the agent gets NotFound.
func TestFetchSourceRefusal(t *testing.T) {
	f := newFix(t)
	_, a := f.tpHost(t, "node-1", true)
	v := f.create(t, eid(1), "node-1")
	f.src.callErr = notFound("unknown enrollment")
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("refused: %v", err)
	}
	if i := f.item(t, v.ID); i.State != store.DeliveryFailed || i.Reason != store.ReasonSourceRefused || f.met.source["refused"] != 1 {
		t.Fatalf("item = %+v %v", i, f.met.source)
	}
}

// TestFetchBudget: other source errors and malformed material are
// retryable (Unavailable) until the fifth fetch fails the item.
func TestFetchBudget(t *testing.T) {
	f := newFix(t)
	_, a := f.tpHost(t, "node-1", true)
	v := f.create(t, eid(1), "node-1")
	f.src.callErr = status.Error(codes.Unavailable, "down")
	for n := 1; n < store.MaxTeleportFetches; n++ {
		if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("fetch %d: %v", n, err)
		}
		if n == 2 {
			// Malformed material counts the same way.
			f.src.callErr = nil
			f.src.mutate = func(m *invv1.TeleportJoinMaterial) { m.Token = "" }
		}
		if n == 3 {
			f.src.mutate = func(m *invv1.TeleportJoinMaterial) { m.EnrollmentId = eid(77) }
		}
	}
	if f.item(t, v.ID).State != store.DeliveryDelivered {
		t.Fatal("item ended before the budget")
	}
	f.src.mutate = func(m *invv1.TeleportJoinMaterial) { m.Token = strings.Repeat("t", MaxTokenBytes+1) }
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("last fetch: %v", err)
	}
	if i := f.item(t, v.ID); i.State != store.DeliveryFailed || i.Reason != store.ReasonFetchBudget || i.Fetches != 5 {
		t.Fatalf("item = %+v", i)
	}
	if f.met.source["unavailable"] != 2 || f.met.source["bad_material"] != 3 {
		t.Fatalf("source metrics = %v", f.met.source)
	}
	if sourceCode(errBoom) != codes.Unknown || sourceErr(errBoom).Error() != ErrSourceUnavailable.Error() {
		t.Fatal("source error mapping")
	}
}

// TestFetchRaces: the item changing between the checks and the updates is
// handled under the row lock.
func TestFetchRaces(t *testing.T) {
	f := newFix(t)
	_, a := f.tpHost(t, "node-1", true)
	v := f.create(t, eid(1), "node-1")
	// Superseded while the source renders: the material is dropped.
	f.src.during = func() {
		_, _ = f.mem.UpdateTeleportItem(ctx, tenant, v.ID, func(i *store.TeleportJoinItem) ([]store.AuditRow, error) {
			i.State = store.DeliverySuperseded
			return nil, nil
		})
	}
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("superseded during render: %v", err)
	}
	// Cancelled between the ownership check and the counted fetch.
	v2 := f.create(t, eid(2), "node-1")
	f.src.during = nil
	rr := &raceRepo{Mem: f.mem, onList: func() {
		_, _ = f.mem.UpdateTeleportItem(ctx, tenant, v2.ID, func(i *store.TeleportJoinItem) ([]store.AuditRow, error) {
			i.State = store.DeliveryCancelled
			return nil, nil
		})
	}}
	f.svc.repo = rr
	if _, err := f.svc.Fetch(ctx, a, v2.ID); !errors.Is(err, repo.ErrNotFound) || len(f.src.reqs) != 1 {
		t.Fatalf("cancelled before count: %v renders %d", err, len(f.src.reqs))
	}
	// failItem on an item that ended meanwhile changes nothing.
	if err := f.svc.failItem(ctx, f.item(t, v2.ID), store.DeliveryFailed, "x", "teleport_join_failed"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("failItem on a terminal item: %v", err)
	}
}

// TestFetchStoreErrors: storage failures surface unchanged.
func TestFetchStoreErrors(t *testing.T) {
	for _, method := range []string{"GetTeleportItem", "ListAgents", "UpdateTeleportItem"} {
		f := newFix(t)
		_, a := f.tpHost(t, "node-1", true)
		v := f.create(t, eid(1), "node-1")
		f.mem.FailNext(method)
		if _, err := f.svc.Fetch(ctx, a, v.ID); err == nil || errors.Is(err, repo.ErrNotFound) {
			t.Errorf("%s: %v", method, err)
		}
	}
	// The final update failing drops the material.
	f := newFix(t)
	_, a := f.tpHost(t, "node-1", true)
	v := f.create(t, eid(1), "node-1")
	f.src.during = func() { f.mem.FailNext("UpdateTeleportItem") }
	if _, err := f.svc.Fetch(ctx, a, v.ID); err == nil || errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("final update: %v", err)
	}
	// failItem failing to store surfaces the error.
	f = newFix(t)
	_, a = f.tpHost(t, "node-1", true)
	v = f.create(t, eid(1), "node-1")
	f.src.callErr = notFound("gone")
	f.src.during = func() { f.mem.FailNext("UpdateTeleportItem") }
	if _, err := f.svc.Fetch(ctx, a, v.ID); err == nil || errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("failItem store error: %v", err)
	}
	// No source timeout configured: no deadline added.
	f = newFix(t)
	_, a = f.tpHost(t, "node-1", true)
	v = f.create(t, eid(1), "node-1")
	f.svc.cfg.SourceTimeout = 0
	if _, err := f.svc.Fetch(ctx, a, v.ID); err != nil || f.src.deadline {
		t.Fatalf("no timeout: %v deadline %v", err, f.src.deadline)
	}
}

// TestRefusePlaintext records the refusal.
func TestRefusePlaintext(t *testing.T) {
	f := newFix(t)
	_, a := f.tpHost(t, "node-1", true)
	f.svc.RefusePlaintext(ctx, a, eid(1))
	if f.met.refused[RefusedPlaintext] != 1 || f.count("teleport_join_refused") != 1 {
		t.Fatalf("refused = %v", f.met.refused)
	}
}
