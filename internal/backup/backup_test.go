package backup_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/authz"
	"github.com/go-freya/freya/services/inventory/internal/backup"
	"github.com/go-freya/freya/services/inventory/internal/memstore"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

func subjFor(tenant string) authz.Subjects {
	return authz.Subjects{TenantID: tenant, UserID: "u1", Roles: []string{"admin"}, ActorKind: authz.ActorUser}
}

// seedTenant populates a tenant with one host, two snapshots, a change, and an
// agent (whose secret must never appear in a backup).
func seedTenant(t *testing.T, m *memstore.Mem, tenant string) (hostID, snapID string) {
	t.Helper()
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()
	h, err := m.ResolveHost(ctx, tenant, store.Host{
		Hostname: "web01", HardwareUUID: "uuid-web01", Manufacturer: "Dell", Model: "OptiPlex",
		OSName: "Ubuntu", Tags: map[string]string{"env": "prod"}, LastSeen: at,
	})
	if err != nil {
		t.Fatalf("seed host: %v", err)
	}
	inv := store.Inventory{
		Identity: store.Identity{Hostname: "web01", HardwareUUID: "uuid-web01"},
		OS:       store.OSInfo{Name: "Ubuntu", Version: "24.04"},
		BIOS:     store.BIOSInfo{Vendor: "Dell", Version: "1.0"},
	}
	s1 := store.Snapshot{ID: store.NewID(), TenantID: tenant, HostID: h.ID, CollectedAt: at, Source: store.SourceAgent, Payload: inv}
	s2 := store.Snapshot{ID: store.NewID(), TenantID: tenant, HostID: h.ID, CollectedAt: at.Add(time.Hour), Source: store.SourceAgent, Payload: inv}
	if err := m.InsertSnapshot(ctx, s1); err != nil {
		t.Fatalf("seed snap1: %v", err)
	}
	if err := m.InsertSnapshot(ctx, s2); err != nil {
		t.Fatalf("seed snap2: %v", err)
	}
	if err := m.InsertChanges(ctx, []store.Change{{
		ID: store.NewID(), TenantID: tenant, HostID: h.ID, SnapshotID: s2.ID, PrevSnapshotID: s1.ID,
		Category: "bios", ChangeType: store.ChangeModified, ComponentKey: "bios", DetectedAt: at,
	}}); err != nil {
		t.Fatalf("seed change: %v", err)
	}
	// An agent with a sealed credential; it must never leak into a backup.
	if err := m.CreateAgent(ctx, store.Agent{
		ID: store.NewID(), TenantID: tenant, HostID: h.ID, CredentialSealed: []byte("TOP-SECRET-SEAL"),
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return h.ID, s2.ID
}

func TestExport_IncludeHistory(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	seedTenant(t, m, tenant)

	b, err := backup.New(m).Export(context.Background(), subjFor(tenant), true)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if b.SchemaVersion != backup.SchemaVersion {
		t.Errorf("schema = %d, want %d", b.SchemaVersion, backup.SchemaVersion)
	}
	if len(b.Hosts) != 1 {
		t.Errorf("hosts = %d, want 1", len(b.Hosts))
	}
	if len(b.Snapshots) != 2 {
		t.Errorf("snapshots = %d, want 2 (full history)", len(b.Snapshots))
	}
	if len(b.Changes) != 1 {
		t.Errorf("changes = %d, want 1", len(b.Changes))
	}
	if b.Hosts[0].Tags["env"] != "prod" {
		t.Errorf("host tags not exported: %+v", b.Hosts[0].Tags)
	}
}

func TestExport_LatestOnly(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	seedTenant(t, m, tenant)

	b, err := backup.New(m).Export(context.Background(), subjFor(tenant), false)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(b.Snapshots) != 1 {
		t.Errorf("snapshots = %d, want 1 (latest only)", len(b.Snapshots))
	}
	if len(b.Changes) != 0 {
		t.Errorf("changes = %d, want 0 without history", len(b.Changes))
	}
}

func TestExport_NeverLeaksAgentSecret(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	seedTenant(t, m, tenant)

	b, err := backup.New(m).Export(context.Background(), subjFor(tenant), true)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	raw, _ := json.Marshal(b)
	if strings.Contains(string(raw), "TOP-SECRET-SEAL") {
		t.Errorf("backup leaked agent credential: %s", raw)
	}
	if strings.Contains(strings.ToLower(string(raw)), "credential") {
		t.Errorf("backup JSON references a credential field: %s", raw)
	}
}

func TestImport_PreservesIdsRoundTrip(t *testing.T) {
	m := memstore.New()
	src := store.NewID()
	hostID, snapID := seedTenant(t, m, src)

	b, err := backup.New(m).Export(context.Background(), subjFor(src), true)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Import into a fresh tenant.
	dst := store.NewID()
	res, err := backup.New(m).Import(context.Background(), subjFor(dst), b, backup.ModeSkip)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.HostsImported != 1 || res.SnapshotsImported != 2 || res.ChangesImported != 1 {
		t.Errorf("import result = %+v", res)
	}

	// Ids preserved: the same host/snapshot ids exist under the new tenant.
	if _, err := m.GetHost(context.Background(), dst, hostID); err != nil {
		t.Errorf("host id not preserved on import: %v", err)
	}
	if _, err := m.GetSnapshot(context.Background(), dst, snapID); err != nil {
		t.Errorf("snapshot id not preserved on import: %v", err)
	}
}

func TestImport_SkipVsOverwrite(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	seedTenant(t, m, tenant)
	b, _ := backup.New(m).Export(context.Background(), subjFor(tenant), true)

	// Re-import into the SAME tenant: skip should skip existing host/snapshots.
	skip, err := backup.New(m).Import(context.Background(), subjFor(tenant), b, backup.ModeSkip)
	if err != nil {
		t.Fatalf("Import skip: %v", err)
	}
	if skip.HostsSkipped != 1 || skip.SnapshotsSkipped != 2 {
		t.Errorf("skip result = %+v, want 1 host / 2 snapshots skipped", skip)
	}

	// Overwrite should re-import them.
	over, err := backup.New(m).Import(context.Background(), subjFor(tenant), b, backup.ModeOverwrite)
	if err != nil {
		t.Fatalf("Import overwrite: %v", err)
	}
	if over.HostsImported != 1 || over.SnapshotsImported != 2 {
		t.Errorf("overwrite result = %+v, want 1 host / 2 snapshots imported", over)
	}
}

func TestImport_BadSchema(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	_, err := backup.New(m).Import(context.Background(), subjFor(tenant), backup.Backup{SchemaVersion: 999}, backup.ModeSkip)
	if !errors.Is(err, backup.ErrBadSchema) {
		t.Fatalf("err = %v, want backup.ErrBadSchema", err)
	}
}

func TestExport_Forbidden(t *testing.T) {
	m := memstore.New()
	_, err := backup.New(m).Export(context.Background(), authz.Subjects{}, false)
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("err = %v, want authz.ErrForbidden", err)
	}
}
