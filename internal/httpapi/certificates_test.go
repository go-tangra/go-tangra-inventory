package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra/v4/freyatest/testrt"
	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/backup"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/certdelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/releases"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/stats"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrades"
)

const otherTenant = "44444444-4444-7444-8444-444444444444"

type certAPI struct {
	s    *Server
	mem  *memstore.Mem
	cert *certdelivery.Service
	host store.Host
}

// newCertAPI wires the API with the certificate delivery service (enabled
// or not) and the fleet view, and adds host web-1 to apiTenant.
func newCertAPI(t *testing.T, enabled bool) *certAPI {
	t.Helper()
	mem := memstore.New()
	rt := testrt.New(t, testutil.MustCA("example.org"), "inventory")
	reg := registry.NewMemory()
	rel := releases.New(mem, agentrelease.Keyring{}, nopAudit{}, slog.New(slog.DiscardHandler), releases.Config{BundleDir: t.TempDir(), KeepVersions: 5, ChunkBytes: 65536})
	upg := upgrades.New(mem, rel, reg, events.HubPublisher{}, upgrades.Config{RequestTTL: time.Hour, ProgressTimeout: 15 * time.Minute})
	cert := certdelivery.New(mem, nil, reg, events.HubPublisher{}, certdelivery.Config{Enabled: enabled, PendingTTL: time.Hour, ReportTimeout: 15 * time.Minute})
	hostsSvc := hosts.New(mem)
	env, _ := sealed.NewEnvelope(make([]byte, 32))
	v := fakeVerifier{ids: map[string]authclient.Identity{
		"admin": {UserID: apiAdmin, TenantID: apiTenant, Roles: []string{"admin"}},
		"other": {UserID: apiAdmin, TenantID: otherTenant, Roles: []string{"admin"}},
	}}
	s, err := NewHandler(rt, WithVerifier(v))
	if err != nil {
		t.Fatal(err)
	}
	s.Register(Deps{Hosts: hostsSvc, Snapshots: snapshots.New(mem, hostsSvc, events.HubPublisher{}), Stats: stats.New(mem),
		Backup: backup.New(mem), Enroll: enroll.New(mem, env), Registry: reg, Upgrades: upg, Releases: rel, CertDelivery: cert})
	h, err := mem.ResolveHost(context.Background(), apiTenant, store.Host{Hostname: "web-1", MachineID: "m-1", Status: store.HostActive})
	if err != nil {
		t.Fatal(err)
	}
	return &certAPI{s: s, mem: mem, cert: cert, host: h}
}

func (f *certAPI) req(t *testing.T, method, path, tok, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://localhost"+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if method != "GET" {
		r.Header.Set("X-CSRF-Token", "t")
	}
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "-----BEGIN") {
		t.Fatalf("%s %s returned PEM: %s", method, path, w.Body)
	}
	return w
}

var certSeq int

