package moduledelivery

import (
	"context"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestOnConnectReplays (T006): pending items are pushed as delivered on
// connect, delivered and fetched ones are sent again, terminal ones not.
func TestOnConnectReplays(t *testing.T) {
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", false)
	p := f.create(t, did(1), "gw-1")
	r := f.req(did(2), a.HostID)
	r.Module = "mail"
	m, _ := f.svc.Create(ctx, r)
	r.DeliveryID, r.Module = did(3), "done"
	d, _ := f.svc.Create(ctx, r)
	if _, err := f.svc.Report(ctx, a, Report{ItemID: d.ID, State: store.DeliveryFailed, HookExitCode: -1}); err != nil {
		t.Fatal(err)
	}
	cmds, err := f.svc.OnConnect(ctx, a)
	if err != nil || len(cmds) != 2 || cmds[0].Module.ItemID != p.ID || cmds[1].Module.ItemID != m.ID {
		t.Fatalf("commands = %+v %v", cmds, err)
	}
	if f.item(t, p.ID).State != store.DeliveryDelivered {
		t.Fatal("not delivered")
	}
	if cmds, _ := f.svc.OnConnect(ctx, a); len(cmds) != 2 || f.count("module_delivery_delivered") != 2 {
		t.Fatalf("second connect = %d (delivered rows %d)", len(cmds), f.count("module_delivery_delivered"))
	}
	f.mem.FailNext("ListActiveModuleItemsForAgent")
	if _, err := f.svc.OnConnect(ctx, a); err == nil {
		t.Fatal("store failure hidden")
	}
}

// TestOnConnectWithoutCapability (T006): an agent that stopped announcing
// module.v1 gets nothing; its pending and delivered items become
// unsupported with the capability as reason; a fetched one waits.
func TestOnConnectWithoutCapability(t *testing.T) {
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	r := f.req(did(2), a.HostID)
	r.Module = "mail"
	fm, _ := f.svc.Create(ctx, r)
	f.src.module = "mail"
	if b, err := f.svc.Fetch(ctx, a, fm.ID); err != nil {
		t.Fatal(err)
	} else {
		b.Wipe()
	}
	a.Capabilities = []string{store.CapUpgradeV1}
	cmds, err := f.svc.OnConnect(ctx, a)
	if err != nil || len(cmds) != 0 {
		t.Fatalf("commands = %v %v", cmds, err)
	}
	if i := f.item(t, v.ID); i.State != store.DeliveryUnsupported || i.Reason != store.ReasonDisabledOnHost {
		t.Fatalf("item = %+v", i)
	}
	if f.item(t, fm.ID).State != store.DeliveryFetched {
		t.Fatal("fetched item touched")
	}
}

// TestSweep (T009): a fetched item without a report fails after the report
// timeout (report_timeout); an active item past its expiry expires.
func TestSweep(t *testing.T) {
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	b, err := f.svc.Fetch(ctx, a, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	b.Wipe()
	r := f.req(did(2), a.HostID)
	r.Module, r.ExpiresAt = "mail", f.now.Add(time.Hour)
	m, _ := f.svc.Create(ctx, r)
	if e, fl, err := f.svc.Sweep(ctx); e+fl != 0 || err != nil {
		t.Fatalf("early sweep: %d %d %v", e, fl, err)
	}
	f.now = f.now.Add(31 * time.Minute)
	if e, fl, err := f.svc.Sweep(ctx); e != 0 || fl != 1 || err != nil {
		t.Fatalf("report sweep: %d %d %v", e, fl, err)
	}
	if i := f.item(t, v.ID); i.State != store.DeliveryFailed || i.Reason != store.ReasonReportTimeout {
		t.Fatalf("item = %+v", i)
	}
	f.now = f.now.Add(time.Hour)
	if e, fl, err := f.svc.Sweep(ctx); e != 1 || fl != 0 || err != nil {
		t.Fatalf("expiry sweep: %d %d %v", e, fl, err)
	}
	if i := f.item(t, m.ID); i.State != store.DeliveryExpired || i.Reason != "" {
		t.Fatalf("item = %+v", i)
	}
	if f.count("module_delivery_expired") != 1 {
		t.Fatal("expiry not audited")
	}
	f.mem.FailNext("ListStaleModuleItems")
	if _, _, err := f.svc.Sweep(ctx); err == nil {
		t.Fatal("list failure hidden")
	}
}

// TestSweepRace: an item that changed between the listing and its lock is
// left alone; an update failure stops the run.
func TestSweepRace(t *testing.T) {
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	f.now = v.ExpiresAt.Add(time.Second)
	stale, _ := f.mem.ListStaleModuleItems(ctx, f.now, f.now, 0)
	if len(stale) != 1 {
		t.Fatal("not stale")
	}
	f.svc.repo = staleRepo{Repo: f.svc.repo, items: []store.ModuleDeliveryItem{stale[0], stale[0]}}
	if e, _, err := f.svc.Sweep(ctx); e != 1 || err != nil {
		t.Fatalf("sweep = %d %v", e, err)
	}
	_ = a
	f.mem.FailNext("UpdateModuleItem")
	if _, _, err := f.svc.Sweep(ctx); err == nil {
		t.Fatal("update failure hidden")
	}
}

// staleRepo lists fixed stale items.
type staleRepo struct {
	Repo
	items []store.ModuleDeliveryItem
}

func (s staleRepo) ListStaleModuleItems(_ context.Context, _, _ time.Time, _ int) ([]store.ModuleDeliveryItem, error) {
	return s.items, nil
}
