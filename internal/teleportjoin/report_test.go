package teleportjoin

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestReportJoined: installing then joined with the node's host uuid (case
// folded); the transitions are audited; a repeat is ignored.
func TestReportJoined(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	v, a := f.fetched(t, 1, "node-1")
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: strings.ToUpper(v.ID), State: store.TeleportInstalling, Detail: "downloading"}); !ok || err != nil {
		t.Fatalf("installing: %v %v", ok, err)
	}
	if i := f.item(t, v.ID); i.State != store.TeleportInstalling || i.ReportedAt == nil {
		t.Fatalf("item = %+v", i)
	}
	// A repeated installing is the agent asking whether the item is still
	// active: accepted, unchanged, not audited again, no refusal.
	before := f.item(t, v.ID).UpdatedAt
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.TeleportInstalling}); !ok || err != nil || f.met.refused[RefusedNotFetched] != 0 {
		t.Fatalf("repeated installing: %v %v", ok, err)
	}
	if i := f.item(t, v.ID); !i.UpdatedAt.Equal(before) {
		t.Fatalf("repeated installing moved updated_at: %v -> %v", before, i.UpdatedAt)
	}
	ok, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.TeleportJoined, NodeHostUUID: strings.ToUpper(nodeID), Detail: "ok\x00\n"})
	if !ok || err != nil {
		t.Fatalf("joined: %v %v", ok, err)
	}
	if i := f.item(t, v.ID); i.State != store.TeleportJoined || i.NodeHostUUID != nodeID || i.Reason != "" {
		t.Fatalf("item = %+v", i)
	}
	if f.count("teleport_join_installing") != 1 || f.count("teleport_join_joined") != 1 {
		t.Fatalf("audit = %v", f.mem.AuditRows())
	}
	for _, r := range f.mem.AuditRows() {
		if r.Action == "teleport_join_joined" && (r.Detail["node_host_uuid"] != nodeID || r.Detail["detail"] != "ok  " || r.ActorKind != "agent") {
			t.Fatalf("joined row = %+v", r)
		}
	}
	// Terminal: a repeat (or anything else) is ignored.
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.TeleportJoined, NodeHostUUID: nodeID}); ok || err != nil {
		t.Fatalf("repeat: %v %v", ok, err)
	}
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.DeliveryFailed, Reason: store.ReasonTimeout}); ok || err != nil {
		t.Fatalf("failed after joined: %v %v", ok, err)
	}
	f.noSecret(t)
}

// TestReportJoinedFromFetched: joined may follow fetched directly.
func TestReportJoinedFromFetched(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	v, a := f.fetched(t, 1, "node-1")
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.TeleportJoined, NodeHostUUID: nodeID}); !ok || err != nil {
		t.Fatalf("joined: %v %v", ok, err)
	}
}

// TestReportFailed: failed carries a closed reason and may come before any
// fetch (busy, disabled_locally, already_installed, ...).
func TestReportFailed(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	v := f.create(t, eid(1), "node-1")
	a := f.agent["node-1"]
	long := strings.Repeat("é", 200)
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.DeliveryFailed, Reason: store.ReasonDisabledLocally, Detail: long}); !ok || err != nil {
		t.Fatalf("failed: %v %v", ok, err)
	}
	i := f.item(t, v.ID)
	if i.State != store.DeliveryFailed || i.Reason != store.ReasonDisabledLocally {
		t.Fatalf("item = %+v", i)
	}
	for _, r := range f.mem.AuditRows() {
		if r.Action == "teleport_join_failed" {
			if d, _ := r.Detail["detail"].(string); len(d) > store.MaxDetailBytes || r.Outcome != "error" {
				t.Fatalf("failed row = %+v", r)
			}
		}
	}
}

// TestReportNotFetched: installing or joined before any fetch is ignored
// and refused (not_fetched).
func TestReportNotFetched(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	v := f.create(t, eid(1), "node-1")
	a := f.agent["node-1"]
	for _, r := range []Report{{ItemID: v.ID, State: store.TeleportInstalling}, {ItemID: v.ID, State: store.TeleportJoined, NodeHostUUID: nodeID}} {
		if ok, err := f.svc.Report(ctx, a, r); ok || err != nil {
			t.Fatalf("%s before fetch: %v %v", r.State, ok, err)
		}
	}
	if f.met.refused[RefusedNotFetched] != 2 || f.item(t, v.ID).State != store.DeliveryDelivered {
		t.Fatalf("refused = %v", f.met.refused)
	}
}

// TestReportValidation: closed sets and the reason/uuid rules per state.
func TestReportValidation(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "node-1", true)
	v, a := f.fetched(t, 1, "node-1")
	for name, r := range map[string]Report{
		"item id":              {ItemID: "x", State: store.DeliveryFailed, Reason: store.ReasonBusy},
		"state":                {ItemID: v.ID, State: "installed"},
		"failed no reason":     {ItemID: v.ID, State: store.DeliveryFailed},
		"failed bad reason":    {ItemID: v.ID, State: store.DeliveryFailed, Reason: "hook_failed"},
		"installing reason":    {ItemID: v.ID, State: store.TeleportInstalling, Reason: store.ReasonBusy},
		"joined without uuid":  {ItemID: v.ID, State: store.TeleportJoined},
		"joined bad uuid":      {ItemID: v.ID, State: store.TeleportJoined, NodeHostUUID: "host-1"},
		"failed with uuid":     {ItemID: v.ID, State: store.DeliveryFailed, Reason: store.ReasonJoinFailed, NodeHostUUID: nodeID},
		"installing with uuid": {ItemID: v.ID, State: store.TeleportInstalling, NodeHostUUID: nodeID},
	} {
		var ie *InvalidError
		if _, err := f.svc.Report(ctx, a, r); !errors.As(err, &ie) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Another agent's item: NotFound.
	_, b := f.tpHost(t, "node-2", true)
	if _, err := f.svc.Report(ctx, b, Report{ItemID: v.ID, State: store.DeliveryFailed, Reason: store.ReasonBusy}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("other agent: %v", err)
	}
	// Storage failures surface.
	f.mem.FailNext("UpdateTeleportItem")
	if _, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.DeliveryFailed, Reason: store.ReasonBusy}); err == nil {
		t.Fatal("update failure hidden")
	}
	f.mem.FailNext("GetTeleportItem")
	if _, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.DeliveryFailed, Reason: store.ReasonBusy}); err == nil {
		t.Fatal("get failure hidden")
	}
}

func TestSanitize(t *testing.T) {
	if sanitize("x -----BEGIN PRIVATE KEY----- y") != "" {
		t.Fatal("PEM kept")
	}
	if got := sanitize("a\tb\x7fc\u0085d\xffe"); got != "a b c d?e" {
		t.Fatalf("sanitize = %q", got)
	}
}