// deliver stores a delivery of name to host with one item in state (a
// terminal installed/unchanged/failed item also updates the host
// certificate) and returns the item.
func (f *certAPI) deliver(t *testing.T, hostID, name, state string, at time.Time) store.CertDeliveryItem {
	t.Helper()
	ctx := context.Background()
	certSeq++
	d := store.CertDelivery{ID: store.NewID(), TenantID: apiTenant, Source: "deployer", IdempotencyKey: fmt.Sprintf("job-%d", certSeq),
		ConfigurationID: "cfg-1", Trigger: store.TriggerManual, CertificateID: "cert-1", Name: name, KeyPolicy: store.KeyPolicyRequire,
		HostIDs: []string{hostID}, RequestedBy: "spiffe://example.org/svc/deployer", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
	it := store.CertDeliveryItem{ID: store.NewID(), TenantID: apiTenant, DeliveryID: d.ID, HostID: hostID, Name: name, CertificateID: "cert-1",
		State: store.DeliveryPending, Attempts: 1, CreatedAt: at, UpdatedAt: at}
	if _, err := f.mem.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d, Items: []store.CertDeliveryItem{it}}); err != nil {
		t.Fatal(err)
	}
	if state == store.DeliveryPending {
		return it
	}
	fp := strings.Repeat("ab", 32)
	code := 0
	got, err := f.mem.UpdateCertItem(ctx, apiTenant, it.ID, func(i *store.CertDeliveryItem) (repo.CertItemChange, error) {
		i.State, i.Serial, i.FingerprintSHA256, i.CommonName, i.HookExitCode, i.UpdatedAt = state, "4f3a", fp, "www.example.com", &code, at
		if store.CertDeliveryActive(state) {
			return repo.CertItemChange{}, nil
		}
		i.FinishedAt = &at
		na := at.Add(90 * 24 * time.Hour)
		hc := store.HostCertificate{HostID: hostID, Name: name, CertificateID: "cert-1", ConfigurationID: "cfg-1", CommonName: "www.example.com",
			Serial: "4f3a", FingerprintSHA256: fp, NotAfter: &na, State: state, HookExitCode: &code, LastItemID: i.ID, LastDeliveredAt: &at, UpdatedAt: at}
		return repo.CertItemChange{HostCert: &hc}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func decodeInto(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", w.Body, err)
	}
}

// TestCertificateRoutesDeclared (T073): the four routes are mounted with the
// permissions the gateway enforces (a caller without them gets 403 there).
func TestCertificateRoutesDeclared(t *testing.T) {
	f := newCertAPI(t, true)
	want := map[Route]string{
		{"GET", p + "/hosts/{id}/certificates"}:                  "inventory:read",
		{"GET", p + "/certificate-deliveries"}:                   "inventory:read",
		{"GET", p + "/certificate-deliveries/{item_id}"}:         "inventory:read",
		{"POST", p + "/certificate-deliveries/{item_id}/cancel"}: "agents:manage",
	}
	implemented := map[Route]bool{}
	for _, r := range f.s.Implemented() {
		implemented[r] = true
	}
	for rt, perm := range want {
		if !implemented[rt] {
			t.Errorf("%s not implemented", rt)
		}
		op := f.s.Document().Paths.Find(rt.Path).GetOperation(rt.Method)
		if got, _ := op.Extensions[PermissionExtension].(string); got != perm {
			t.Errorf("%s permission %q, want %q", rt, got, perm)
		}
	}
	// Without the certificate service the routes stay unimplemented.
	g := newAPI(t)
	if w := g.req(t, "GET", p+"/certificate-deliveries", "admin", ""); w.Code != 501 {
		t.Fatalf("without the service: %d", w.Code)
	}
	if w := f.req(t, "GET", p+"/certificate-deliveries", "", ""); w.Code != 401 {
		t.Fatalf("no token: %d", w.Code)
	}
}

// TestHostCertificatesRoute (T073): one row per name with the queued item;
// the revoked filter (T105); paging/sorting per 032; tenant isolation.
func TestHostCertificatesRoute(t *testing.T) {
	f := newCertAPI(t, true)
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	inst := f.deliver(t, f.host.ID, "www", store.DeliveryInstalled, t0)
	queued := f.deliver(t, f.host.ID, "www", store.DeliveryPending, t0.Add(time.Minute))
	f.deliver(t, f.host.ID, "api", store.DeliveryHookFailed, t0.Add(2*time.Minute))
	f.deliver(t, f.host.ID, "mail", store.DeliveryPending, t0.Add(3*time.Minute))

	w := f.req(t, "GET", p+"/hosts/"+f.host.ID+"/certificates", "admin", "")
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
		Page  int              `json:"page"`
		Sort  string           `json:"sort"`
		Order string           `json:"order"`
	}
	decodeInto(t, w, &page)
	if page.Total != 3 || len(page.Items) != 3 || page.Sort != "name" || page.Order != "asc" || page.Page != 1 {
		t.Fatalf("page: %+v", page)
	}
	api, mail, www := page.Items[0], page.Items[1], page.Items[2]
	if api["name"] != "api" || api["state"] != "hook_failed" || api["hook_exit_code"] != float64(0) || api["active_item"] != nil || api["revoked"] != false {
		t.Fatalf("api row: %v", api)
	}
	if mail["state"] != "pending" || mail["last_delivered_at"] != nil || mail["not_after"] != nil {
		t.Fatalf("queued-only row: %v", mail)
	}
	active, _ := www["active_item"].(map[string]any)
	if www["state"] != "installed" || www["common_name"] != "www.example.com" || www["configuration_id"] != "cfg-1" || active == nil ||
		active["id"] != queued.ID || active["hostname"] != "web-1" || active["trigger"] != "manual" || www["fingerprint_sha256"] != inst.FingerprintSHA256 {
		t.Fatalf("www row: %v", www)
	}
	for _, row := range page.Items {
		for _, k := range []string{"tenant_id", "host_id", "last_item_id", "updated_at"} {
			if _, ok := row[k]; ok {
				t.Fatalf("row leaks %s: %v", k, row)
			}
		}
	}
	// Sorting and paging.
	w = f.req(t, "GET", p+"/hosts/"+f.host.ID+"/certificates?sort=name&order=desc&page=1&page_size=1", "admin", "")
	decodeInto(t, w, &page)
	if page.Total != 3 || len(page.Items) != 1 || page.Items[0]["name"] != "www" {
		t.Fatalf("sorted page: %+v", page)
	}
	// Filters.
	w = f.req(t, "GET", p+"/hosts/"+f.host.ID+"/certificates?state=hook_failed", "admin", "")
	decodeInto(t, w, &page)
	if page.Total != 1 || page.Items[0]["name"] != "api" {
		t.Fatalf("state filter: %+v", page)
	}
	if _, _, err := f.cert.MarkRevoked(context.Background(), apiTenant, certdelivery.Actor{Kind: "service", ID: "deployer"}, "cert-1"); err != nil {
		t.Fatal(err)
	}
	w = f.req(t, "GET", p+"/hosts/"+f.host.ID+"/certificates?revoked=true", "admin", "")
	decodeInto(t, w, &page)
	if page.Total != 2 || page.Items[0]["revoked"] != true || page.Items[0]["revoked_at"] == nil {
		t.Fatalf("revoked filter: %+v", page)
	}
	// Bad list parameters: 422 naming the parameter.
	for q, param := range map[string]string{"sort=hostname": "sort", "page=0": "page", "page_size=201": "page_size", "order=up": "order",
		"state=bogus": "state", "revoked=maybe": "revoked"} {
		w := f.req(t, "GET", p+"/hosts/"+f.host.ID+"/certificates?"+q, "admin", "")
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"param":"`+param+`"`) {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
	}
	// Another tenant's host and unknown hosts: 404.
	for tok, id := range map[string]string{"other": f.host.ID, "admin": store.NewID()} {
		if w := f.req(t, "GET", p+"/hosts/"+id+"/certificates", tok, ""); w.Code != 404 {
			t.Fatalf("%s %s: %d", tok, id, w.Code)
		}
	}
	// A store failure answers 503 without detail.
	f.mem.FailNext("ListHostCertificates")
	if w := f.req(t, "GET", p+"/hosts/"+f.host.ID+"/certificates", "admin", ""); w.Code != 503 || !strings.Contains(w.Body.String(), "temporarily_unavailable") {
		t.Fatalf("store error: %d %s", w.Code, w.Body)
	}
}

// TestCertificateDeliveriesRoute (T073): history paging, sorting and filters
// per 032; one item with its delivery; tenant isolation.
func TestCertificateDeliveriesRoute(t *testing.T) {
	f := newCertAPI(t, true)
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	h2, _ := f.mem.ResolveHost(context.Background(), apiTenant, store.Host{Hostname: "web-2", MachineID: "m-2", Status: store.HostActive})
	a := f.deliver(t, f.host.ID, "www", store.DeliveryInstalled, t0)
	b := f.deliver(t, f.host.ID, "api", store.DeliveryFailed, t0.Add(time.Minute))
	c := f.deliver(t, h2.ID, "www", store.DeliveryPending, t0.Add(2*time.Minute))

	var page struct {
		Items    []map[string]any `json:"items"`
		Total    int              `json:"total"`
		Page     int              `json:"page"`
		PageSize int              `json:"page_size"`
		Sort     string           `json:"sort"`
		Order    string           `json:"order"`
	}
	w := f.req(t, "GET", p+"/certificate-deliveries", "admin", "")
	decodeInto(t, w, &page)
	if w.Code != 200 || page.Total != 3 || page.Sort != "created_at" || page.Order != "desc" || page.PageSize != 25 || page.Items[0]["id"] != c.ID {
		t.Fatalf("default page: %d %+v", w.Code, page)
	}
	first := page.Items[2]
	if first["id"] != a.ID || first["hostname"] != "web-1" || first["configuration_id"] != "cfg-1" || first["trigger"] != "manual" ||
		first["common_name"] != "www.example.com" || first["attempts"] != float64(1) || first["finished_at"] == nil {
		t.Fatalf("item: %v", first)
	}
	for _, k := range []string{"tenant_id", "agent_id", "fetches", "rerun_hook"} {
		if _, ok := first[k]; ok {
			t.Fatalf("item leaks %s: %v", k, first)
		}
	}
	for q, want := range map[string][]string{
		"host_id=" + f.host.ID:        {b.ID, a.ID},
		"state=failed":                {b.ID},
		"name=api":                    {b.ID},
		"certificate_id=cert-1":       {c.ID, b.ID, a.ID},
		"delivery_id=" + c.DeliveryID: {c.ID},
		"sort=name&order=asc":         {b.ID, a.ID, c.ID},
		"sort=created_at&order=asc":   {a.ID, b.ID, c.ID},
		"page=2&page_size=2":          {a.ID},
		"page=9&page_size=2":          {a.ID}, // beyond the end: the last page
		"host_id=" + store.NewID():    {},
	} {
		w := f.req(t, "GET", p+"/certificate-deliveries?"+q, "admin", "")
		decodeInto(t, w, &page)
		var got []string
		for _, it := range page.Items {
			got = append(got, it["id"].(string))
		}
		if w.Code != 200 || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: %d %v want %v", q, w.Code, got, want)
		}
	}
	for q, param := range map[string]string{"sort=hostname": "sort", "page=0": "page", "page_size=500": "page_size", "host_id=x": "host_id",
		"state=done": "state", "name=../x": "name", "delivery_id=1": "delivery_id", "certificate_id=" + strings.Repeat("c", 129): "certificate_id"} {
		w := f.req(t, "GET", p+"/certificate-deliveries?"+q, "admin", "")
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"param":"`+param+`"`) {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
	}
	// Another tenant sees nothing.
	w = f.req(t, "GET", p+"/certificate-deliveries", "other", "")
	decodeInto(t, w, &page)
	if page.Total != 0 || page.Items == nil {
		t.Fatalf("other tenant: %+v", page)
	}
	// One item with its delivery.
	w = f.req(t, "GET", p+"/certificate-deliveries/"+a.ID, "admin", "")
	var detail struct {
		Item     map[string]any `json:"item"`
		Delivery map[string]any `json:"delivery"`
	}
	decodeInto(t, w, &detail)
	if w.Code != 200 || detail.Item["id"] != a.ID || detail.Delivery["id"] != a.DeliveryID || detail.Delivery["source"] != "deployer" ||
		detail.Delivery["requested_by"] != "spiffe://example.org/svc/deployer" || detail.Delivery["key_policy"] != "require" {
		t.Fatalf("detail: %d %+v", w.Code, detail)
	}
	if _, ok := detail.Delivery["host_ids"]; ok {
		t.Fatal("detail leaks the host selection")
	}
	for tok, id := range map[string]string{"other": a.ID, "admin": store.NewID()} {
		if w := f.req(t, "GET", p+"/certificate-deliveries/"+id, tok, ""); w.Code != 404 {
			t.Fatalf("%s %s: %d", tok, id, w.Code)
		}
	}
	if w := f.req(t, "GET", p+"/certificate-deliveries/not-a-uuid", "admin", ""); w.Code != 404 {
		t.Fatalf("malformed id: %d", w.Code)
	}
	f.mem.FailNext("ListCertItemsPage")
	if w := f.req(t, "GET", p+"/certificate-deliveries", "admin", ""); w.Code != 503 {
		t.Fatalf("store error: %d", w.Code)
	}
}

