package moduledelivery

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// fetched creates and fetches an item of gw-1.
func fetched(t *testing.T) (*fix, store.Agent, string) {
	t.Helper()
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	b, err := f.svc.Fetch(ctx, a, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	b.Wipe()
	return f, a, v.ID
}

// TestReport (T008): own item only; closed sets; idempotent duplicates;
// a hook failure keeps its exit code; the detail is sanitised.
func TestReport(t *testing.T) {
	f, a, id := fetched(t)
	ok, err := f.svc.Report(ctx, a, Report{ItemID: id, State: store.DeliveryHookFailed, HookExitCode: 3,
		Detail: "hook exited\nwith code 3" + strings.Repeat("x", 400)})
	if err != nil || !ok {
		t.Fatalf("report: %v %v", ok, err)
	}
	i := f.item(t, id)
	if i.State != store.DeliveryHookFailed || i.Reason != store.ReasonHookFailed || i.HookExitCode != 3 || i.ReportedAt == nil {
		t.Fatalf("item = %+v", i)
	}
	for _, r := range f.mem.AuditRows() {
		if r.Action == "module_delivery_hook_failed" {
			d := r.Detail["detail"].(string)
			if len(d) != store.MaxDetailBytes || strings.Contains(d, "\n") || r.Detail["hook_exit_code"] != 3 || r.ActorID != a.ID {
				t.Fatalf("row = %+v", r)
			}
		}
	}
	// The same report again (lost answer): accepted, no change.
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: id, State: store.DeliveryHookFailed, HookExitCode: 3}); err != nil || !ok {
		t.Fatalf("duplicate: %v %v", ok, err)
	}
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: id, State: store.DeliveryHookFailed, Reason: store.ReasonHookFailed, HookExitCode: 3}); err != nil || !ok {
		t.Fatalf("duplicate with reason: %v %v", ok, err)
	}
	// Another report on the terminal item: ignored.
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: id, State: store.DeliveryInstalled, HookExitCode: 0}); err != nil || ok {
		t.Fatalf("conflicting: %v %v", ok, err)
	}
	if f.count("module_delivery_hook_failed") != 1 {
		t.Fatal("duplicate wrote a row")
	}
}

func TestReportInstalledAndFailed(t *testing.T) {
	f, a, id := fetched(t)
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: id, State: store.DeliveryInstalled, Reason: store.ReasonBusy, HookExitCode: 0,
		Detail: "-----BEGIN PRIVATE KEY-----"}); err != nil || !ok {
		t.Fatalf("installed: %v %v", ok, err)
	}
	if i := f.item(t, id); i.State != store.DeliveryInstalled || i.Reason != "" || i.HookExitCode != 0 {
		t.Fatalf("item = %+v", i)
	}
	for _, r := range f.mem.AuditRows() {
		if r.Action == "module_delivery_installed" && r.Detail["detail"] != "" {
			t.Fatal("PEM detail kept")
		}
	}
	// Failed before any fetch (disabled locally) is accepted.
	f2 := newFix(t)
	_, a2 := f2.modHost(t, "gw-1", true)
	v := f2.create(t, did(1), "gw-1")
	if ok, err := f2.svc.Report(ctx, a2, Report{ItemID: v.ID, State: store.DeliveryFailed, Reason: store.ReasonDisabledLocally, HookExitCode: -1}); err != nil || !ok {
		t.Fatalf("disabled locally: %v %v", ok, err)
	}
	if i := f2.item(t, v.ID); i.State != store.DeliveryFailed || i.Reason != store.ReasonDisabledLocally {
		t.Fatalf("item = %+v", i)
	}
}

// TestReportRefusals: an install claimed without a fetch is ignored and
// audited; another agent's item is NotFound; malformed reports are invalid.
func TestReportRefusals(t *testing.T) {
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	_, b := f.modHost(t, "gw-2", true)
	v := f.create(t, did(1), "gw-1")
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.DeliveryInstalled, HookExitCode: -1}); err != nil || ok {
		t.Fatalf("installed without fetch: %v %v", ok, err)
	}
	if f.met.refused[RefusedNotFetched] != 1 || !f.item(t, v.ID).Active() {
		t.Fatal("not refused")
	}
	if _, err := f.svc.Report(ctx, b, Report{ItemID: v.ID, State: store.DeliveryFailed, HookExitCode: -1}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("other agent: %v", err)
	}
	for name, r := range map[string]Report{
		"item_id":        {ItemID: "x", State: "failed", HookExitCode: -1},
		"state":          {ItemID: v.ID, State: "unchanged", HookExitCode: -1},
		"reason":         {ItemID: v.ID, State: "failed", Reason: "no_report", HookExitCode: -1},
		"hook_exit_code": {ItemID: v.ID, State: "failed", HookExitCode: 257},
		"hook low":       {ItemID: v.ID, State: "failed", HookExitCode: -2},
	} {
		var ie *InvalidError
		if _, err := f.svc.Report(ctx, a, r); !errors.As(err, &ie) {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.mem.FailNext("UpdateModuleItem")
	if _, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.DeliveryFailed, HookExitCode: -1}); err == nil {
		t.Fatal("store failure hidden")
	}
	f.mem.FailNext("GetModuleItem")
	if _, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.DeliveryFailed, HookExitCode: -1}); err == nil || errors.Is(err, repo.ErrNotFound) {
		t.Fatal("store failure hidden")
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("a\x00b\x7fc\u0085d\xff"); got != "a b c d?" {
		t.Fatalf("sanitize = %q", got)
	}
	if got := sanitize(strings.Repeat("é", 200)); len(got) != 256 {
		t.Fatalf("multi-byte cut = %d", len(got))
	}
}
