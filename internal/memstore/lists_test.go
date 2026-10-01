package memstore

import (
	"fmt"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// The snapshot keyset continues after the cursor in (collected_at, id) order
// even when ids and collection times disagree; the paged variant windows the
// same order without payloads.
func TestSnapshotKeysetAndPage(t *testing.T) {
	m := New()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	offsets := []int{5, 1, 6, 1, 3, 0, 4}
	for i, o := range offsets {
		s := store.Snapshot{ID: fmt.Sprintf("s%02d", i), TenantID: tenant, HostID: "h", CollectedAt: base.Add(time.Duration(o) * time.Hour),
			Payload: store.Inventory{Programs: []store.Program{{Name: "p"}}}}
		if err := m.InsertSnapshot(ctx(), s); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for range 10 {
		page, err := m.ListSnapshotsForHost(ctx(), tenant, "h", 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, s := range page {
			if seen[s.ID] {
				t.Fatalf("%s twice", s.ID)
			}
			seen[s.ID] = true
		}
		cursor = page[len(page)-1].ID
	}
	if len(seen) != len(offsets) {
		t.Fatalf("keyset saw %d of %d", len(seen), len(offsets))
	}
	if page, _ := m.ListSnapshotsForHost(ctx(), tenant, "h", 0, "zz"); len(page) != len(offsets) {
		t.Fatalf("unknown cursor fallback = %d", len(page))
	}

	items, total, applied, err := m.ListSnapshotsPage(ctx(), tenant, "h", listquery.Request{Page: 3, PageSize: 3})
	if err != nil || total != len(offsets) || applied.Page != 3 || len(items) != 1 || items[0].ID != "s05" || len(items[0].Payload.Programs) != 0 {
		t.Fatalf("page = %+v %d %+v %v", items, total, applied, err)
	}
	m.FailNext("ListSnapshotsPage")
	if _, _, _, err := m.ListSnapshotsPage(ctx(), tenant, "h", listquery.Request{}); err == nil {
		t.Fatal("injected error swallowed")
	}
	m.FailNext("ListChangesPage")
	if _, _, _, err := m.ListChangesPage(ctx(), tenant, "h", listquery.Request{}); err == nil {
		t.Fatal("injected error swallowed")
	}
}
