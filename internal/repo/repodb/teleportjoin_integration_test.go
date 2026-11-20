//go:build integration

package repodb_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repotest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestTeleportJoinMigration: a populated 0012 database migrates to 0013;
// CHECKs, the unique enrollment id, the one-active-item index and RLS hold;
// no column can hold join material (feature 038).
func TestTeleportJoinMigration(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()

	migrateTo(t, adminDSN, 12)
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	hostA := store.NewID()
	mustExec(t, admin, `INSERT INTO inventory_hosts (id, tenant_id, hostname, identity_key, status) VALUES ($1, $2, 'node-1', 'hostname', 'active')`, hostA, tenantA)
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatalf("migrate 0013: %v", err)
	}

	// Schema assertion: nothing can hold a token, CA pins or labels.
	rows, err := admin.QueryContext(ctx, `SELECT column_name, data_type FROM information_schema.columns
		WHERE table_name = 'inventory_teleport_join_items'`)
	if err != nil {
		t.Fatal(err)
	}
	cols := 0
	for rows.Next() {
		var col, typ string
		if err := rows.Scan(&col, &typ); err != nil {
			t.Fatal(err)
		}
		cols++
		lc := strings.ToLower(col)
		for _, bad := range []string{"token", "secret", "password", "pin", "label", "proxy", "material", "data"} {
			if strings.Contains(lc, bad) {
				t.Errorf("column %s could hold join material", col)
			}
		}
		if typ == "bytea" || typ == "jsonb" {
			t.Errorf("column %s (%s) could hold join material", col, typ)
		}
	}
	_ = rows.Close()
	if cols != 18 {
		t.Fatalf("columns = %d, want 18", cols)
	}

	ins := `INSERT INTO inventory_teleport_join_items (id, tenant_id, source, enrollment_id, host_id, cluster_id, state, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now() + interval '1 day')`
	i1, e1, c1 := store.NewID(), store.NewID(), store.NewID()
	mustExec(t, admin, ins, i1, tenantA, "teleport", e1, hostA, c1, "pending")
	for name, args := range map[string][]any{
		"source":          {store.NewID(), tenantA, "Teleport!", store.NewID(), hostA, store.NewID(), "failed"},
		"state":           {store.NewID(), tenantA, "teleport", store.NewID(), hostA, store.NewID(), "installed"},
		"enrollment dupe": {store.NewID(), tenantA, "teleport", e1, hostA, store.NewID(), "failed"},
		"second active":   {store.NewID(), tenantA, "teleport", store.NewID(), hostA, c1, "installing"},
	} {
		mustFail(t, admin, name, ins, args...)
	}
	// Terminal items accumulate; another cluster on the host is fine; the
	// same enrollment id from another source or tenant is fine.
	mustExec(t, admin, ins, store.NewID(), tenantA, "teleport", store.NewID(), hostA, c1, "joined")
	mustExec(t, admin, ins, store.NewID(), tenantA, "teleport", store.NewID(), hostA, store.NewID(), "pending")
	mustExec(t, admin, ins, store.NewID(), tenantA, "other", e1, store.NewID(), c1, "pending")
	mustExec(t, admin, ins, store.NewID(), tenantB, "teleport", e1, store.NewID(), c1, "pending")
	for name, q := range map[string]string{
		"reason":         `UPDATE inventory_teleport_join_items SET reason='Bad Reason' WHERE id=$1`,
		"node uuid":      `UPDATE inventory_teleport_join_items SET node_host_uuid='not-a-uuid' WHERE id=$1`,
		"node uuid case": `UPDATE inventory_teleport_join_items SET node_host_uuid='5B0C7A4E-2F61-4C1E-9D3A-0E4F6A7B8C9D' WHERE id=$1`,
		"fetches":        `UPDATE inventory_teleport_join_items SET fetches=6 WHERE id=$1`,
		"attempt":        `UPDATE inventory_teleport_join_items SET attempt=0 WHERE id=$1`,
	} {
		mustFail(t, admin, name, q, i1)
	}
	mustExec(t, admin, `UPDATE inventory_teleport_join_items SET reason='invalid_material', fetches=5,
		node_host_uuid='5b0c7a4e-2f61-4c1e-9d3a-0e4f6a7b8c9d' WHERE id=$1`, i1)

	// RLS as the application role.
	appConn, err := pgx.Connect(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = appConn.Close(ctx) }()
	count := func(scope string) int {
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
		var c int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM inventory_teleport_join_items").Scan(&c); err != nil {
			t.Fatalf("count as %s: %v", scope, err)
		}
		return c
	}
	if a, b, s := count(tenantA), count(tenantB), count("system"); a != 4 || b != 1 || s != 5 {
		t.Fatalf("RLS counts A=%d B=%d system=%d", a, b, s)
	}
	tx, _ := appConn.Begin(ctx)
	_, _ = tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantB)
	ct, err := tx.Exec(ctx, "UPDATE inventory_teleport_join_items SET state='failed' WHERE id=$1", i1)
	if err != nil || ct.RowsAffected() != 0 {
		t.Fatalf("RLS breach on update: %v %v", ct, err)
	}
	_ = tx.Rollback(ctx)
}

// TestTeleportJoinRepoContract runs the shared storage contract (also run
// against memstore) on the database store.
func TestTeleportJoinRepoContract(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, appDSN, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	db := repodb.New(st)
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	open := func(t *testing.T) repo.Store {
		mustExec(t, admin, "TRUNCATE inventory_teleport_join_items")
		mustExec(t, admin, "DELETE FROM inventory_audit_events")
		return db
	}
	audits := func(t *testing.T, tenantID, action, subjectID string) int {
		var c int
		if err := admin.QueryRowContext(ctx, "SELECT count(*) FROM inventory_audit_events WHERE tenant_id=$1 AND action=$2 AND subject_id=$3",
			tenantID, action, subjectID).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	repotest.TeleportJoinContract(t, open, audits)
}
