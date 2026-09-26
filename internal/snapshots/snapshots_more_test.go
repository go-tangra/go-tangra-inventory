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

// The report digest is set by the first snapshot, left unchanged (with its
// change time) by an identical one, and bumped when a projected field changes.
func TestIngest_ReportDigest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	clock := time.UnixMilli(1_700_000_000_000).UTC()
	f.svc.SetClock(func() time.Time { return clock })
	inv := baseInv("d", "uuid-d", time.Unix(1_700_000_000, 0).UTC())
	inv.Networks = []store.NetIface{{Name: "eth0", MAC: "00:11:22:33:44:55", Addresses: []store.IfAddress{{Address: "10.0.0.1", PrefixLength: 24, Family: "ipv4"}}}}

	s1, err := f.svc.Ingest(ctx, f.subj.TenantID, inv, store.SourceAgent)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := f.mem.GetHost(ctx, f.subj.TenantID, s1.HostID)
	if len(h1.ReportDigest) != 64 || !h1.ReportChangedAt.Equal(clock) {
		t.Fatalf("first snapshot: digest %q changed_at %v", h1.ReportDigest, h1.ReportChangedAt)
	}

	clock = clock.Add(time.Hour + 123456*time.Nanosecond)
	inv.CollectedAt = inv.CollectedAt.Add(time.Hour)
	if _, err := f.svc.Ingest(ctx, f.subj.TenantID, inv, store.SourceAgent); err != nil {
		t.Fatal(err)
	}
	h2, _ := f.mem.GetHost(ctx, f.subj.TenantID, s1.HostID)
	if h2.ReportDigest != h1.ReportDigest || !h2.ReportChangedAt.Equal(h1.ReportChangedAt) {
		t.Fatalf("identical snapshot bumped the report: %v -> %v", h1.ReportChangedAt, h2.ReportChangedAt)
	}

	inv.Networks[0].Addresses[0].Address = "10.0.0.2"
	inv.CollectedAt = inv.CollectedAt.Add(time.Hour)
	if _, err := f.svc.Ingest(ctx, f.subj.TenantID, inv, store.SourceAgent); err != nil {
		t.Fatal(err)
	}
	h3, _ := f.mem.GetHost(ctx, f.subj.TenantID, s1.HostID)
	if h3.ReportDigest == h1.ReportDigest || !h3.ReportChangedAt.Equal(clock.Truncate(time.Millisecond)) {
		t.Fatalf("changed interface did not bump the report: %q %v", h3.ReportDigest, h3.ReportChangedAt)
	}
}

// A failing digest write surfaces from Ingest (the watermark must not silently
// miss a change).
func TestIngest_ReportDigestErrorPropagates(t *testing.T) {
	f := newFixture(t)
	f.mem.FailNext("SetReportDigest")
	inv := baseInv("e", "uuid-e", time.Unix(1_700_000_000, 0).UTC())
	if _, err := f.svc.Ingest(context.Background(), f.subj.TenantID, inv, store.SourceAgent); err == nil {
		t.Fatal("expected injected SetReportDigest error")
	}
}
