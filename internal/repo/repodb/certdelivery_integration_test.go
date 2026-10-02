//go:build integration

package repodb_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repotest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

var certTables = []string{"inventory_cert_deliveries", "inventory_cert_delivery_items", "inventory_host_certificates"}

// TestCertDeliveryMigration (T010): a populated 0009 database migrates to
// 0010; CHECKs, the unique idempotency key, the one-active-item index and
// RLS hold; no column can hold material (feature 033, data-model §1.3).
func TestCertDeliveryMigration(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()

	migrateTo(t, adminDSN, 9)
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	hostA, hostB, agentA := store.NewID(), store.NewID(), store.NewID()
	mustExec(t, admin, `INSERT INTO inventory_hosts (id, tenant_id, hostname, identity_key, status) VALUES ($1, $2, 'web-1', 'hostname', 'active')`, hostA, tenantA)
	mustExec(t, admin, `INSERT INTO inventory_hosts (id, tenant_id, hostname, identity_key, status) VALUES ($1, $2, 'web-2', 'hostname', 'active')`, hostB, tenantB)
	mustExec(t, admin, `INSERT INTO inventory_agents (id, tenant_id, host_id, agent_version, capabilities) VALUES ($1, $2, $3, '4.6.3', '{upgrade.v1}')`, agentA, tenantA, hostA)

	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatalf("migrate 0010: %v", err)
	}
	var n int
	if err := admin.QueryRowContext(ctx, "SELECT count(*) FROM inventory_hosts").Scan(&n); err != nil || n != 2 {
		t.Fatalf("hosts after migration = %d %v", n, err)
	}

	// Schema assertion: nothing in the three tables can hold material.
	rows, err := admin.QueryContext(ctx, `SELECT table_name, column_name, data_type FROM information_schema.columns
		WHERE table_name = ANY($1)`, certTables)
	if err != nil {
		t.Fatal(err)
	}
	cols := 0
	for rows.Next() {
		var tbl, col, typ string
		if err := rows.Scan(&tbl, &col, &typ); err != nil {
			t.Fatal(err)
		}
		cols++
		lc := strings.ToLower(col)
		if strings.Contains(lc, "key_pem") || strings.Contains(lc, "private") || strings.Contains(lc, "pem") || typ == "bytea" {
			t.Errorf("%s.%s (%s) could hold certificate material", tbl, col, typ)
		}
	}
	_ = rows.Close()
	if cols < 40 {
		t.Fatalf("only %d columns found: tables missing?", cols)
	}

	// A valid delivery and item.
	d1, i1 := store.NewID(), store.NewID()
	insD := `INSERT INTO inventory_cert_deliveries (id, tenant_id, source, idempotency_key, trigger, certificate_id, name, key_policy, requested_by, expires_at, host_ids, host_tags)
		VALUES ($1, $2, $3, $4, $5, 'cert-1', $6, $7, 'spiffe://example.org/svc/deployer', now() + interval '1 day', $8::uuid[], $9::text[])`
	mustExec(t, admin, insD, d1, tenantA, "deployer", "job-1", "manual", "www", "require", []string{hostA}, []string{"role=web"})
	insI := `INSERT INTO inventory_cert_delivery_items (id, tenant_id, delivery_id, host_id, agent_id, name, certificate_id, state)
		VALUES ($1, $2, $3, $4, $5, 'www', 'cert-1', $6)`
	mustExec(t, admin, insI, i1, tenantA, d1, hostA, agentA, "pending")

	// CHECKs on deliveries.
	tooMany := make([]string, 1001)
	for i := range tooMany {
		tooMany[i] = store.NewID()
	}
	tags17 := make([]string, 17)
	for i := range tags17 {
		tags17[i] = fmt.Sprintf("t%d", i)
	}
	for name, args := range map[string][]any{
		"name ..":          {store.NewID(), tenantA, "deployer", "k1", "manual", "a..b", "require", []string{}, []string{}},
		"name traversal":   {store.NewID(), tenantA, "deployer", "k1", "manual", "../x", "require", []string{}, []string{}},
		"name dot":         {store.NewID(), tenantA, "deployer", "k1", "manual", ".hidden", "require", []string{}, []string{}},
		"name slash":       {store.NewID(), tenantA, "deployer", "k1", "manual", "a/b", "require", []string{}, []string{}},
		"name long":        {store.NewID(), tenantA, "deployer", "k1", "manual", strings.Repeat("a", 65), "require", []string{}, []string{}},
		"trigger":          {store.NewID(), tenantA, "deployer", "k1", "cron", "www", "require", []string{}, []string{}},
		"key_policy":       {store.NewID(), tenantA, "deployer", "k1", "manual", "www", "maybe", []string{}, []string{}},
		"source":           {store.NewID(), tenantA, "Deployer!", "k1", "manual", "www", "require", []string{}, []string{}},
		"empty key":        {store.NewID(), tenantA, "deployer", "", "manual", "www", "require", []string{}, []string{}},
		"> 1000 host ids":  {store.NewID(), tenantA, "deployer", "k1", "manual", "www", "require", tooMany, []string{}},
		"> 16 host tags":   {store.NewID(), tenantA, "deployer", "k1", "manual", "www", "require", []string{}, tags17},
		"idempotency dupe": {store.NewID(), tenantA, "deployer", "job-1", "manual", "www", "require", []string{}, []string{}},
	} {
		mustFail(t, admin, name, insD, args...)
	}
	// The same idempotency key in another tenant or from another source is fine.
	mustExec(t, admin, insD, store.NewID(), tenantB, "deployer", "job-1", "manual", "www", "require", []string{}, []string{})
	mustExec(t, admin, insD, store.NewID(), tenantA, "other", "job-1", "manual", "www", "require", []string{}, []string{})

	// CHECKs on items.
	for name, q := range map[string]string{
		"item state":       `UPDATE inventory_cert_delivery_items SET state='done' WHERE id=$1`,
		"item reason":      `UPDATE inventory_cert_delivery_items SET reason='Bad Reason' WHERE id=$1`,
		"item fingerprint": `UPDATE inventory_cert_delivery_items SET fingerprint_sha256=upper(repeat('a', 64)) WHERE id=$1`,
		"item fp short":    `UPDATE inventory_cert_delivery_items SET fingerprint_sha256='abc' WHERE id=$1`,
		"item serial":      `UPDATE inventory_cert_delivery_items SET serial='zz' WHERE id=$1`,
		"item detail":      `UPDATE inventory_cert_delivery_items SET detail=repeat('d', 257) WHERE id=$1`,
		"item attempts":    `UPDATE inventory_cert_delivery_items SET attempts=6 WHERE id=$1`,
		"item fetches":     `UPDATE inventory_cert_delivery_items SET fetches=6 WHERE id=$1`,
		"item hook code":   `UPDATE inventory_cert_delivery_items SET hook_exit_code=257 WHERE id=$1`,
		"item name":        `UPDATE inventory_cert_delivery_items SET name='a..b' WHERE id=$1`,
	} {
		mustFail(t, admin, name, q, i1)
	}
	mustExec(t, admin, `UPDATE inventory_cert_delivery_items SET fingerprint_sha256=repeat('a', 64), serial='4F:3a', detail=repeat('d', 256), hook_exit_code=256 WHERE id=$1`, i1)

	// One active item per (tenant, host, name); many terminal ones.
	d2 := store.NewID()
	mustExec(t, admin, insD, d2, tenantA, "deployer", "job-2", "manual", "www", "require", []string{}, []string{})
	mustFail(t, admin, "second active item", insI, store.NewID(), tenantA, d2, hostA, agentA, "delivered")
	mustExec(t, admin, insI, store.NewID(), tenantA, d2, hostB, nil, "pending") // another host
	d3 := store.NewID()
	mustExec(t, admin, insD, d3, tenantA, "deployer", "job-3", "manual", "www", "require", []string{}, []string{})
	mustExec(t, admin, insI, store.NewID(), tenantA, d3, hostA, agentA, "installed")
	d4 := store.NewID()
	mustExec(t, admin, insD, d4, tenantA, "deployer", "job-4", "manual", "www", "require", []string{}, []string{})
	mustExec(t, admin, insI, store.NewID(), tenantA, d4, hostA, agentA, "failed")
	mustFail(t, admin, "item twice per delivery", insI, store.NewID(), tenantA, d4, hostA, agentA, "failed")

	// Host certificates.
	insH := `INSERT INTO inventory_host_certificates (tenant_id, host_id, name, certificate_id, state, last_item_id) VALUES ($1, $2, $3, 'cert-1', $4, $5)`
	mustExec(t, admin, insH, tenantA, hostA, "www", "installed", i1)
	mustFail(t, admin, "host cert name", insH, tenantA, hostA, "../www", "installed", i1)
	mustFail(t, admin, "host cert state", insH, tenantA, hostA, "api", "pending", i1)
	mustFail(t, admin, "host cert duplicate", insH, tenantA, hostA, "www", "installed", i1)
	mustFail(t, admin, "host cert cn", `INSERT INTO inventory_host_certificates (tenant_id, host_id, name, certificate_id, state, last_item_id, common_name)
		VALUES ($1, $2, 'cn', 'c', 'installed', $3, repeat('c', 257))`, tenantA, hostA, i1)
	mustExec(t, admin, insH, tenantB, hostB, "www", "installed", store.NewID())

	// Cascade: deleting a delivery removes its items.
	mustExec(t, admin, "DELETE FROM inventory_cert_deliveries WHERE id=$1", d4)
	if err := admin.QueryRowContext(ctx, "SELECT count(*) FROM inventory_cert_delivery_items WHERE delivery_id=$1", d4).Scan(&n); err != nil || n != 0 {
		t.Fatalf("items left after delivery delete = %d %v", n, err)
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
		var c int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&c); err != nil {
			t.Fatalf("count %s as %s: %v", table, scope, err)
		}
		return c
	}
	for _, c := range []struct {
		table   string
		a, b, s int
	}{
		{"inventory_cert_deliveries", 4, 1, 5},
		{"inventory_cert_delivery_items", 3, 0, 3},
		{"inventory_host_certificates", 1, 1, 2},
	} {
		if got := count(tenantA, c.table); got != c.a {
			t.Errorf("%s tenant A = %d, want %d", c.table, got, c.a)
		}
		if got := count(tenantB, c.table); got != c.b {
			t.Errorf("%s tenant B = %d, want %d (RLS)", c.table, got, c.b)
		}
		if got := count("system", c.table); got != c.s {
			t.Errorf("%s system = %d, want %d", c.table, got, c.s)
		}
	}
	// Writing another tenant's rows through RLS is refused.
	tx, _ := appConn.Begin(ctx)
	_, _ = tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantB)
	ct, err := tx.Exec(ctx, "UPDATE inventory_cert_delivery_items SET state='cancelled' WHERE id=$1", i1)
	if err != nil || ct.RowsAffected() != 0 {
		t.Fatalf("RLS breach on update: %v %v", ct, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO inventory_cert_deliveries (id, tenant_id, source, idempotency_key, trigger, certificate_id, name, key_policy, requested_by, expires_at)
		VALUES ($1, $2, 'deployer', 'x', 'manual', 'c', 'www', 'require', 'r', now())`, store.NewID(), tenantA); err == nil {
		t.Fatal("RLS admitted an insert for another tenant")
	}
	_ = tx.Rollback(ctx)
}

// TestCertDeliveryRepoContract runs the shared storage contract (T015,
// also run against memstore) on the database store.
func TestCertDeliveryRepoContract(t *testing.T) {
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
		mustExec(t, admin, "TRUNCATE inventory_cert_delivery_items, inventory_cert_deliveries, inventory_host_certificates")
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
	repotest.CertDeliveryContract(t, open, audits)
}

// TestCertDeliveryConcurrentSupersede (T055): concurrent creates of active
// items for the same host and name leave exactly one active item; the
// losers either superseded the winner's predecessor or got ErrConflict.
func TestCertDeliveryConcurrentSupersede(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, appDSN, 16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	db := repodb.New(st)
	host := store.NewID()
	now := time.Now().UTC().Truncate(time.Second)
	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for k := 0; k < n; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			d := store.CertDelivery{ID: store.NewID(), TenantID: tenantA, Source: "deployer", IdempotencyKey: fmt.Sprintf("job-%d", k),
				Trigger: store.TriggerManual, CertificateID: "cert-1", Name: "www", KeyPolicy: store.KeyPolicyRequire,
				RequestedBy: "spiffe://example.org/svc/deployer", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
			i := store.CertDeliveryItem{ID: store.NewID(), TenantID: tenantA, DeliveryID: d.ID, HostID: host, Name: "www", CertificateID: "cert-1",
				State: store.DeliveryPending, Attempts: 1, CreatedAt: now, UpdatedAt: now}
			_, err := db.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d, Items: []store.CertDeliveryItem{i}})
			errs <- err
		}(k)
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, repo.ErrConflict):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	active, err := db.ListActiveCertItemsByName(ctx, tenantA, "www")
	if err != nil || len(active) != 1 || ok < 1 {
		t.Fatalf("active = %d (created %d) %v", len(active), ok, err)
	}
}
