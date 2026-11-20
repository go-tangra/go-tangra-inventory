package teleportjoin

import (
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestOnConnectReplays: pending items are delivered, delivered and fetched
// ones sent again, installing ones not; oldest first.
func TestOnConnectReplays(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", false)
	a := f.agent["node-1"]
	p := f.create(t, eid(1), "node-1")
	f.now = f.now.Add(time.Second)
	r := f.req(eid(2), f.hosts["node-1"].ID)
	r.ClusterID = other
	fe, err := f.svc.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Fetch(ctx, a, fe.ID); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	r = f.req(eid(3), f.hosts["node-1"].ID)
	r.ClusterID = eid(100)
	in, err := f.svc.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Fetch(ctx, a, in.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.svc.Report(ctx, a, Report{ItemID: in.ID, State: store.TeleportInstalling}); !ok {
		t.Fatal("installing")
	}
	cmds, err := f.svc.OnConnect(ctx, a)
	if err != nil || len(cmds) != 2 || cmds[0].TeleportJoin.ItemID != p.ID || cmds[1].TeleportJoin.ItemID != fe.ID {
		t.Fatalf("commands = %+v %v", cmds, err)
	}
	if f.item(t, p.ID).State != store.DeliveryDelivered || f.item(t, fe.ID).State != store.DeliveryFetched {
		t.Fatal("states after replay")
	}
	f.mem.FailNext("ListActiveTeleportItemsForAgent")
	if _, err := f.svc.OnConnect(ctx, a); err == nil {
		t.Fatal("list failure hidden")
	}
}

// TestOnConnectWithoutCapability: an agent that no longer announces
// teleport.v1 gets nothing; its pending and delivered items become
// unsupported, fetched ones are left to the report timeout.
func TestOnConnectWithoutCapability(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	a := f.agent["node-1"]
	d := f.create(t, eid(1), "node-1")
	r := f.req(eid(2), f.hosts["node-1"].ID)
	r.ClusterID = other
	fe, _ := f.svc.Create(ctx, r)
	if _, err := f.svc.Fetch(ctx, a, fe.ID); err != nil {
		t.Fatal(err)
	}
	a.Capabilities = []string{store.CapUpgradeV1}
	cmds, err := f.svc.OnConnect(ctx, a)
	if err != nil || len(cmds) != 0 {
		t.Fatalf("commands = %+v %v", cmds, err)
	}
	if i := f.item(t, d.ID); i.State != store.DeliveryUnsupported || i.Reason != CapabilityUpgradeRequired {
		t.Fatalf("delivered item = %+v", i)
	}
	if f.item(t, fe.ID).State != store.DeliveryFetched || f.count("teleport_join_unsupported") != 1 {
		t.Fatal("fetched item touched")
	}
}

// TestSweep: fetched and installing items without a report fail
// (report_timeout); pending, delivered and fetched items past expiry
// expire; installing items do not expire; errors surface.
func TestSweep(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	f.tpHost(t, "node-2", true)
	f.tpHost(t, "node-3", true)
	pending := f.create(t, eid(1), "node-1")
	fetched, _ := f.fetched(t, 2, "node-2")
	inst, a3 := f.fetched(t, 3, "node-3")
	if ok, _ := f.svc.Report(ctx, a3, Report{ItemID: inst.ID, State: store.TeleportInstalling}); !ok {
		t.Fatal("installing")
	}
	// Within the report timeout and the window: nothing.
	if e, fl, err := f.svc.Sweep(ctx); e+fl != 0 || err != nil {
		t.Fatalf("early sweep: %d %d %v", e, fl, err)
	}
	f.now = f.now.Add(31 * time.Minute)
	if e, fl, err := f.svc.Sweep(ctx); e != 0 || fl != 2 || err != nil {
		t.Fatalf("report sweep: %d %d %v", e, fl, err)
	}
	for _, id := range []string{fetched.ID, inst.ID} {
		if i := f.item(t, id); i.State != store.DeliveryFailed || i.Reason != store.ReasonReportTimeout {
			t.Fatalf("item = %+v", i)
		}
	}
	f.now = f.now.Add(24 * time.Hour)
	if e, fl, err := f.svc.Sweep(ctx); e != 1 || fl != 0 || err != nil {
		t.Fatalf("expiry sweep: %d %d %v", e, fl, err)
	}
	if f.item(t, pending.ID).State != store.DeliveryExpired || f.count("teleport_join_expired") != 1 {
		t.Fatal("pending item not expired")
	}
	f.mem.FailNext("ListStaleTeleportItems")
	if _, _, err := f.svc.Sweep(ctx); err == nil {
		t.Fatal("list failure hidden")
	}
}

// TestSweepInstallingNotExpired: an installing item past its expiry but
// within the report timeout is left alone; a stale row that changed since
// the listing is skipped; an update failure surfaces.
func TestSweepInstallingNotExpired(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	v, a := f.fetched(t, 1, "node-1")
	f.now = f.now.Add(24*time.Hour - time.Minute)
	if ok, _ := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.TeleportInstalling}); !ok {
		t.Fatal("installing")
	}
	f.now = f.now.Add(10 * time.Minute)
	// The store lists it (fetched/installing stale filter does not match,
	// but a racing replica's view may): the re-check skips it.
	stale := []store.TeleportJoinItem{f.item(t, v.ID)}
	f.svc.repo = &staleRepo{Mem: f.mem, stale: stale}
	if e, fl, err := f.svc.Sweep(ctx); e+fl != 0 || err != nil || f.item(t, v.ID).State != store.TeleportInstalling {
		t.Fatalf("installing past expiry: %d %d %v", e, fl, err)
	}
	f.mem.FailNext("UpdateTeleportItem")
	if _, _, err := f.svc.Sweep(ctx); err == nil {
		t.Fatal("update failure hidden")
	}
}

// TestMarkDeliveredAndOnline: a non-pending item is not marked again; an
// item without an agent is offline.
func TestMarkDeliveredAndOnline(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	v := f.create(t, eid(1), "node-1")
	f.svc.markDelivered(ctx, f.item(t, v.ID))
	if f.count("teleport_join_delivered") != 1 {
		t.Fatal("delivered twice")
	}
	if f.svc.online(ctx, "") {
		t.Fatal("no agent is online")
	}
}
