package snapshots_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/authz"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestForbidden_ReadMethods exercises the RequireTenant guard on the read paths.
func TestForbidden_ReadMethods(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	empty := authz.Subjects{}

	if _, err := f.svc.Get(ctx, empty, "x"); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("Get: %v", err)
	}
	if _, err := f.svc.GetLatestForHost(ctx, empty, "x"); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("GetLatestForHost: %v", err)
	}
	if _, err := f.svc.ListForHost(ctx, empty, "x", 0, ""); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("ListForHost: %v", err)
	}
	if err := f.svc.Delete(ctx, empty, "x"); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("Delete: %v", err)
	}
	if _, err := f.svc.Diff(ctx, empty, "a", "b"); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("Diff: %v", err)
	}
	if _, err := f.svc.ListChanges(ctx, empty, "x"); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("ListChanges: %v", err)
	}
}

// TestNotFound_ReadMethods maps store not-found to the package sentinel.
func TestNotFound_ReadMethods(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Get(ctx, f.subj, "missing"); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("Get: %v", err)
	}
	if _, err := f.svc.GetLatestForHost(ctx, f.subj, "missing"); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("GetLatestForHost: %v", err)
	}
	if err := f.svc.Delete(ctx, f.subj, "missing"); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("Delete: %v", err)
	}
}

// TestIngest_LiftsBaseboardFallback covers the summary fallback to baseboard
// fields when the system record is empty.
func TestIngest_LiftsBaseboardFallback(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	inv := store.Inventory{
		Identity:  store.Identity{Hostname: "bb", HardwareUUID: "uuid-bb"},
		Baseboard: store.BaseboardInfo{Manufacturer: "Supermicro", Product: "X11"},
		OS:        store.OSInfo{Name: "Linux"},
	}
	snap, err := f.svc.Ingest(ctx, f.subj.TenantID, inv, store.SourceManual)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if snap.Manufacturer != "Supermicro" || snap.Model != "X11" {
		t.Errorf("baseboard fallback not applied: manufacturer=%q model=%q", snap.Manufacturer, snap.Model)
	}
}

// TestIngest_StoreErrorPropagates ensures an injected InsertSnapshot failure
// surfaces from Ingest.
func TestIngest_StoreErrorPropagates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.mem.FailNext("InsertSnapshot")
	inv := baseInv("h", "uuid-h", time.Unix(1_700_000_000, 0).UTC())
	if _, err := f.svc.Ingest(ctx, f.subj.TenantID, inv, store.SourceAgent); err == nil {
		t.Errorf("expected injected InsertSnapshot error")
	}
}