// TestCancelCertificateDelivery (T073): queued items are cancelled by the
// user (audited); terminal items 409; other tenants 404; disabled 503
// while reads keep working.
func TestCancelCertificateDelivery(t *testing.T) {
	f := newCertAPI(t, true)
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	q := f.deliver(t, f.host.ID, "www", store.DeliveryPending, t0)
	done := f.deliver(t, f.host.ID, "api", store.DeliveryInstalled, t0)

	if w := f.req(t, "POST", p+"/certificate-deliveries/"+q.ID+"/cancel", "other", ""); w.Code != 404 {
		t.Fatalf("other tenant: %d", w.Code)
	}
	w := f.req(t, "POST", p+"/certificate-deliveries/"+q.ID+"/cancel", "admin", "")
	var it map[string]any
	decodeInto(t, w, &it)
	if w.Code != 200 || it["state"] != "cancelled" || it["reason"] != "cancelled_by_user" || it["hostname"] != "web-1" {
		t.Fatalf("cancel: %d %v", w.Code, it)
	}
	rows := f.mem.AuditRows()
	if last := rows[len(rows)-1]; last.Action != "cert_delivery_cancelled" || last.ActorKind != "user" || last.ActorID != apiAdmin {
		t.Fatalf("audit: %+v", last)
	}
	for id, code := range map[string]int{q.ID: 409, done.ID: 409, store.NewID(): 404, "x": 404} {
		w := f.req(t, "POST", p+"/certificate-deliveries/"+id+"/cancel", "admin", "")
		if w.Code != code {
			t.Fatalf("cancel %s: %d %s", id, w.Code, w.Body)
		}
		if code == 409 && !strings.Contains(w.Body.String(), "not_cancellable") {
			t.Fatalf("409 reason: %s", w.Body)
		}
	}
	if w := f.req(t, "POST", p+"/certificate-deliveries/"+q.ID+"/cancel", "admin", strings.Repeat("x", 2048)); w.Code != 413 {
		t.Fatalf("oversized body: %d", w.Code)
	}
	// A store failure after the cancel (reading the view) is a 503.
	q2 := f.deliver(t, f.host.ID, "mail", store.DeliveryPending, t0)
	f.mem.FailNext("HostnamesByID")
	if w := f.req(t, "POST", p+"/certificate-deliveries/"+q2.ID+"/cancel", "admin", ""); w.Code != 503 {
		t.Fatalf("view error: %d", w.Code)
	}

	// Disabled relay: cancel 503, reads work.
	g := newCertAPI(t, false)
	q3 := g.deliver(t, g.host.ID, "www", store.DeliveryPending, t0)
	if w := g.req(t, "POST", p+"/certificate-deliveries/"+q3.ID+"/cancel", "admin", ""); w.Code != 503 || !strings.Contains(w.Body.String(), "certificate_delivery_disabled") {
		t.Fatalf("disabled cancel: %d %s", w.Code, w.Body)
	}
	if w := g.req(t, "GET", p+"/certificate-deliveries", "admin", ""); w.Code != 200 {
		t.Fatalf("disabled read: %d", w.Code)
	}
	if w := g.req(t, "GET", p+"/hosts/"+g.host.ID+"/certificates", "admin", ""); w.Code != 200 {
		t.Fatalf("disabled host read: %d", w.Code)
	}
}

