//go:build integration

package repodb_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// List contract (go-tangra specs/032-server-side-tables) against TimescaleDB:
// count → clamp → ORDER BY <Spec field> NULLS LAST, id → LIMIT/OFFSET, every
// row exactly once per sort and direction, filters applied before the count,
// tenant isolation, and the unchanged keyset listings (gRPC ListHosts,
// snapshots, changes, host reports).

var dirs = []listquery.Dir{listquery.Asc, listquery.Desc}

func TestListHostsPage(t *testing.T) {
	db := openRepo(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	const n = 23
	for i := range n {
		os := []string{"Linux", "windows", "linux"}[i%3] // duplicates and case
		name := fmt.Sprintf("host-%02d", i)
		if i%4 == 0 {
			name = strings.ToUpper(name)
		}
		// last_seen repeats every 5 hosts so the tie-breaker decides.
		h := store.Host{Hostname: name, MachineID: fmt.Sprintf("m-%02d", i), OSName: os, Manufacturer: []string{"Dell", "HP", ""}[i%3],
			LastSeen: base.Add(-time.Duration(i%5) * time.Hour)}
		if _, err := db.ResolveHost(ctx, tenantA, h); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2 {
		if _, err := db.ResolveHost(ctx, tenantB, store.Host{Hostname: fmt.Sprintf("other-%d", i), MachineID: fmt.Sprintf("o-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	for sort := range store.HostList.Fields {
		for _, dir := range dirs {
			seen := map[string]bool{}
			var prev *store.Host
			for page := 1; page <= 5; page++ {
				items, total, applied, err := db.ListHostsPage(ctx, tenantA, store.HostFilter{}, listquery.Request{Page: page, PageSize: 5, Sort: sort, Order: dir})
				if err != nil || total != n || applied.Page != page {
					t.Fatalf("%s %s page %d: total %d applied %+v err %v", sort, dir, page, total, applied, err)
				}
				for i := range items {
					h := items[i]
					if seen[h.ID] {
						t.Fatalf("%s %s: host %s twice", sort, dir, h.Hostname)
					}
					seen[h.ID] = true
					if prev != nil && !ordered(*prev, h, sort, dir) {
						t.Fatalf("%s %s: %s before %s", sort, dir, prev.Hostname, h.Hostname)
					}
					prev = &items[i]
				}
			}
			if len(seen) != n {
				t.Fatalf("%s %s: %d of %d hosts", sort, dir, len(seen), n)
			}
		}
	}

	// A page beyond the end answers the last page.
	items, total, applied, err := db.ListHostsPage(ctx, tenantA, store.HostFilter{}, listquery.Request{Page: 99, PageSize: 10})
	if err != nil || total != n || applied.Page != 3 || len(items) != 3 {
		t.Fatalf("clamp: %d items total %d applied %+v %v", len(items), total, applied, err)
	}
	// Filters apply before the count (os_name exact, last_seen_from).
	_, total, _, err = db.ListHostsPage(ctx, tenantA, store.HostFilter{OSName: "linux"}, listquery.Request{PageSize: 1})
	if err != nil || total != 7 { // i%3 == 2 → 2,5,…,20
		t.Fatalf("os_name total %d %v", total, err)
	}
	from := base.Add(-90 * time.Minute) // i%5 in {0,1}
	items, total, _, err = db.ListHostsPage(ctx, tenantA, store.HostFilter{LastSeenFrom: &from}, listquery.Request{PageSize: 200, Sort: "last_seen"})
	if err != nil || total != 10 || len(items) != 10 {
		t.Fatalf("last_seen_from total %d (%d) %v", total, len(items), err)
	}
	for _, h := range items {
		if h.LastSeen.Before(from) {
			t.Fatalf("last_seen_from leaked %s %v", h.Hostname, h.LastSeen)
		}
	}
	// The hostname filter is a literal substring: LIKE wildcards match only
	// themselves (no host name contains % or _).
	for _, q := range []string{"%", "_", "host_0", `\`} {
		if _, total, _, err = db.ListHostsPage(ctx, tenantA, store.HostFilter{Hostname: q}, listquery.Request{}); err != nil || total != 0 {
			t.Fatalf("hostname %q total %d %v", q, total, err)
		}
	}
	if _, total, _, err = db.ListHostsPage(ctx, tenantA, store.HostFilter{Hostname: "ST-0"}, listquery.Request{}); err != nil || total != 10 {
		t.Fatalf("hostname ST-0 total %d %v", total, err) // host-00..09, any case
	}
	// Tenant isolation.
	if _, total, _, err = db.ListHostsPage(ctx, tenantB, store.HostFilter{}, listquery.Request{}); err != nil || total != 2 {
		t.Fatalf("tenant B total %d %v", total, err)
	}

	// gRPC ListHosts (asset invclient: Limit + CursorID, id DESC keyset) unchanged.
	seen := map[string]bool{}
	cursor, last := "", ""
	for {
		page, err := db.ListHosts(ctx, tenantA, store.HostFilter{Limit: 5, CursorID: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, h := range page {
			if seen[h.ID] || (last != "" && h.ID >= last) {
				t.Fatalf("keyset not strictly id DESC at %s", h.ID)
			}
			seen[h.ID], last = true, h.ID
		}
		cursor = page[len(page)-1].ID
	}
	if len(seen) != n {
		t.Fatalf("keyset walk saw %d of %d", len(seen), n)
	}

	// Host reports keep their own (report_changed_at, id) listing.
	if rows, err := db.ListHostReportRows(ctx, tenantA, store.ReportRowFilter{}); err != nil || len(rows) != n {
		t.Fatalf("host report rows %d %v", len(rows), err)
	}
}

// ordered reports whether a may precede b in the Spec order of sort.
func ordered(a, b store.Host, sort string, dir listquery.Dir) bool {
	var c int
	switch sort {
	case "hostname":
		c = strings.Compare(strings.ToLower(a.Hostname), strings.ToLower(b.Hostname))
	case "os_name":
		c = strings.Compare(strings.ToLower(a.OSName), strings.ToLower(b.OSName))
	case "manufacturer":
		c = strings.Compare(strings.ToLower(a.Manufacturer), strings.ToLower(b.Manufacturer))
	case "status":
		c = strings.Compare(a.Status, b.Status)
	case "last_seen":
		c = a.LastSeen.Compare(b.LastSeen)
	case "created_at":
		c = a.CreatedAt.Compare(b.CreatedAt)
	}
	if c == 0 {
		c = strings.Compare(a.ID, b.ID)
	}
	if dir == listquery.Desc {
		return c > 0
	}
	return c < 0
}

func TestListSnapshotsAndChangesPage(t *testing.T) {
	db := openRepo(t)
	ctx := context.Background()
	h, err := db.ResolveHost(ctx, tenantA, store.Host{Hostname: "snap", MachineID: "snap-1"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.ResolveHost(ctx, tenantB, store.Host{Hostname: "snap", MachineID: "snap-1"})
	if err != nil {
		t.Fatal(err)
	}
	// Ids increase with insertion while collected_at does not (agent clocks,
	// imports): the former id-cursor under a collected_at order skipped or
	// repeated rows.
	base := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	offsets := []int{5, 1, 6, 1, 3, 0, 4}
	var ids []string
	for _, o := range offsets {
		id := store.NewID()
		ids = append(ids, id)
		s := store.Snapshot{ID: id, TenantID: tenantA, HostID: h.ID, CollectedAt: base.Add(time.Duration(o) * time.Hour), Source: store.SourceAgent,
			Payload: store.Inventory{Programs: []store.Program{{Name: "p", Version: "1"}}}}
		if err := db.InsertSnapshot(ctx, s); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct, increasing v7 ids
	}
	if err := db.InsertSnapshot(ctx, store.Snapshot{ID: store.NewID(), TenantID: tenantB, HostID: other.ID, CollectedAt: base, Source: store.SourceAgent}); err != nil {
		t.Fatal(err)
	}

	// Legacy keyset (gRPC ListSnapshots / HTTP cursor): every snapshot once,
	// newest first.
	seen := map[string]bool{}
	var prev store.Snapshot
	cursor := ""
	for range 10 {
		page, err := db.ListSnapshotsForHost(ctx, tenantA, h.ID, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, s := range page {
			if seen[s.ID] {
				t.Fatalf("keyset repeated %s", s.ID)
			}
			if prev.ID != "" && (s.CollectedAt.After(prev.CollectedAt) || (s.CollectedAt.Equal(prev.CollectedAt) && s.ID > prev.ID)) {
				t.Fatalf("keyset order: %v after %v", s.CollectedAt, prev.CollectedAt)
			}
			seen[s.ID], prev = true, s
		}
		cursor = page[len(page)-1].ID
	}
	if len(seen) != len(offsets) {
		t.Fatalf("keyset walk saw %d of %d", len(seen), len(offsets))
	}
	// An unknown cursor falls back to the lower ids.
	if page, err := db.ListSnapshotsForHost(ctx, tenantA, h.ID, 0, "00000000-0000-7000-8000-000000000000"); err != nil || len(page) != 0 {
		t.Fatalf("unknown low cursor: %d %v", len(page), err)
	}
	if page, err := db.ListSnapshotsForHost(ctx, tenantA, h.ID, 0, "ffffffff-ffff-7fff-bfff-ffffffffffff"); err != nil || len(page) != len(offsets) {
		t.Fatalf("unknown high cursor: %d %v", len(page), err)
	}

	for _, dir := range dirs {
		seen := map[string]bool{}
		var prev store.Snapshot
		for page := 1; page <= 3; page++ {
			items, total, _, err := db.ListSnapshotsPage(ctx, tenantA, h.ID, listquery.Request{Page: page, PageSize: 3, Order: dir})
			if err != nil || total != len(offsets) {
				t.Fatalf("snapshots %s page %d: total %d %v", dir, page, total, err)
			}
			for _, s := range items {
				if seen[s.ID] || len(s.Payload.Programs) != 0 {
					t.Fatalf("snapshots %s: repeated or with payload %s", dir, s.ID)
				}
				if prev.ID != "" {
					c := s.CollectedAt.Compare(prev.CollectedAt)
					if c == 0 {
						c = strings.Compare(s.ID, prev.ID)
					}
					if (dir == listquery.Asc && c < 0) || (dir == listquery.Desc && c > 0) {
						t.Fatalf("snapshots %s order", dir)
					}
				}
				seen[s.ID], prev = true, s
			}
		}
		if len(seen) != len(offsets) {
			t.Fatalf("snapshots %s: %d of %d", dir, len(seen), len(offsets))
		}
	}
	if _, total, _, err := db.ListSnapshotsPage(ctx, tenantB, other.ID, listquery.Request{}); err != nil || total != 1 {
		t.Fatalf("tenant B snapshots %d %v", total, err)
	}
	if _, total, _, err := db.ListSnapshotsPage(ctx, tenantB, h.ID, listquery.Request{}); err != nil || total != 0 {
		t.Fatalf("cross-tenant snapshots %d %v", total, err)
	}

	// Changes: duplicate detected_at and kinds so the tie-breaker decides.
	var changes []store.Change
	kinds := []string{store.ChangeAdded, store.ChangeRemoved, store.ChangeModified}
	for i := range 9 {
		changes = append(changes, store.Change{ID: store.NewID(), TenantID: tenantA, HostID: h.ID, SnapshotID: ids[i%len(ids)],
			DetectedAt: base.Add(time.Duration(i%3) * time.Minute), Category: "software", ChangeType: kinds[i%3], ComponentKey: fmt.Sprintf("c%d", i)})
	}
	if err := db.InsertChanges(ctx, changes); err != nil {
		t.Fatal(err)
	}
	for sort := range store.ChangeList.Fields {
		for _, dir := range dirs {
			seen := map[string]bool{}
			for page := 1; page <= 3; page++ {
				items, total, _, err := db.ListChangesPage(ctx, tenantA, h.ID, listquery.Request{Page: page, PageSize: 4, Sort: sort, Order: dir})
				if err != nil || total != 9 {
					t.Fatalf("changes %s %s: total %d %v", sort, dir, total, err)
				}
				for _, c := range items {
					if seen[c.ID] {
						t.Fatalf("changes %s %s: %s twice", sort, dir, c.ID)
					}
					seen[c.ID] = true
				}
			}
			if len(seen) != 9 {
				t.Fatalf("changes %s %s: %d of 9", sort, dir, len(seen))
			}
		}
	}
	if cs, err := db.ListChangesForHost(ctx, tenantA, h.ID, 3); err != nil || len(cs) != 3 {
		t.Fatalf("changes limit: %d %v", len(cs), err)
	}
	if _, total, _, err := db.ListChangesPage(ctx, tenantB, h.ID, listquery.Request{}); err != nil || total != 0 {
		t.Fatalf("cross-tenant changes %d %v", total, err)
	}
}
