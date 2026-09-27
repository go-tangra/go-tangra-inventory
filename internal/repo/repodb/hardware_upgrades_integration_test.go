//go:build integration

package repodb_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestHardwareUpgradesMigration: a populated 0005 database migrates to 0006;
// new columns get their defaults; CHECKs, the one-active-upgrade index and
// RLS hold (feature 023, data-model §1.3).
func TestHardwareUpgradesMigration(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()

	migrateTo(t, adminDSN, 5)
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	hostA, agentA, agentB := store.NewID(), store.NewID(), store.NewID()
	mustExec(t, admin, `INSERT INTO inventory_hosts (id, tenant_id, hostname, identity_key, status) VALUES ($1, $2, 'h', 'hostname', 'active')`, hostA, tenantA)
	mustExec(t, admin, `INSERT INTO inventory_agents (id, tenant_id, host_id, agent_version) VALUES ($1, $2, $3, '4.3.1')`, agentA, tenantA, hostA)
	mustExec(t, admin, `INSERT INTO inventory_agents (id, tenant_id, agent_version) VALUES ($1, $2, '4.3.1')`, agentB, tenantB)
	snap := store.NewID()
	mustExec(t, admin, `INSERT INTO inventory_memory_modules (id, tenant_id, host_id, snapshot_id, device_locator) VALUES ($1, $2, $3, $4, 'DIMM_A1')`, store.NewID(), tenantA, hostA, snap)
	mustExec(t, admin, `INSERT INTO inventory_disks (id, tenant_id, host_id, snapshot_id, model) VALUES ($1, $2, $3, $4, 'm')`, store.NewID(), tenantA, hostA, snap)
	mustExec(t, admin, `INSERT INTO inventory_processors (id, tenant_id, host_id, snapshot_id) VALUES ($1, $2, $3, $4)`, store.NewID(), tenantA, hostA, snap)

	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatalf("migrate 0006: %v", err)
	}

	// Defaults on existing rows.
	var populated bool
	var typeDetail, diskName, family, os, arch, installType string
	var removable bool
	var caps []string
	var seen sql.NullTime
	if err := admin.QueryRowContext(ctx, "SELECT populated, type_detail FROM inventory_memory_modules WHERE host_id=$1", hostA).Scan(&populated, &typeDetail); err != nil || !populated || typeDetail != "" {
		t.Fatalf("module defaults = %v %q %v", populated, typeDetail, err)
	}
	if err := admin.QueryRowContext(ctx, "SELECT name, removable FROM inventory_disks WHERE host_id=$1", hostA).Scan(&diskName, &removable); err != nil || diskName != "" || removable {
		t.Fatalf("disk defaults = %q %v %v", diskName, removable, err)
	}
	if err := admin.QueryRowContext(ctx, "SELECT family FROM inventory_processors WHERE host_id=$1", hostA).Scan(&family); err != nil || family != "" {
		t.Fatalf("processor defaults = %q %v", family, err)
	}
	row := admin.QueryRowContext(ctx, "SELECT os, arch, install_type, capabilities, platform_seen_at FROM inventory_agents WHERE id=$1", agentA)
	var capsRaw string
	if err := row.Scan(&os, &arch, &installType, &capsRaw, &seen); err != nil || os != "" || arch != "" || installType != "" || capsRaw != "{}" || seen.Valid {
		t.Fatalf("agent defaults = %q %q %q %q %v %v", os, arch, installType, capsRaw, seen, err)
	}
	_ = caps

	// CHECK constraints.
	mustFail(t, admin, "agent os", `UPDATE inventory_agents SET os='darwin' WHERE id=$1`, agentA)
	mustFail(t, admin, "agent arch", `UPDATE inventory_agents SET arch='386' WHERE id=$1`, agentA)
	mustFail(t, admin, "agent install_type", `UPDATE inventory_agents SET install_type='snap' WHERE id=$1`, agentA)
	mustExec(t, admin, `UPDATE inventory_agents SET os='linux', arch='amd64', install_type='deb', capabilities='{upgrade.v1}' WHERE id=$1`, agentA)

	sig := strings.Repeat("s", 64)
	sha := strings.Repeat("a", 64)
	for name, q := range map[string]string{
		"release version":  `INSERT INTO inventory_agent_releases (version, manifest, signature, key_id, manifest_sha256, source) VALUES ('v4.4', 'm', $1, 'k', $2, 'bundled')`,
		"release sig len":  `INSERT INTO inventory_agent_releases (version, manifest, signature, key_id, manifest_sha256, source) VALUES ('4.4.0', 'm', substr($1, 1, 5)::bytea, 'k', $2, 'bundled')`,
		"release key id":   `INSERT INTO inventory_agent_releases (version, manifest, signature, key_id, manifest_sha256, source) VALUES ('4.4.0', 'm', $1, 'Bad Key', $2, 'bundled')`,
		"release sha":      `INSERT INTO inventory_agent_releases (version, manifest, signature, key_id, manifest_sha256, source) VALUES ('4.4.0', 'm', $1, 'k', upper($2), 'bundled')`,
		"release source":   `INSERT INTO inventory_agent_releases (version, manifest, signature, key_id, manifest_sha256, source) VALUES ('4.4.0', 'm', $1, 'k', $2, 'github')`,
		"release manifest": `INSERT INTO inventory_agent_releases (version, manifest, signature, key_id, manifest_sha256, source) VALUES ('4.4.0', decode(repeat('00', 65537), 'hex'), $1, 'k', $2, 'bundled')`,
	} {
		mustFail(t, admin, name, q, sig, sha)
	}
	mustExec(t, admin, `INSERT INTO inventory_agent_releases (version, manifest, signature, key_id, manifest_sha256, source) VALUES ('4.4.0', 'm', $1, 'tangra-dev', $2, 'bundled')`, sig, sha)
	for name, q := range map[string]string{
		"artifact file":  `INSERT INTO inventory_agent_artifacts (version, os, arch, install_type, file, size, sha256) VALUES ('4.4.0', 'linux', 'amd64', 'deb', '../x.deb', 1, $1)`,
		"artifact size0": `INSERT INTO inventory_agent_artifacts (version, os, arch, install_type, file, size, sha256) VALUES ('4.4.0', 'linux', 'amd64', 'deb', 'a.deb', 0, $1)`,
		"artifact size":  `INSERT INTO inventory_agent_artifacts (version, os, arch, install_type, file, size, sha256) VALUES ('4.4.0', 'linux', 'amd64', 'deb', 'a.deb', 157286401, $1)`,
		"artifact sha":   `INSERT INTO inventory_agent_artifacts (version, os, arch, install_type, file, size, sha256) VALUES ('4.4.0', 'linux', 'amd64', 'deb', 'a.deb', 1, substr($1, 1, 10))`,
		"artifact os":    `INSERT INTO inventory_agent_artifacts (version, os, arch, install_type, file, size, sha256) VALUES ('4.4.0', 'darwin', 'amd64', 'binary', 'a', 1, $1)`,
	} {
		mustFail(t, admin, name, q, sha)
	}
	mustExec(t, admin, `INSERT INTO inventory_agent_artifacts (version, os, arch, install_type, file, size, sha256) VALUES ('4.4.0', 'linux', 'amd64', 'deb', 'a_4.4.0_amd64.deb', 1, $1)`, sha)
	mustFail(t, admin, "empty chunk", `INSERT INTO inventory_agent_artifact_chunks (version, os, arch, install_type, seq, data) VALUES ('4.4.0', 'linux', 'amd64', 'deb', 0, ''::bytea)`)
	mustFail(t, admin, "oversized chunk", `INSERT INTO inventory_agent_artifact_chunks (version, os, arch, install_type, seq, data) VALUES ('4.4.0', 'linux', 'amd64', 'deb', 0, decode(repeat('00', 1048577), 'hex'))`)
	mustFail(t, admin, "negative seq", `INSERT INTO inventory_agent_artifact_chunks (version, os, arch, install_type, seq, data) VALUES ('4.4.0', 'linux', 'amd64', 'deb', -1, 'x')`)
	mustExec(t, admin, `INSERT INTO inventory_agent_artifact_chunks (version, os, arch, install_type, seq, data) VALUES ('4.4.0', 'linux', 'amd64', 'deb', 0, 'x')`)

	up := func(id, tenant, agent, state, origin, reason string) error {
		_, err := admin.ExecContext(ctx, `INSERT INTO inventory_agent_upgrades (id, tenant_id, agent_id, target_version, state, origin, reason, expires_at)
			VALUES ($1, $2, $3, '4.4.0', $4, $5, $6, now() + interval '7 days')`, id, tenant, agent, state, origin, reason)
		return err
	}
	for name, e := range map[string]error{
		"state waiting":    up(store.NewID(), tenantA, agentA, "waiting", "user", ""),
		"origin admin":     up(store.NewID(), tenantA, agentA, "pending", "admin", ""),
		"free-text reason": up(store.NewID(), tenantA, agentA, "failed", "user", "Bad Reason!"),
	} {
		var pg *pgconn.PgError
		if !errors.As(e, &pg) || pg.Code != "23514" {
			t.Fatalf("CHECK on upgrade %s: %v", name, e)
		}
	}
	u1 := store.NewID()
	if err := up(u1, tenantA, agentA, "pending", "user", ""); err != nil {
		t.Fatal(err)
	}
	if up(store.NewID(), tenantA, agentA, "downloading", "policy", "") == nil {
		t.Fatal("two active upgrades for one agent")
	}
	if err := up(store.NewID(), tenantA, agentA, "failed", "user", "checksum_mismatch"); err != nil {
		t.Fatalf("terminal requests are unbounded: %v", err)
	}
	if err := up(store.NewID(), tenantB, agentB, "pending", "agent", ""); err != nil {
		t.Fatal(err)
	}

	for name, q := range map[string]string{
		"window pattern": `INSERT INTO inventory_agent_upgrade_policy (tenant_id, window_start) VALUES ($1, '24:00')`,
		"window minutes": `INSERT INTO inventory_agent_upgrade_policy (tenant_id, window_end) VALUES ($1, '2:60')`,
		"timezone":       `INSERT INTO inventory_agent_upgrade_policy (tenant_id, timezone) VALUES ($1, '')`,
		"max low":        `INSERT INTO inventory_agent_upgrade_policy (tenant_id, max_concurrent) VALUES ($1, 0)`,
		"max high":       `INSERT INTO inventory_agent_upgrade_policy (tenant_id, max_concurrent) VALUES ($1, 101)`,
	} {
		mustFail(t, admin, name, q, tenantA)
	}
	mustExec(t, admin, `INSERT INTO inventory_agent_upgrade_policy (tenant_id) VALUES ($1)`, tenantA)
	mustExec(t, admin, `INSERT INTO inventory_agent_upgrade_policy (tenant_id, enabled) VALUES ($1, true)`, tenantB)
	var enabled bool
	var ws, we, tz string
	var maxc int
	if err := admin.QueryRowContext(ctx, "SELECT enabled, window_start, window_end, timezone, max_concurrent FROM inventory_agent_upgrade_policy WHERE tenant_id=$1", tenantA).
		Scan(&enabled, &ws, &we, &tz, &maxc); err != nil || enabled || ws != "02:00" || we != "04:00" || tz != "UTC" || maxc != 5 {
		t.Fatalf("policy defaults = %v %s %s %s %d %v", enabled, ws, we, tz, maxc, err)
	}

	// RLS as the application role.
	appConn, err := pgx.Connect(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = appConn.Close(ctx) }()
	count := func(scope, table string) int {
		t.Helper()
		tx, err := appConn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if scope == "system" {
			_, _ = tx.Exec(ctx, "SELECT set_config('app.system', 'on', true)")
			_, _ = tx.Exec(ctx, "SELECT set_config('app.tenant_id', '00000000-0000-0000-0000-000000000000', true)")
		} else {
			_, _ = tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", scope)
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("count %s as %s: %v", table, scope, err)
		}
		return n
	}
	if n := count(tenantA, "inventory_agent_upgrades"); n != 2 {
		t.Fatalf("tenant A upgrades = %d", n)
	}
	if n := count(tenantB, "inventory_agent_upgrades"); n != 1 {
		t.Fatalf("tenant B upgrades = %d (RLS)", n)
	}
	if n := count("system", "inventory_agent_upgrades"); n != 3 {
		t.Fatalf("system upgrades = %d", n)
	}
	if n := count(tenantB, "inventory_agent_upgrade_policy"); n != 1 {
		t.Fatalf("tenant B policy rows = %d (RLS)", n)
	}
	if n := count("system", "inventory_agent_upgrade_policy"); n != 2 {
		t.Fatalf("system policy rows = %d", n)
	}
	for _, tbl := range []string{"inventory_agent_releases", "inventory_agent_artifacts", "inventory_agent_artifact_chunks"} {
		if n := count(tenantB, tbl); n != 1 {
			t.Fatalf("%s readable in tenant scope = %d (global table)", tbl, n)
		}
	}
	// Writing another tenant's upgrade row through RLS is refused.
	tx, _ := appConn.Begin(ctx)
	_, _ = tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantB)
	ct, err := tx.Exec(ctx, "UPDATE inventory_agent_upgrades SET state='cancelled' WHERE id=$1", u1)
	if err != nil || ct.RowsAffected() != 0 {
		t.Fatalf("RLS breach on update: %v %v", ct, err)
	}
	_ = tx.Rollback(ctx)
	// Cascade: deleting the release removes artifacts and chunks.
	mustExec(t, admin, "DELETE FROM inventory_agent_releases WHERE version='4.4.0'")
	var left int
	_ = admin.QueryRowContext(ctx, "SELECT count(*) FROM inventory_agent_artifact_chunks").Scan(&left)
	if left != 0 {
		t.Fatalf("chunks left after release delete = %d", left)
	}
}

