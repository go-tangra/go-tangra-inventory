package store

import (
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