// TestAgentListCertificateCapability (T074): every fleet entry carries its
// certificate capability per data-model §1.5 (first match wins).
func TestAgentListCertificateCapability(t *testing.T) {
	type agentCase struct {
		os, version string
		caps        []string
		want        string
	}
	cases := []agentCase{
		{"linux", "4.7.0", []string{store.CapUpgradeV1, store.CapCertV1}, "enabled"},
		{"linux", "4.8.1", []string{store.CapUpgradeV1}, "disabled_on_host"},
		{"linux", "4.6.3", []string{store.CapUpgradeV1}, "upgrade_required"},
		{"linux", "", nil, "upgrade_required"},
		{"windows", "4.7.0", []string{store.CapCertV1}, "not_supported_platform"},
	}
	add := func(t *testing.T, f *certAPI) map[string]agentCase {
		ids := map[string]agentCase{}
		for n, c := range cases {
			id := fmt.Sprintf("018f0000-0000-7000-8000-%012d", n+1)
			if err := f.mem.CreateAgent(context.Background(), store.Agent{ID: id, TenantID: apiTenant, AgentVersion: c.version}); err != nil {
				t.Fatal(err)
			}
			if err := f.mem.SetAgentPlatform(context.Background(), id, c.os, "amd64", "deb", c.caps, time.Now()); err != nil {
				t.Fatal(err)
			}
			ids[id] = c
		}
		return ids
	}
	list := func(t *testing.T, f *certAPI, q string) []map[string]any {
		w := f.req(t, "GET", p+"/agents"+q, "admin", "")
		var page struct {
			Items []map[string]any `json:"items"`
		}
		decodeInto(t, w, &page)
		if w.Code != 200 || len(page.Items) != len(cases) {
			t.Fatalf("agents: %d %s", w.Code, w.Body)
		}
		return page.Items
	}
	f := newCertAPI(t, true)
	ids := add(t, f)
	for _, q := range []string{"", "?cursor=&limit=50"} { // list contract and legacy shapes
		for _, it := range list(t, f, q) {
			c := ids[it["agent_id"].(string)]
			if it["certificate_capability"] != c.want {
				t.Errorf("%s %s %v: capability %v, want %s", c.os, c.version, c.caps, it["certificate_capability"], c.want)
			}
			if _, ok := it["capabilities"]; ok {
				t.Fatalf("raw capabilities exposed: %v", it)
			}
		}
	}
	// The agent detail carries it as well.
	for id, c := range ids {
		w := f.req(t, "GET", p+"/agents/"+id, "admin", "")
		var d map[string]any
		decodeInto(t, w, &d)
		if w.Code != 200 || d["certificate_capability"] != c.want {
			t.Fatalf("detail %s: %d %v", id, w.Code, d["certificate_capability"])
		}
	}
	// Switched off on the server: every agent shows disabled_on_server.
	g := newCertAPI(t, false)
	add(t, g)
	for _, it := range list(t, g, "") {
		if it["certificate_capability"] != "disabled_on_server" {
			t.Fatalf("disabled server: %v", it)
		}
	}
}
