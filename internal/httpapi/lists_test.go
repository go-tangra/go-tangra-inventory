package httpapi

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// List contract (go-tangra specs/032-server-side-tables): page, page_size,
// sort, order; {items,total,page,page_size,sort,order}; 422 naming the
// parameter; legacy cursor/limit for one release.

// hostsFixture adds n hosts to apiTenant: host-00 … with alternating OS and
// last_seen i hours ago.
func hostsFixture(t *testing.T, f *apiFixture, n int) time.Time {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	for i := range n {
		os := "linux"
		if i%2 == 1 {
			os = "windows"
		}
		if _, err := f.mem.ResolveHost(context.Background(), apiTenant, store.Host{Hostname: fmt.Sprintf("host-%02d", i), MachineID: fmt.Sprintf("m%02d", i),
			OSName: os, LastSeen: now.Add(-time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	return now
}

func items(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, _ := m["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestHostsListContract(t *testing.T) {
	f := newAPI(t)
	now := hostsFixture(t, f, 30)

	w := f.req(t, "GET", p+"/hosts", "admin", "")
	if w.Code != 200 {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}
	b := decodeBody(t, w)
	if b["total"] != 30.0 || b["page"] != 1.0 || b["page_size"] != 25.0 || b["sort"] != "hostname" || b["order"] != "asc" {
		t.Fatalf("defaults = %v", b)
	}
	if it := items(t, b); len(it) != 25 || it[0]["hostname"] != "host-00" || it[24]["hostname"] != "host-24" {
		t.Fatalf("first page = %v", it)
	}
	// Every host exactly once across the pages, for every sort and direction.
	for _, sort := range []string{"hostname", "os_name", "manufacturer", "status", "last_seen", "created_at"} {
		for _, order := range []string{"asc", "desc"} {
			seen := map[string]int{}
			for page := 1; page <= 4; page++ {
				w := f.req(t, "GET", fmt.Sprintf("%s/hosts?page=%d&page_size=8&sort=%s&order=%s", p, page, sort, order), "admin", "")
				if w.Code != 200 {
					t.Fatalf("%s %s page %d = %d %s", sort, order, page, w.Code, w.Body)
				}
				for _, h := range items(t, decodeBody(t, w)) {
					seen[h["id"].(string)]++
				}
			}
			if len(seen) != 30 {
				t.Fatalf("%s %s: %d distinct hosts", sort, order, len(seen))
			}
			for id, n := range seen {
				if n != 1 {
					t.Fatalf("%s %s: host %s seen %d times", sort, order, id, n)
				}
			}
		}
	}
	// last_seen defaults to newest first.
	b = decodeBody(t, f.req(t, "GET", p+"/hosts?sort=last_seen&page_size=1", "admin", ""))
	if b["order"] != "desc" || items(t, b)[0]["hostname"] != "host-00" {
		t.Fatalf("last_seen default = %v", b)
	}
	// A page beyond the end answers the last page.
	b = decodeBody(t, f.req(t, "GET", p+"/hosts?page=9", "admin", ""))
	if b["page"] != 2.0 || len(items(t, b)) != 5 {
		t.Fatalf("clamped = %v", b)
	}
	// Filters apply before the count: os_name (the UI's former "os" is ignored).
	b = decodeBody(t, f.req(t, "GET", p+"/hosts?os_name=windows", "admin", ""))
	if b["total"] != 15.0 {
		t.Fatalf("os_name filter total = %v", b["total"])
	}
	for _, h := range items(t, b) {
		if h["os_name"] != "windows" {
			t.Fatalf("os_name filter leaked %v", h)
		}
	}
	// last_seen_from: hosts seen within the last 10 hours (i = 0..10).
	from := url.QueryEscape(now.Add(-10*time.Hour - time.Minute).Format(time.RFC3339))
	b = decodeBody(t, f.req(t, "GET", p+"/hosts?last_seen_from="+from, "admin", ""))
	if b["total"] != 11.0 {
		t.Fatalf("last_seen_from total = %v", b["total"])
	}
	to := url.QueryEscape(now.Add(-20*time.Hour + time.Minute).Format(time.RFC3339))
	b = decodeBody(t, f.req(t, "GET", p+"/hosts?last_seen_from="+from+"&last_seen_to="+to+"&os_name=linux", "admin", ""))
	if b["total"] != 0.0 {
		t.Fatalf("empty window total = %v", b["total"])
	}
	b = decodeBody(t, f.req(t, "GET", p+"/hosts?last_seen_to="+to+"&os_name=linux", "admin", ""))
	if b["total"] != 5.0 { // i = 20, 22, 24, 26, 28
		t.Fatalf("last_seen_to + os total = %v", b["total"])
	}
	// Tenant isolation: another tenant's host is never counted.
	if _, err := f.mem.ResolveHost(context.Background(), "99999999-9999-7999-8999-999999999999", store.Host{Hostname: "elsewhere", MachineID: "x"}); err != nil {
		t.Fatal(err)
	}
	if b = decodeBody(t, f.req(t, "GET", p+"/hosts", "admin", "")); b["total"] != 30.0 {
		t.Fatalf("tenant isolation total = %v", b["total"])
	}
}

func TestHostsListNegativesAndLegacy(t *testing.T) {
	f := newAPI(t)
	hostsFixture(t, f, 3)
	for q, param := range map[string]string{
		"sort=payload":                         "sort",
		"order=sideways":                       "order",
		"page=0":                               "page",
		"page=x":                               "page",
		"page_size=0":                          "page_size",
		"page_size=201":                        "page_size",
		"page=1&cursor=abc":                    "cursor",
		"limit=5&sort=hostname":                "cursor",
		"last_seen_from=nonsense":              "last_seen_from",
		"hostname=" + strings.Repeat("h", 201): "hostname",
	} {
		w := f.req(t, "GET", p+"/hosts?"+q, "admin", "")
		if w.Code != 422 {
			t.Fatalf("%s = %d %s", q, w.Code, w.Body)
		}
		b := decodeBody(t, w)
		d, _ := b["detail"].(map[string]any)
		if b["reason"] != "validation_failed" || d["param"] != param {
			t.Fatalf("%s = %s", q, w.Body)
		}
	}
	// Legacy limit/cursor: the former id-DESC shape plus the total.
	w := f.req(t, "GET", p+"/hosts?limit=2", "admin", "")
	b := decodeBody(t, w)
	it := items(t, b)
	if w.Code != 200 || len(it) != 2 || b["total"] != 3.0 || b["page"] != nil {
		t.Fatalf("legacy = %d %s", w.Code, w.Body)
	}
	if it[0]["id"].(string) < it[1]["id"].(string) {
		t.Fatalf("legacy order not id DESC: %v", it)
	}
	b = decodeBody(t, f.req(t, "GET", p+"/hosts?limit=2&cursor="+it[1]["id"].(string), "admin", ""))
	if len(items(t, b)) != 1 || b["total"] != 3.0 {
		t.Fatalf("legacy cursor = %v", b)
	}
	f.mem.FailNext("ListHostsPage")
	if w := f.req(t, "GET", p+"/hosts", "admin", ""); w.Code != 500 {
		t.Fatalf("store error = %d", w.Code)
	}
}

func TestSnapshotsAndChangesListContract(t *testing.T) {
	f := newAPI(t)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	var host string
	for i := range 12 {
		inv := store.Inventory{CollectedAt: base.Add(time.Duration(i) * time.Hour), OS: store.OSInfo{Name: "linux"}}
		inv.Programs = []store.Program{{Name: fmt.Sprintf("pkg-%02d", i), Version: "1"}}
		host = f.seed(t, "snap-host", inv)
	}
	w := f.req(t, "GET", p+"/hosts/"+host+"/snapshots?page_size=5", "admin", "")
	b := decodeBody(t, w)
	if w.Code != 200 || b["total"] != 12.0 || b["sort"] != "collected_at" || b["order"] != "desc" || len(items(t, b)) != 5 {
		t.Fatalf("snapshots = %d %s", w.Code, w.Body)
	}
	first := items(t, b)[0]
	if _, ok := first["payload"]; ok && first["payload"] != nil {
		t.Fatalf("summary carries a payload: %v", first)
	}
	if ts, _ := time.Parse(time.RFC3339, first["collected_at"].(string)); !ts.Equal(base.Add(11 * time.Hour)) {
		t.Fatalf("newest first: %v", first["collected_at"])
	}
	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		for _, s := range items(t, decodeBody(t, f.req(t, "GET", fmt.Sprintf("%s/hosts/%s/snapshots?page=%d&page_size=5&order=asc", p, host, page), "admin", ""))) {
			if seen[s["id"].(string)] {
				t.Fatalf("snapshot %v twice", s["id"])
			}
			seen[s["id"].(string)] = true
		}
	}
	if len(seen) != 12 {
		t.Fatalf("snapshots seen %d", len(seen))
	}
	// Legacy limit: previous shape plus total.
	b = decodeBody(t, f.req(t, "GET", p+"/hosts/"+host+"/snapshots?limit=3", "admin", ""))
	if len(items(t, b)) != 3 || b["total"] != 12.0 || b["page"] != nil {
		t.Fatalf("legacy snapshots = %v", b)
	}
	if w := f.req(t, "GET", p+"/hosts/"+host+"/snapshots?sort=received_at", "admin", ""); w.Code != 422 {
		t.Fatalf("snapshot sort = %d", w.Code)
	}

	// Changes: each new snapshot adds and removes a program.
	w = f.req(t, "GET", p+"/hosts/"+host+"/changes?page_size=4", "admin", "")
	b = decodeBody(t, w)
	total, _ := b["total"].(float64)
	if w.Code != 200 || total < 11 || len(items(t, b)) != 4 || b["sort"] != "detected_at" || b["order"] != "desc" {
		t.Fatalf("changes = %d %s", w.Code, w.Body)
	}
	seen = map[string]bool{}
	pages := int(total+3) / 4
	for page := 1; page <= pages; page++ {
		for _, c := range items(t, decodeBody(t, f.req(t, "GET", fmt.Sprintf("%s/hosts/%s/changes?page=%d&page_size=4&sort=kind", p, host, page), "admin", ""))) {
			if seen[c["id"].(string)] {
				t.Fatalf("change %v twice", c["id"])
			}
			seen[c["id"].(string)] = true
		}
	}
	if len(seen) != int(total) {
		t.Fatalf("changes seen %d of %v", len(seen), total)
	}
	// Legacy limit is honoured now.
	b = decodeBody(t, f.req(t, "GET", p+"/hosts/"+host+"/changes?limit=2", "admin", ""))
	if len(items(t, b)) != 2 || b["total"] != total {
		t.Fatalf("legacy changes = %v", b)
	}
	if w := f.req(t, "GET", p+"/hosts/"+host+"/changes?order=up", "admin", ""); w.Code != 422 {
		t.Fatalf("changes order = %d", w.Code)
	}
}

func TestFleetListContract(t *testing.T) {
	f := newUpgradeAPI(t)
	versions := []string{"4.10.0", "4.4.0", "4.9.1", "4.5.0", "4.4.0"}
	ids := make([]string, len(versions))
	for i, v := range versions {
		ids[i] = f.agent(t, i+1, v, true)
	}
	_, unregister, _ := f.reg.Register(context.Background(), registry.ConnectedAgent{AgentID: ids[2], TenantID: apiTenant, Version: "4.9.1"})
	defer unregister()

	w := f.req(t, "GET", p+"/agents?page_size=2", "admin", "", false)
	b := decodeBody(t, w)
	if w.Code != 200 || b["total"] != 5.0 || b["page_size"] != 2.0 || b["sort"] != "hostname" || b["current_version"] != "4.5.0" || len(items(t, b)) != 2 {
		t.Fatalf("fleet page = %d %s", w.Code, w.Body)
	}
	// Versions sort by semantic version, not as text.
	b = decodeBody(t, f.req(t, "GET", p+"/agents?sort=version&order=desc", "admin", "", false))
	if it := items(t, b); it[0]["version"] != "4.10.0" || it[1]["version"] != "4.9.1" || it[4]["version"] != "4.4.0" {
		t.Fatalf("version order = %v", it)
	}
	// Each agent once across pages for every sort.
	for _, sort := range []string{"hostname", "version", "state", "last_seen"} {
		seen := map[string]bool{}
		for page := 1; page <= 3; page++ {
			for _, a := range items(t, decodeBody(t, f.req(t, "GET", fmt.Sprintf("%s/agents?page=%d&page_size=2&sort=%s", p, page, sort), "admin", "", false))) {
				if seen[a["agent_id"].(string)] {
					t.Fatalf("%s: agent twice", sort)
				}
				seen[a["agent_id"].(string)] = true
			}
		}
		if len(seen) != 5 {
			t.Fatalf("%s: %d agents", sort, len(seen))
		}
	}
	// online filter (the UI's connected-agent registry).
	b = decodeBody(t, f.req(t, "GET", p+"/agents?online=true", "admin", "", false))
	if it := items(t, b); b["total"] != 1.0 || it[0]["agent_id"] != ids[2] {
		t.Fatalf("online = %v", b)
	}
	if b = decodeBody(t, f.req(t, "GET", p+"/agents?online=false", "admin", "", false)); b["total"] != 4.0 {
		t.Fatalf("offline = %v", b)
	}
	// Legacy limit: previous shape plus total; mixing is a 422 on cursor.
	b = decodeBody(t, f.req(t, "GET", p+"/agents?limit=2", "admin", "", false))
	if len(items(t, b)) != 2 || b["total"] != 5.0 || b["page"] != nil || b["current_version"] != "4.5.0" {
		t.Fatalf("legacy fleet = %v", b)
	}
	w = f.req(t, "GET", p+"/agents?cursor="+ids[0]+"&page=1", "admin", "", false)
	if d, _ := decodeBody(t, w)["detail"].(map[string]any); w.Code != 422 || d["param"] != "cursor" {
		t.Fatalf("mixed = %d %s", w.Code, w.Body)
	}
	w = f.req(t, "GET", p+"/agents?sort=agent_id", "admin", "", false)
	if d, _ := decodeBody(t, w)["detail"].(map[string]any); w.Code != 422 || d["param"] != "sort" {
		t.Fatalf("bad sort = %d %s", w.Code, w.Body)
	}
}

func TestConnectedAgentsListContract(t *testing.T) {
	f := newAPI(t)
	w := f.req(t, "GET", p+"/agents", "admin", "")
	b := decodeBody(t, w)
	if w.Code != 200 || b["total"] != 0.0 || b["sort"] != "hostname" {
		t.Fatalf("connected = %d %s", w.Code, w.Body)
	}
	if b = decodeBody(t, f.req(t, "GET", p+"/agents?limit=5", "admin", "")); b["total"] != 0.0 || b["page"] != nil {
		t.Fatalf("legacy connected = %v", b)
	}
	if w := f.req(t, "GET", p+"/agents?order=x", "admin", ""); w.Code != 422 {
		t.Fatalf("bad order = %d", w.Code)
	}
}

func TestAutoEnrollKeysListContract(t *testing.T) {
	f := newAutoAPI(t)
	const base = p + "/agents/auto-enroll"
	for _, name := range []string{"charlie", "alpha", "Bravo"} {
		w := f.req(t, "POST", base+"/keys", "admin", `{"name":"`+name+`","allowed_cidrs":["10.0.0.0/8"]}`, true)
		if w.Code != 201 {
			t.Fatalf("create %s = %d %s", name, w.Code, w.Body)
		}
	}
	b := decode(t, f.req(t, "GET", base+"?page_size=2", "admin", "", false).Body.String())
	keys, _ := b["keys"].([]any)
	if b["total"] != 3.0 || b["sort"] != "name" || len(keys) != 2 || keys[0].(map[string]any)["name"] != "alpha" || keys[1].(map[string]any)["name"] != "Bravo" {
		t.Fatalf("keys page = %v", b)
	}
	b = decode(t, f.req(t, "GET", base+"?page=2&page_size=2&sort=name&order=desc", "admin", "", false).Body.String())
	keys, _ = b["keys"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["name"] != "alpha" {
		t.Fatalf("keys desc page 2 = %v", b)
	}
	if b = decode(t, f.req(t, "GET", base+"?sort=created_at", "admin", "", false).Body.String()); b["order"] != "desc" {
		t.Fatalf("created_at default = %v", b)
	}
	if w := f.req(t, "GET", base+"?sort=secret", "admin", "", false); w.Code != 422 {
		t.Fatalf("bad sort = %d", w.Code)
	}
}
