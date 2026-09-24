//go:build integration

// Package repodb integration test: exercises the TimescaleDB-backed inventory
// store and its repo wrappers against a real database (testcontainers). It covers
// migration, host identity precedence + partial-unique, snapshot ingest with
// normalized child rows + GetLatest, ListHosts filters, single-use enrollment
// tokens, tenant statistics, and per-tenant row-level security. Run with:
//
//	go test -tags integration ./internal/repo/repodb/
//
// It skips cleanly when Docker/testcontainers is unavailable.
package repodb_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const (
	tenantA = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	tenantB = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
)

func startDB(t *testing.T) (adminDSN, appDSN string) {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "timescale/timescaledb:latest-pg16", ExposedPorts: []string{"5432/tcp"},
			Env:        map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "inventory"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
		}, Started: true,
	})
	if err != nil {
		t.Skipf("testcontainers unavailable: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	adminDSN = "postgres://postgres:test@" + host + ":" + port.Port() + "/inventory?sslmode=disable"
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Exec(ctx, "CREATE ROLE inventory_app LOGIN PASSWORD 'app' NOBYPASSRLS")
	_ = conn.Close(ctx)
	appDSN = "postgres://inventory_app:app@" + host + ":" + port.Port() + "/inventory?sslmode=disable"
	return
}

func openRepo(t *testing.T) repo.Store {
	t.Helper()
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.Migrate(ctx, adminDSN); err != nil { // idempotent
		t.Fatalf("migrate idempotent: %v", err)
	}
	st, err := store.Open(ctx, appDSN, 4)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(st.Close)
	return repodb.New(st)
}

func TestInventoryRepo(t *testing.T) {
	db := openRepo(t)
	ctx := context.Background()

	// --- ResolveHost: identity precedence + upsert (hardware_uuid wins) ---
	h1, err := db.ResolveHost(ctx, tenantA, store.Host{
		Hostname: "alpha", MachineID: "mid-1", HardwareUUID: "hw-1",
		Manufacturer: "Dell", Model: "OptiPlex", OSName: "Windows", OSVersion: "11",
	})
	if err != nil || h1.ID == "" || h1.IdentityKey != store.IdentityHardwareUUID {
		t.Fatalf("resolve host 1: %+v %v", h1, err)
	}
	// Same hardware_uuid but changed hostname resolves to the SAME host (upsert).
	h1b, err := db.ResolveHost(ctx, tenantA, store.Host{
		Hostname: "alpha-renamed", MachineID: "mid-1", HardwareUUID: "hw-1", AssignedUser: "bob",
	})
	if err != nil || h1b.ID != h1.ID || h1b.Hostname != "alpha-renamed" || h1b.AssignedUser != "bob" {
		t.Fatalf("resolve upsert should reuse host: %+v (orig %s) %v", h1b, h1.ID, err)
	}
	// A host with no hardware_uuid resolves by machine_id.
	h2, err := db.ResolveHost(ctx, tenantA, store.Host{Hostname: "beta", MachineID: "mid-2"})
	if err != nil || h2.IdentityKey != store.IdentityMachineID {
		t.Fatalf("resolve host 2 by machine_id: %+v %v", h2, err)
	}
	// A host with neither resolves by hostname.
	h3, err := db.ResolveHost(ctx, tenantA, store.Host{Hostname: "gamma"})
	if err != nil || h3.IdentityKey != store.IdentityHostname {
		t.Fatalf("resolve host 3 by hostname: %+v %v", h3, err)
	}
	if _, err := db.GetHost(ctx, tenantA, h1.ID); err != nil {
		t.Fatalf("get host: %v", err)
	}
	if _, err := db.GetHostByIdentity(ctx, tenantA, store.Identity{HardwareUUID: "hw-1"}); err != nil {
		t.Fatalf("get host by identity: %v", err)
	}

	// --- InsertSnapshot: child rows + GetLatest + host summary advance ---
	now := time.Now().UTC()
	snap1 := store.Snapshot{
		ID: store.NewID(), TenantID: tenantA, HostID: h1.ID, CollectedAt: now.Add(-time.Hour),
		Source: store.SourceAgent, OSName: "Windows", OSVersion: "11", Manufacturer: "Dell",
		Payload: store.Inventory{
			Memory:     store.MemoryInfo{TotalPhysicalBytes: 16 << 30, Modules: []store.MemoryModule{{DeviceLocator: "DIMM0", CapacityBytes: 8 << 30, SerialNumber: "m-1"}}},
			Processors: []store.Processor{{Manufacturer: "Intel", CoreCount: 8, SerialNumber: "cpu-1"}},
			Disks:      []store.Disk{{Model: "SSD", Serial: "d-1", SizeBytes: 512 << 30}},
			Networks:   []store.NetIface{{Name: "eth0", MAC: "aa:bb", IPAddresses: []string{"10.0.0.1"}, DNS: []string{"1.1.1.1"}}},
			Programs:   []store.Program{{Name: "Chrome", Version: "120"}, {Name: "Slack", Version: "4"}},
			Services:   []store.Service{{Name: "sshd", State: "running"}},
			Monitors:   []store.Monitor{{Manufacturer: "LG", SerialNumber: "mon-1"}},
		},
	}
	if err := db.InsertSnapshot(ctx, snap1); err != nil {
		t.Fatalf("insert snapshot 1: %v", err)
	}
	snap2 := snap1
	snap2.ID = store.NewID()
	snap2.CollectedAt = now
	snap2.Payload.Processors = []store.Processor{{Manufacturer: "Intel", CoreCount: 16, SerialNumber: "cpu-1b"}}
	if err := db.InsertSnapshot(ctx, snap2); err != nil {
		t.Fatalf("insert snapshot 2: %v", err)
	}
	latest, err := db.GetLatestForHost(ctx, tenantA, h1.ID)
	if err != nil || latest.ID != snap2.ID {
		t.Fatalf("get latest: %s want %s %v", latest.ID, snap2.ID, err)
	}
	if list, err := db.ListSnapshotsForHost(ctx, tenantA, h1.ID, 10, ""); err != nil || len(list) != 2 {
		t.Fatalf("list snapshots: %d %v", len(list), err)
	}
	// Verify child rows were written for snap1.
	if got, err := db.GetSnapshot(ctx, tenantA, snap1.ID); err != nil || len(got.Payload.Processors) != 1 {
		t.Fatalf("get snapshot payload: %v %v", got.Payload.Processors, err)
	}
	assertCount(t, db, tenantA, snap1.ID, "inventory_processors", 1)
	assertCount(t, db, tenantA, snap1.ID, "inventory_memory_modules", 1)
	assertCount(t, db, tenantA, snap1.ID, "inventory_disks", 1)
	assertCount(t, db, tenantA, snap1.ID, "inventory_network_interfaces", 1)
	assertCount(t, db, tenantA, snap1.ID, "inventory_software", 2)
	assertCount(t, db, tenantA, snap1.ID, "inventory_services", 1)
	assertCount(t, db, tenantA, snap1.ID, "inventory_monitors", 1)

	// host summary advanced to snap2
	if hh, _ := db.GetHost(ctx, tenantA, h1.ID); hh.LastSnapshotID != snap2.ID {
		t.Fatalf("host last_snapshot_id not advanced: %s", hh.LastSnapshotID)
	}

	// --- ListHosts filters ---
	_ = db.SetHostTags(ctx, tenantA, h1.ID, map[string]string{"env": "prod", "team": "it"})
	if l, err := db.ListHosts(ctx, tenantA, store.HostFilter{Hostname: "alpha"}); err != nil || len(l) != 1 {
		t.Fatalf("filter hostname: %d %v", len(l), err)
	}
	if l, err := db.ListHosts(ctx, tenantA, store.HostFilter{Manufacturer: "Dell"}); err != nil || len(l) != 1 {
		t.Fatalf("filter manufacturer: %d %v", len(l), err)
	}
	if l, err := db.ListHosts(ctx, tenantA, store.HostFilter{Tag: "env=prod"}); err != nil || len(l) != 1 {
		t.Fatalf("filter tag kv: %d %v", len(l), err)
	}
	if l, err := db.ListHosts(ctx, tenantA, store.HostFilter{Tag: "team"}); err != nil || len(l) != 1 {
		t.Fatalf("filter tag key: %d %v", len(l), err)
	}
	if l, err := db.ListHosts(ctx, tenantA, store.HostFilter{Status: store.HostActive}); err != nil || len(l) != 3 {
		t.Fatalf("filter status active: %d %v", len(l), err)
	}

	// --- Changes ---
	if err := db.InsertChanges(ctx, []store.Change{{
		ID: store.NewID(), TenantID: tenantA, HostID: h1.ID, SnapshotID: snap2.ID, PrevSnapshotID: snap1.ID,
		Category: "processor", ChangeType: store.ChangeModified, ComponentKey: "cpu-1",
		Before: `{"cores":8}`, After: `{"cores":16}`,
	}}); err != nil {
		t.Fatalf("insert changes: %v", err)
	}
	if cs, err := db.ListChangesForHost(ctx, tenantA, h1.ID, 10); err != nil || len(cs) != 1 || cs[0].After == "" || cs[0].PrevSnapshotID != snap1.ID {
		t.Fatalf("list changes for host: %+v %v", cs, err)
	}
	if cs, err := db.ListChangesForSnapshot(ctx, tenantA, snap2.ID); err != nil || len(cs) != 1 {
		t.Fatalf("list changes for snapshot: %d %v", len(cs), err)
	}

	// --- Agents (create / get / ingest-auth lookup / touch / revoke) ---
	agentID := store.NewID()
	if err := db.CreateAgent(ctx, store.Agent{
		ID: agentID, TenantID: tenantA, HostID: h1.ID, CredentialSealed: []byte("sealed"),
		AgentVersion: "1.0", IdentityHint: "alpha",
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if a, err := db.GetAgentByID(ctx, agentID); err != nil || a.TenantID != tenantA || a.HostID != h1.ID {
		t.Fatalf("get agent by id (ingest auth): %+v %v", a, err)
	}
	if err := db.TouchAgent(ctx, agentID, "1.1", h1.ID, time.Now()); err != nil {
		t.Fatalf("touch agent: %v", err)
	}
	if a, _ := db.GetAgent(ctx, tenantA, agentID); a.AgentVersion != "1.1" {
		t.Fatalf("touch did not update version: %+v", a)
	}
	if err := db.RevokeAgent(ctx, tenantA, agentID); err != nil {
		t.Fatalf("revoke agent: %v", err)
	}

	// --- Enrollment token single-use ---
	tokID := store.NewID()
	if err := db.CreateEnrollmentToken(ctx, store.EnrollmentToken{
		ID: tokID, TenantID: tenantA, TokenHash: "hash-1", ExpiresAt: now.Add(time.Hour), CreatedBy: "op",
	}); err != nil {
		t.Fatalf("create token: %v", err)
	}
	if tok, err := db.ConsumeEnrollmentToken(ctx, "hash-1", time.Now()); err != nil || tok.ID != tokID {
		t.Fatalf("consume token: %+v %v", tok, err)
	}
	// Second consume must fail (single-use).
	if _, err := db.ConsumeEnrollmentToken(ctx, "hash-1", time.Now()); err != repo.ErrConflict {
		t.Fatalf("second consume should conflict, got %v", err)
	}
	// Unknown token -> not found.
	if _, err := db.ConsumeEnrollmentToken(ctx, "nope", time.Now()); err != repo.ErrNotFound {
		t.Fatalf("unknown token should be not found, got %v", err)
	}

	// --- TenantStats ---
	st, err := db.TenantStats(ctx, tenantA, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("tenant stats: %v", err)
	}
	if st.HostsTotal != 3 {
		t.Fatalf("stats hosts total = %d want 3", st.HostsTotal)
	}
	if st.SnapshotsTotal != 2 {
		t.Fatalf("stats snapshots total = %d want 2", st.SnapshotsTotal)
	}
	if st.TotalMemoryBytes != 16<<30 {
		t.Fatalf("stats memory = %d want %d", st.TotalMemoryBytes, uint64(16)<<30)
	}
	if st.TotalCPUCores != 16 { // latest snapshot (snap2) has 16
		t.Fatalf("stats cpu cores = %d want 16", st.TotalCPUCores)
	}
	if st.HostsByManufacturer["Dell"] != 1 {
		t.Fatalf("stats manufacturer: %+v", st.HostsByManufacturer)
	}
	if st.TopPrograms["Chrome"] != 1 {
		t.Fatalf("stats top programs: %+v", st.TopPrograms)
	}

	// --- Audit + TenantIDs (system scope) ---
	if err := db.AppendAudit(ctx, store.AuditRow{
		TenantID: tenantA, ActorKind: "user", ActorID: "op", Action: "snapshot_ingested",
		SubjectKind: "snapshot", SubjectID: snap1.ID, Outcome: "ok", Detail: map[string]any{"k": "v"},
	}); err != nil {
		t.Fatalf("append audit: %v", err)
	}
	if ids, err := db.TenantIDs(ctx); err != nil || len(ids) == 0 {
		t.Fatalf("tenant ids: %v %v", ids, err)
	}

	// --- MarkStaleHosts + PurgeSnapshots (system scope) ---
	if n, err := db.MarkStaleHosts(ctx, now.Add(24*time.Hour)); err != nil || n < 1 {
		t.Fatalf("mark stale: %d %v", n, err)
	}
	if n, err := db.PurgeSnapshots(ctx, now.Add(time.Minute)); err != nil || n != 1 {
		// snap1 is older-than and not the latest; snap2 is the latest so kept.
		t.Fatalf("purge snapshots = %d want 1: %v", n, err)
	}
	if _, err := db.GetSnapshot(ctx, tenantA, snap1.ID); err != repo.ErrNotFound {
		t.Fatalf("purged snapshot should be gone, got %v", err)
	}

	// --- RLS isolation: tenant B sees nothing of tenant A ---
	if _, err := db.GetHost(ctx, tenantB, h1.ID); err != repo.ErrNotFound {
		t.Fatalf("RLS breach: tenant B read tenant A host: %v", err)
	}
	if l, err := db.ListHosts(ctx, tenantB, store.HostFilter{}); err != nil || len(l) != 0 {
		t.Fatalf("RLS breach: tenant B listed tenant A hosts: %d %v", len(l), err)
	}
	// Tenant B can create its own host with a colliding identity (separate scope).
	if _, err := db.ResolveHost(ctx, tenantB, store.Host{Hostname: "alpha", HardwareUUID: "hw-1"}); err != nil {
		t.Fatalf("tenant B resolve own host: %v", err)
	}

	// --- DeleteHost cascade ---
	if err := db.DeleteHost(ctx, tenantA, h1.ID); err != nil {
		t.Fatalf("delete host: %v", err)
	}
	if _, err := db.GetHost(ctx, tenantA, h1.ID); err != repo.ErrNotFound {
		t.Fatalf("host should be deleted, got %v", err)
	}
}

// assertCount checks a component child table has n rows for the snapshot, using a
// direct tenant-scoped connection via the repo's underlying store.
func assertCount(t *testing.T, db repo.Store, tenantID, snapshotID, table string, want int) {
	t.Helper()
	dbx, ok := db.(*repodb.DB)
	if !ok {
		return
	}
	ctx := context.Background()
	var n int
	err := dbx.St.Tx(ctx, store.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE tenant_id=$1 AND snapshot_id=$2", tenantID, snapshotID).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if n != want {
		t.Fatalf("%s rows for snapshot = %d want %d", table, n, want)
	}
}
