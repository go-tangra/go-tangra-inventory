package httpapi

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
)

// 032 security review F-1: a legacy cursor/limit request is always bounded
// to 1..listquery.MaxPageSize (default legacyDefaultLimit), and walking the
// legacy cursor still reaches every row.

func TestLegacyLimitClamp(t *testing.T) {
	for v, want := range map[string]int{
		"": legacyDefaultLimit, "x": legacyDefaultLimit, "0": legacyDefaultLimit, "-5": legacyDefaultLimit,
		"7": 7, "200": listquery.MaxPageSize, "500": listquery.MaxPageSize, "100000": listquery.MaxPageSize,
	} {
		q := url.Values{"cursor": {"c"}}
		if v != "" {
			q.Set("limit", v)
		}
		if got := legacyLimit(q); got != want {
			t.Errorf("legacyLimit(%q) = %d, want %d", v, got, want)
		}
	}
	if got := clampLimit("-1", 100); got != 100 {
		t.Errorf("clampLimit default = %d", got)
	}
}

// walkLegacy follows the legacy id cursor (the last item's idKey) with the
// base query (may be empty: cursor only), asserting every page holds at most
// max items, and returns the ids.
func walkLegacy(t *testing.T, get func(string) map[string]any, base, idKey string, max int) []string {
	t.Helper()
	var ids []string
	seen := map[string]bool{}
	with := func(cursor string) string {
		c := "cursor=" + url.QueryEscape(cursor)
		if base == "" {
			return c
		}
		return base + "&" + c
	}
	path := with("")
	for range 100 {
		it := items(t, get(path))
		if len(it) > max {
			t.Fatalf("%s: %d items > %d", path, len(it), max)
		}
		if len(it) == 0 {
			return ids
		}
		for _, x := range it {
			id := x[idKey].(string)
			if seen[id] {
				t.Fatalf("%s: %s twice", path, id)
			}
			seen[id] = true
			ids = append(ids, id)
		}
		path = with(it[len(it)-1][idKey].(string))
	}
	t.Fatal("legacy walk did not end")
	return nil
}

func TestHostsLegacyLimitBounded(t *testing.T) {
	f := newAPI(t)
	hostsFixture(t, f, 230)
	get := func(path string) map[string]any {
		w := f.req(t, "GET", p+"/hosts?"+path, "admin", "")
		if w.Code != 200 {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body)
		}
		return decodeBody(t, w)
	}
	// Out-of-range limits never reach the handler unbounded: the edge
	// rejects them, and the handler clamps whatever it is handed.
	for _, l := range []string{"100000", "0", "-5"} {
		if w := f.req(t, "GET", p+"/hosts?limit="+l, "admin", ""); w.Code != 422 && len(items(t, decodeBody(t, w))) > listquery.MaxPageSize {
			t.Fatalf("limit=%s = %d, %d items", l, w.Code, len(items(t, decodeBody(t, w))))
		}
	}
	if b := get("limit=500"); len(items(t, b)) != listquery.MaxPageSize || b["total"] != 230.0 {
		t.Fatalf("limit=500: %d items, total %v", len(items(t, b)), b["total"])
	}
	// Cursor only: the default page, and the walk reaches every host.
	start := items(t, get("limit=1"))[0]["id"].(string)
	if n := len(items(t, get("cursor="+start))); n != legacyDefaultLimit {
		t.Fatalf("cursor only: %d items", n)
	}
	if ids := walkLegacy(t, get, "limit=500", "id", listquery.MaxPageSize); len(ids) != 230 {
		t.Fatalf("limit=500 walk reached %d hosts", len(ids))
	}
	if ids := walkLegacy(t, get, "", "id", legacyDefaultLimit); len(ids) != 230 {
		t.Fatalf("cursor walk reached %d hosts", len(ids))
	}
}

func TestFleetLegacyLimitBounded(t *testing.T) {
	f := newUpgradeAPI(t)
	for i := range 120 {
		f.agent(t, i+1, "4.4.0", true)
	}
	get := func(path string) map[string]any {
		w := f.req(t, "GET", p+"/agents?"+path, "admin", "", false)
		if w.Code != 200 {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body)
		}
		return decodeBody(t, w)
	}
	if b := get("cursor="); len(items(t, b)) != legacyDefaultLimit || b["total"] != 120.0 {
		t.Fatalf("cursor only: %d items, total %v", len(items(t, b)), b["total"])
	}
	if ids := walkLegacy(t, get, "", "agent_id", legacyDefaultLimit); len(ids) != 120 {
		t.Fatalf("fleet walk reached %d agents", len(ids))
	}
	// /agents/upgrades: cursor/limit clamped too (default 100).
	if w := f.req(t, "GET", p+"/agents/upgrades?limit=500", "admin", "", false); w.Code != 200 {
		t.Fatalf("upgrades limit=500 = %d %s", w.Code, w.Body)
	}
}

func TestConnectedLegacyLimitBounded(t *testing.T) {
	f := newAPI(t)
	for i := range 75 {
		if _, _, err := f.reg.Register(context.Background(), registry.ConnectedAgent{AgentID: fmt.Sprintf("018f0000-0000-7000-8000-%012d", i), TenantID: apiTenant, Version: "4.4.0"}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string) map[string]any {
		w := f.req(t, "GET", p+"/agents?"+path, "admin", "")
		if w.Code != 200 {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body)
		}
		return decodeBody(t, w)
	}
	if b := get("cursor="); len(items(t, b)) != legacyDefaultLimit || b["total"] != 75.0 {
		t.Fatalf("cursor only: %d items, total %v", len(items(t, b)), b["total"])
	}
	if ids := walkLegacy(t, get, "limit=30", "agent_id", 30); len(ids) != 75 {
		t.Fatalf("connected walk reached %d agents", len(ids))
	}
}
