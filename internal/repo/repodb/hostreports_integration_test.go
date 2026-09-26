//go:build integration

package repodb_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// migrateTo applies the migrations up to version with goose directly (the
// embedded store.Migrate always goes to the latest version).
func migrateTo(t *testing.T, dsn string, version int64) {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*cfg)
	defer func() { _ = db.Close() }()
	goose.SetBaseFS(os.DirFS("../../store/migrations"))
	defer goose.SetBaseFS(nil)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", version); err != nil {
		t.Fatalf("migrate to %d: %v", version, err)
	}
}

func TestHostReportsMigrationAndRepo(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()

	// Database at 0004 with hosts in two tenants.
	migrateTo(t, adminDSN, 4)
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	hostA, hostB := store.NewID(), store.NewID()
	for _, row := range [][2]string{{hostA, tenantA}, {hostB, tenantB}} {
		if _, err := admin.ExecContext(ctx, `INSERT INTO inventory_hosts (id, tenant_id, hostname, identity_key, status)
			VALUES ($1, $2, 'legacy', 'hostname', 'active')`, row[0], row[1]); err != nil {
			t.Fatalf("seed 0004 host: %v", err)
		}
	}

	// 0005 applies on top (via the production migrator).
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatalf("migrate 0005: %v", err)
	}
	var digest string
	var changed sql.NullTime
	if err := admin.QueryRowContext(ctx, "SELECT report_digest, report_changed_at FROM inventory_hosts WHERE id=$1", hostA).Scan(&digest, &changed); err != nil {
		t.Fatal(err)
	}
	if digest != "" || changed.Valid {
		t.Fatalf("existing host defaults = %q %v", digest, changed)
	}
	for _, bad := range []string{"xyz", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if _, err := admin.ExecContext(ctx, "UPDATE inventory_hosts SET report_digest=$1 WHERE id=$2", bad, hostA); err == nil {
			t.Fatalf("CHECK accepted report_digest %q", bad)
		}
	}
	for _, idx := range []string{"hosts_report_changed", "hosts_report_changed_global"} {
		var n int
		if err := admin.QueryRowContext(ctx, "SELECT count(*) FROM pg_indexes WHERE tablename='inventory_hosts' AND indexname=$1", idx).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s missing (%d, %v)", idx, n, err)
		}
	}

	st, err := store.Open(ctx, appDSN, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := repodb.New(st)

	// Legacy hosts appear in a full listing only.
	rows, err := db.ListHostReportRows(ctx, tenantA, store.ReportRowFilter{})
	if err != nil || len(rows) != 1 || rows[0].ID != hostA || !rows[0].ReportChangedAt.IsZero() {
		t.Fatalf("legacy listing = %v %v", rows, err)
	}
	ids, _, err := db.ListReportTenants(ctx, time.Time{}, 0)
	if err != nil || len(ids) != 2 {
		t.Fatalf("system scope tenants = %v %v", ids, err)
	}

	d1 := strings.Repeat("a", 64)
	t1 := time.UnixMilli(1_700_000_000_123).UTC()
	if ok, err := db.SetReportDigest(ctx, tenantA, hostA, d1, t1); err != nil || !ok {
		t.Fatalf("set digest: %v %v", ok, err)
	}
	if ok, err := db.SetReportDigest(ctx, tenantA, hostA, d1, t1.Add(time.Hour)); err != nil || ok {
		t.Fatalf("same digest must not bump: %v %v", ok, err)
	}
	if _, err := db.SetReportDigest(ctx, tenantA, store.NewID(), d1, t1); err != repo.ErrNotFound {
		t.Fatalf("missing host: %v", err)
	}
	// RLS: tenant B cannot touch or list tenant A's host.
	if _, err := db.SetReportDigest(ctx, tenantB, hostA, strings.Repeat("b", 64), t1); err != repo.ErrNotFound {
		t.Fatalf("RLS breach on SetReportDigest: %v", err)
	}
	if rows, _ := db.ListHostReportRows(ctx, tenantB, store.ReportRowFilter{}); len(rows) != 1 || rows[0].ID != hostB {
		t.Fatalf("RLS breach on ListHostReportRows: %v", rows)
	}
	h, err := db.GetHost(ctx, tenantA, hostA)
	if err != nil || h.ReportDigest != d1 || !h.ReportChangedAt.Equal(t1) {
		t.Fatalf("host after set = %q %v %v", h.ReportDigest, h.ReportChangedAt, err)
	}

	// Watermarks.
	ids, maxAt, err := db.ListReportTenants(ctx, t1.Add(-time.Millisecond), 0)
	if err != nil || len(ids) != 1 || ids[0] != tenantA || !maxAt.Equal(t1) {
		t.Fatalf("since = %v %v %v", ids, maxAt, err)
	}
	ids, maxAt, _ = db.ListReportTenants(ctx, t1, 0)
	if len(ids) != 0 || !maxAt.Equal(t1) {
		t.Fatalf("nothing after the watermark: %v %v", ids, maxAt)
	}

	// Keyset paging over several hosts of tenant A.
	var created []string
	for i := 0; i < 3; i++ {
		hh, err := db.ResolveHost(ctx, tenantA, store.Host{Hostname: "k" + string(rune('a'+i))})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, hh.ID)
		if _, err := db.SetReportDigest(ctx, tenantA, hh.ID, strings.Repeat(string(rune('b'+i)), 64), t1.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	page1, _ := db.ListHostReportRows(ctx, tenantA, store.ReportRowFilter{ChangedSince: t1, Limit: 2})
	if len(page1) != 2 {
		t.Fatalf("page1 = %v", page1)
	}
	last := page1[1]
	page2, _ := db.ListHostReportRows(ctx, tenantA, store.ReportRowFilter{ChangedSince: t1, Limit: 2,
		After: &store.ReportCursor{ChangedAt: last.ReportChangedAt, ID: last.ID}})
	if len(page2) != 1 {
		t.Fatalf("page2 = %v", page2)
	}
	seen := map[string]bool{page1[0].ID: true, page1[1].ID: true, page2[0].ID: true}
	for _, id := range created {
		if !seen[id] {
			t.Fatalf("host %s missing from the paged listing", id)
		}
	}
	// Cursor at a legacy (NULL) row.
	all, _ := db.ListHostReportRows(ctx, tenantA, store.ReportRowFilter{After: &store.ReportCursor{ID: "00000000-0000-0000-0000-000000000000"}})
	if len(all) != 4 {
		t.Fatalf("after zero cursor = %d rows", len(all))
	}

	// Retire invalidates the digest and bumps the change time.
	if err := db.RetireHost(ctx, tenantA, hostA); err != nil {
		t.Fatal(err)
	}
	h, _ = db.GetHost(ctx, tenantA, hostA)
	if h.ReportDigest != "" || !h.ReportChangedAt.After(t1) || h.Status != store.HostRetired {
		t.Fatalf("retired = %q %v %s", h.ReportDigest, h.ReportChangedAt, h.Status)
	}
	if ids, _, _ = db.ListReportTenants(ctx, time.Time{}, 1); len(ids) != 1 {
		t.Fatalf("limit = %v", ids)
	}
}
