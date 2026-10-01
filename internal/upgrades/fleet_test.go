package upgrades

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// FleetPage sorts and windows the in-memory fleet view (list contract,
// store.FleetList): each agent exactly once per sort and direction.
func TestFleetPageWindow(t *testing.T) {
	f := newFixture(t)
	versions := []string{"4.10.0", "4.4.0", "4.9.1", "4.5.0", "4.4.0", "dev", "4.5.0-rc.1"}
	for i, v := range versions {
		id := fmt.Sprintf("a%d", i)
		f.enroll(t, id, v, deb, store.CapUpgradeV1)
		if i%2 == 0 { // every other agent has a host name
			if _, err := f.mem.ResolveHost(context.Background(), tenant, store.Host{ID: "h-" + id, Hostname: fmt.Sprintf("node-%d", 9-i)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.reg.online["a1"] = true

	for _, sort := range []string{"hostname", "version", "state", "last_seen"} {
		for _, order := range []listquery.Dir{listquery.Asc, listquery.Desc} {
			seen := map[string]bool{}
			for page := 1; page <= 4; page++ {
				pg, target, err := f.svc.FleetPage(context.Background(), tenant, FleetFilter{}, listquery.Request{Page: page, PageSize: 2, Sort: sort, Order: order})
				if err != nil || target != "4.5.0" {
					t.Fatalf("%s %s: %v %q", sort, order, err, target)
				}
				if pg.Total != len(versions) || pg.PageSize != 2 || pg.Sort != sort || pg.Order != order {
					t.Fatalf("%s %s page %d = %+v", sort, order, page, pg)
				}
				for _, e := range pg.Items {
					if seen[e.AgentID] {
						t.Fatalf("%s %s: %s twice", sort, order, e.AgentID)
					}
					seen[e.AgentID] = true
				}
			}
			if len(seen) != len(versions) {
				t.Fatalf("%s %s: %d agents", sort, order, len(seen))
			}
		}
	}

	// Semantic version order; a non-version sorts after every version and a
	// pre-release before its release.
	pg, _, _ := f.svc.FleetPage(context.Background(), tenant, FleetFilter{}, listquery.Request{PageSize: 10, Sort: "version"})
	var got []string
	for _, e := range pg.Items {
		got = append(got, e.Version)
	}
	want := []string{"4.4.0", "4.4.0", "4.5.0-rc.1", "4.5.0", "4.9.1", "4.10.0", "dev"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("version order = %v", got)
	}
	// Hostname order with agents without a host name last (both directions).
	for _, order := range []listquery.Dir{listquery.Asc, listquery.Desc} {
		pg, _, _ = f.svc.FleetPage(context.Background(), tenant, FleetFilter{}, listquery.Request{PageSize: 10, Order: order})
		if pg.Items[0].Hostname == "" || pg.Items[len(pg.Items)-1].Hostname != "" {
			t.Fatalf("hostname %s = %+v", order, pg.Items)
		}
	}
	// A page beyond the end answers the last page; zero request = defaults.
	pg, _, _ = f.svc.FleetPage(context.Background(), tenant, FleetFilter{}, listquery.Request{Page: 99, PageSize: 3})
	if pg.Page != 3 || len(pg.Items) != 1 {
		t.Fatalf("clamp = %+v", pg)
	}
	pg, _, _ = f.svc.FleetPage(context.Background(), tenant, FleetFilter{Cursor: "a9", Limit: 1}, listquery.Request{})
	if pg.Page != 1 || pg.PageSize != listquery.DefaultPageSize || pg.Sort != "hostname" || pg.Total != len(versions) {
		t.Fatalf("defaults (legacy cursor/limit ignored) = %+v", pg)
	}
	// Online filter.
	on, off := true, false
	if pg, _, _ = f.svc.FleetPage(context.Background(), tenant, FleetFilter{Online: &on}, listquery.Request{}); pg.Total != 1 || pg.Items[0].AgentID != "a1" {
		t.Fatalf("online = %+v", pg)
	}
	if pg, _, _ = f.svc.FleetPage(context.Background(), tenant, FleetFilter{Online: &off}, listquery.Request{}); pg.Total != len(versions)-1 {
		t.Fatalf("offline = %+v", pg)
	}
	// The internal id-ordered keyset is unchanged.
	all, _, _ := f.svc.Fleet(context.Background(), tenant, FleetFilter{Cursor: "a2", Limit: 2})
	if len(all) != 2 || all[0].AgentID != "a3" || all[1].AgentID != "a4" {
		t.Fatalf("keyset = %+v", all)
	}
	f.mem.FailNext("ListAgents")
	if _, _, err := f.svc.FleetPage(context.Background(), tenant, FleetFilter{}, listquery.Request{}); err == nil {
		t.Fatal("store error swallowed")
	}
}

func TestVersionSortKey(t *testing.T) {
	ordered := []string{"4.3.1~11-gabc", "4.3.1", "4.4.0-rc.1", "v4.4.0", "4.4.0+build", "4.10.0", "dev", "x.y.z"}
	for i := 1; i < len(ordered); i++ {
		a, b := VersionSortKey(ordered[i-1]), VersionSortKey(ordered[i])
		if a > b {
			t.Errorf("%q (%s) sorts after %q (%s)", ordered[i-1], a, ordered[i], b)
		}
	}
	if VersionSortKey("4.4.0") != VersionSortKey("v4.4.0") {
		t.Error("v prefix changes the key")
	}
	if VersionSortKey("1.2") != "~1.2" || VersionSortKey("1.2.3456789012") != "~1.2.3456789012" {
		t.Error("malformed version key")
	}
}
