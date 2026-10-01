package store

import (
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra/v4/listquery"
)

func TestListSpecsValid(t *testing.T) {
	for name, s := range map[string]listquery.Spec{
		"hosts": HostList, "snapshots": SnapshotList, "changes": ChangeList,
		"fleet": FleetList, "auto-enroll keys": AutoEnrollKeyList,
	} {
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestListRequestDefaults(t *testing.T) {
	r := ListRequest(listquery.Request{}, HostList)
	if r.Page != 1 || r.PageSize != listquery.DefaultPageSize || r.Sort != "hostname" || r.Order != listquery.Asc {
		t.Fatalf("defaults: %+v", r)
	}
	r = ListRequest(listquery.Request{Sort: "last_seen"}, HostList)
	if r.Order != listquery.Desc {
		t.Fatalf("last_seen default direction: %+v", r)
	}
	// An invalid hand-built request falls back to the defaults.
	r = ListRequest(listquery.Request{Sort: "payload", PageSize: 5}, HostList)
	if r.Sort != "hostname" || r.PageSize != listquery.DefaultPageSize {
		t.Fatalf("fallback: %+v", r)
	}
}

// The SQL-backed sorts are over NOT NULL columns, so ORDER BY carries no
// NULLS LAST and the (tenant_id, <expr>, id) indexes serve both directions.
func TestListOrderByIndexFriendly(t *testing.T) {
	for name, s := range map[string]listquery.Spec{"hosts": HostList, "snapshots": SnapshotList, "changes": ChangeList} {
		for field := range s.Fields {
			for _, dir := range []listquery.Dir{listquery.Asc, listquery.Desc} {
				ob := listquery.Request{Sort: field, Order: dir}.OrderBy(s)
				if strings.Contains(ob, "NULLS") {
					t.Errorf("%s %s %s: %q", name, field, dir, ob)
				}
			}
		}
	}
	if got := (listquery.Request{Sort: "created_at", Order: listquery.Desc}).OrderBy(HostList); got != "created_at DESC, id DESC" {
		t.Fatalf("created_at desc = %q", got)
	}
}