// TestComponentHardwareColumns: insertComponents writes the 023 columns.
func TestComponentHardwareColumns(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, appDSN, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := repodb.New(st)
	h, err := db.ResolveHost(ctx, tenantA, store.Host{Hostname: "hw"})
	if err != nil {
		t.Fatal(err)
	}
	inv := store.Inventory{
		HardwareSchema: store.HardwareSchemaCurrent,
		Processors:     []store.Processor{{SocketDesignation: "CPU1", Family: "Xeon"}},
		Memory: store.MemoryInfo{Modules: []store.MemoryModule{
			{DeviceLocator: "A1", Populated: true, CapacityBytes: 16 << 30, TypeDetail: []string{"Synchronous", "Registered (Buffered)"}},
			{DeviceLocator: "B1"},
		}},
		Disks: []store.Disk{{Name: "sdb", Removable: true, SizeBytes: 1 << 30}, {Name: "nvme0n1", SizeBytes: 2 << 30}},
	}
	legacy := store.Inventory{Memory: store.MemoryInfo{Modules: []store.MemoryModule{{DeviceLocator: "L1", CapacityBytes: 8 << 30}}}}
	s1, s2 := store.NewID(), store.NewID()
	for id, p := range map[string]store.Inventory{s1: inv, s2: legacy} {
		if err := db.InsertSnapshot(ctx, store.Snapshot{ID: id, TenantID: tenantA, HostID: h.ID, CollectedAt: time.Now().UTC(), Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	var family string
	if err := admin.QueryRowContext(ctx, "SELECT family FROM inventory_processors WHERE snapshot_id=$1", s1).Scan(&family); err != nil || family != "Xeon" {
		t.Fatalf("family = %q %v", family, err)
	}
	mods := map[string][2]string{}
	rows, err := admin.QueryContext(ctx, "SELECT device_locator, populated::text, type_detail FROM inventory_memory_modules WHERE host_id=$1", h.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var loc, pop, td string
		if err := rows.Scan(&loc, &pop, &td); err != nil {
			t.Fatal(err)
		}
		mods[loc] = [2]string{pop, td}
	}
	_ = rows.Close()
	if mods["A1"] != [2]string{"true", "Synchronous, Registered (Buffered)"} || mods["B1"] != [2]string{"false", ""} {
		t.Fatalf("modules = %v", mods)
	}
	if mods["L1"][0] != "true" {
		t.Fatalf("legacy modules (agents < 023 report populated slots only) must be stored as populated: %v", mods["L1"])
	}
	var name string
	var removable bool
	if err := admin.QueryRowContext(ctx, "SELECT name, removable FROM inventory_disks WHERE snapshot_id=$1 AND removable", s1).Scan(&name, &removable); err != nil || name != "sdb" {
		t.Fatalf("removable disk = %q %v %v", name, removable, err)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// mustFail requires the statement to be refused by a CHECK constraint
// (SQLSTATE 23514) or a unique index (23505), not by any other error.
func mustFail(t *testing.T, db *sql.DB, name, q string, args ...any) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), q, args...)
	var pg *pgconn.PgError
	if err == nil {
		t.Fatalf("CHECK accepted %s", name)
	}
	if !errors.As(err, &pg) || (pg.Code != "23514" && pg.Code != "23505") {
		t.Fatalf("%s refused for another reason: %v", name, err)
	}
}
