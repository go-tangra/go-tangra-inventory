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

// TestModuleDeliveryMigration (T003): a populated 0011 database migrates to
// 0012; CHECKs, the unique delivery id, the one-active-item index and RLS
// hold; no column can hold bundle material (feature 037, data-model.md).
func TestModuleDeliveryMigration(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()

	migrateTo(t, adminDSN, 11)
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	hostA := store.NewID()
	mustExec(t, admin, `INSERT INTO inventory_hosts (id, tenant_id, hostname, identity_key, status) VALUES ($1, $2, 'gw-1', 'hostname', 'active')`, hostA, tenantA)
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatalf("migrate 0012: %v", err)
	}

	// Schema assertion: nothing can hold bundle bytes, a token or a password.
	rows, err := admin.QueryContext(ctx, `SELECT column_name, data_type FROM information_schema.columns
		WHERE table_name = 'inventory_module_delivery_items'`)
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
		for _, bad := range []string{"bundle", "token", "password", "secret", "data", "zip"} {
			if strings.Contains(lc, bad) {
				t.Errorf("column %s could hold bundle material", col)
			}
		}
		if typ == "bytea" || typ == "jsonb" {
			t.Errorf("column %s (%s) could hold bundle material", col, typ)
		}
	}
	_ = rows.Close()
	if cols != 19 {
		t.Fatalf("columns = %d, want 19", cols)
	}

	ins := `INSERT INTO inventory_module_delivery_items (id, tenant_id, source, delivery_id, host_id, module, version, state, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now() + interval '1 day')`
	i1, d1 := store.NewID(), store.NewID()
	mustExec(t, admin, ins, i1, tenantA, "gateway", d1, hostA, "sms-gw", "1.2.0", "pending")
	for name, args := range map[string][]any{
		"source":         {store.NewID(), tenantA, "Gateway!", store.NewID(), hostA, "x", "1.0.0", "failed"},
		"module upper":   {store.NewID(), tenantA, "gateway", store.NewID(), hostA, "SMS", "1.0.0", "failed"},
		"module dots":    {store.NewID(), tenantA, "gateway", store.NewID(), hostA, "../x", "1.0.0", "failed"},
		"module leading": {store.NewID(), tenantA, "gateway", store.NewID(), hostA, "-x", "1.0.0", "failed"},
		"module long":    {store.NewID(), tenantA, "gateway", store.NewID(), hostA, strings.Repeat("a", 64), "1.0.0", "failed"},
		"version empty":  {store.NewID(), tenantA, "gateway", store.NewID(), hostA, "x", "", "failed"},
		"version long":   {store.NewID(), tenantA, "gateway", store.NewID(), hostA, "x", strings.Repeat("1", 65), "failed"},
		"state":          {store.NewID(), tenantA, "gateway", store.NewID(), hostA, "x", "1.0.0", "cancelled"},
		"delivery dupe":  {store.NewID(), tenantA, "gateway", d1, hostA, "x", "1.0.0", "failed"},
		"second active":  {store.NewID(), tenantA, "gateway", store.NewID(), hostA, "sms-gw", "1.0.0", "delivered"},
	} {
		mustFail(t, admin, name, ins, args...)
	}
	// A digit-led module name (gateway catalogue pattern) is fine.
	mustExec(t, admin, ins, store.NewID(), tenantA, "gateway", store.NewID(), hostA, "3cx", "1.0.0", "pending")
	// Terminal items accumulate; the same delivery id from another source or tenant is fine.
	mustExec(t, admin, ins, store.NewID(), tenantA, "gateway", store.NewID(), hostA, "sms-gw", "1.0.0", "failed")
	mustExec(t, admin, ins, store.NewID(), tenantA, "other", d1, store.NewID(), "sms-gw", "1.0.0", "pending")
	mustExec(t, admin, ins, store.NewID(), tenantB, "gateway", d1, store.NewID(), "sms-gw", "1.0.0", "pending")
	for name, q := range map[string]string{
		"reason":    `UPDATE inventory_module_delivery_items SET reason='Bad Reason' WHERE id=$1`,
		"hook code": `UPDATE inventory_module_delivery_items SET hook_exit_code=257 WHERE id=$1`,
		"fetches":   `UPDATE inventory_module_delivery_items SET fetches=6 WHERE id=$1`,
		"attempt":   `UPDATE inventory_module_delivery_items SET attempt=0 WHERE id=$1`,
	} {
		mustFail(t, admin, name, q, i1)
	}
	mustExec(t, admin, `UPDATE inventory_module_delivery_items SET reason='already_installed', hook_exit_code=256, fetches=5 WHERE id=$1`, i1)

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
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM inventory_module_delivery_items").Scan(&c); err != nil {
			t.Fatalf("count as %s: %v", scope, err)
		}
		return c
	}
	if a, b, s := count(tenantA), count(tenantB), count("system"); a != 4 || b != 1 || s != 5 {
		t.Fatalf("RLS counts A=%d B=%d system=%d", a, b, s)
	}
	tx, _ := appConn.Begin(ctx)
	_, _ = tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantB)
	ct, err := tx.Exec(ctx, "UPDATE inventory_module_delivery_items SET state='failed' WHERE id=$1", i1)
	if err != nil || ct.RowsAffected() != 0 {
		t.Fatalf("RLS breach on update: %v %v", ct, err)
	}
	_ = tx.Rollback(ctx)
}

// TestModuleDeliveryRepoContract runs the shared storage contract (also run
// against memstore) on the database store.
func TestModuleDeliveryRepoContract(t *testing.T) {
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
		mustExec(t, admin, "TRUNCATE inventory_module_delivery_items")
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
	repotest.ModuleDeliveryContract(t, open, audits)
}
