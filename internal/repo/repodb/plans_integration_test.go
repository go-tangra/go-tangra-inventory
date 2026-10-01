//go:build integration

package repodb_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestListSortPlans checks (EXPLAIN) that every SQL-backed list sort, in both
// directions, is served by an index rather than a full sort (go-tangra specs/
// 032-server-side-tables perf.md): with enable_sort off the planner still
// emits a Sort node when no index provides the order, so its absence proves
// one does. Change detected_at keeps an Incremental Sort on the id
// tie-breaker over the (tenant_id, host_id, detected_at DESC) index.
func TestListSortPlans(t *testing.T) {
	adminDSN, _ := startDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	const host = "0190f7c2-6a3e-7c1a-9b2e-000000000001"
	for _, q := range []string{
		`INSERT INTO inventory_hosts (id, tenant_id, hostname, machine_id, os_name, manufacturer, status, last_seen, created_at)
		 SELECT gen_random_uuid(), t, 'host-'||i, 'm-'||i, (ARRAY['Linux','windows','macOS'])[i%3+1], (ARRAY['Dell','HP',''])[i%3+1],
		        (ARRAY['active','stale','retired'])[i%3+1], now() - (i%500)*interval '1 minute', now() - i*interval '1 second'
		 FROM generate_series(1, 6000) i, (VALUES ('` + tenantA + `'::uuid), ('` + tenantB + `'::uuid)) v(t)`,
		`INSERT INTO inventory_hosts (id, tenant_id, hostname, machine_id) VALUES ('` + host + `', '` + tenantA + `', 'h', 'mh')`,
		`INSERT INTO inventory_changes (id, tenant_id, host_id, snapshot_id, detected_at, change_type)
		 SELECT gen_random_uuid(), '` + tenantA + `', '` + host + `', gen_random_uuid(), now() - i*interval '1 second', (ARRAY['added','removed','modified'])[i%3+1]
		 FROM generate_series(1, 6000) i`,
		"ANALYZE inventory_hosts", "ANALYZE inventory_changes",
		"SET enable_sort = off",
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	plan := func(q string) string {
		rows, err := conn.Query(ctx, "EXPLAIN "+q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			b.WriteString(line + "\n")
		}
		return b.String()
	}
	fullSort := func(p string) bool {
		for _, l := range strings.Split(p, "\n") {
			l = strings.TrimLeft(l, " ->")
			if strings.HasPrefix(l, "Sort  (") { // a full Sort node; Incremental Sort is fine
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		spec  listquery.Spec
		table string
		where string
	}{
		{store.HostList, "inventory_hosts", "tenant_id='" + tenantA + "'"},
		{store.ChangeList, "inventory_changes", "tenant_id='" + tenantA + "' AND host_id='" + host + "'"},
	} {
		for field := range c.spec.Fields {
			for _, dir := range dirs {
				ob := listquery.Request{Sort: field, Order: dir}.OrderBy(c.spec)
				p := plan(fmt.Sprintf("SELECT * FROM %s WHERE %s ORDER BY %s LIMIT 25", c.table, c.where, ob))
				if fullSort(p) || !strings.Contains(p, "Index") {
					t.Errorf("%s %s %s: not index-backed:\n%s", c.table, field, dir, p)
				}
			}
		}
	}
}
