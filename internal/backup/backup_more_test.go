package backup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/authz"
	"github.com/go-freya/freya/services/inventory/internal/backup"
	"github.com/go-freya/freya/services/inventory/internal/memstore"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

func TestSetClock_UsedInExportTimestamp(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	svc := backup.New(m)
	fixed := time.Unix(1_700_000_777, 0).UTC()
	svc.SetClock(func() time.Time { return fixed })

	b, err := svc.Export(context.Background(), subjFor(tenant), false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !b.ExportedAt.Equal(fixed) {
		t.Errorf("ExportedAt = %v, want %v", b.ExportedAt, fixed)
	}
}

func TestImport_Forbidden(t *testing.T) {
	m := memstore.New()
	_, err := backup.New(m).Import(context.Background(), authz.Subjects{}, backup.Backup{SchemaVersion: backup.SchemaVersion}, backup.ModeSkip)
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("err = %v, want authz.ErrForbidden", err)
	}
}

func TestImport_StoreErrorPropagates(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	b := backup.Backup{
		SchemaVersion: backup.SchemaVersion,
		Hosts:         []backup.HostExport{{ID: store.NewID(), Hostname: "h", HardwareUUID: "uuid-h", Status: store.HostActive}},
	}
	m.FailNext("ResolveHost")
	if _, err := backup.New(m).Import(context.Background(), subjFor(tenant), b, backup.ModeSkip); err == nil {
		t.Errorf("expected injected ResolveHost error")
	}
}

func TestImport_ChangesOnlyBackup(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	b := backup.Backup{
		SchemaVersion: backup.SchemaVersion,
		Changes: []backup.ChangeExport{{
			ID: store.NewID(), HostID: "h1", SnapshotID: "s1", Category: "bios",
			ChangeType: store.ChangeModified, ComponentKey: "bios",
		}},
	}
	res, err := backup.New(m).Import(context.Background(), subjFor(tenant), b, backup.ModeSkip)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ChangesImported != 1 {
		t.Errorf("ChangesImported = %d, want 1", res.ChangesImported)
	}
}

func TestExport_LatestOnly_HostWithoutSnapshots(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	if _, err := m.ResolveHost(context.Background(), tenant, store.Host{Hostname: "bare", HardwareUUID: "uuid-bare"}); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	b, err := backup.New(m).Export(context.Background(), subjFor(tenant), false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(b.Hosts) != 1 {
		t.Errorf("hosts = %d, want 1", len(b.Hosts))
	}
	if len(b.Snapshots) != 0 {
		t.Errorf("snapshots = %d, want 0 (host never reported)", len(b.Snapshots))
	}
}

func TestImport_OverwriteDeleteHostError(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	if _, err := m.ResolveHost(context.Background(), tenant, store.Host{ID: "host-1", Hostname: "h", HardwareUUID: "uuid-h"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	b := backup.Backup{SchemaVersion: backup.SchemaVersion, Hosts: []backup.HostExport{{ID: "host-1", Hostname: "h", HardwareUUID: "uuid-h", Status: store.HostActive}}}
	m.FailNext("DeleteHost")
	if _, err := backup.New(m).Import(context.Background(), subjFor(tenant), b, backup.ModeOverwrite); err == nil {
		t.Errorf("expected injected DeleteHost error")
	}
}

func TestImport_OverwriteDeleteSnapshotError(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	if err := m.InsertSnapshot(context.Background(), store.Snapshot{ID: "snap-1", TenantID: tenant, HostID: "h"}); err != nil {
		t.Fatalf("seed snap: %v", err)
	}
	b := backup.Backup{SchemaVersion: backup.SchemaVersion, Snapshots: []backup.SnapshotExport{{ID: "snap-1", HostID: "h"}}}
	m.FailNext("DeleteSnapshot")
	if _, err := backup.New(m).Import(context.Background(), subjFor(tenant), b, backup.ModeOverwrite); err == nil {
		t.Errorf("expected injected DeleteSnapshot error")
	}
}

func TestImport_InsertSnapshotError(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	b := backup.Backup{SchemaVersion: backup.SchemaVersion, Snapshots: []backup.SnapshotExport{{ID: "snap-x", HostID: "h"}}}
	m.FailNext("InsertSnapshot")
	if _, err := backup.New(m).Import(context.Background(), subjFor(tenant), b, backup.ModeSkip); err == nil {
		t.Errorf("expected injected InsertSnapshot error")
	}
}

func TestImport_InsertChangesError(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	b := backup.Backup{SchemaVersion: backup.SchemaVersion, Changes: []backup.ChangeExport{{ID: "c1", HostID: "h", Category: "bios", ChangeType: store.ChangeModified, ComponentKey: "bios"}}}
	m.FailNext("InsertChanges")
	if _, err := backup.New(m).Import(context.Background(), subjFor(tenant), b, backup.ModeSkip); err == nil {
		t.Errorf("expected injected InsertChanges error")
	}
}
