package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-freya/freya/internal/testrt"
	"github.com/go-freya/freya/internal/testutil"
	"github.com/go-freya/freya/services/auth/pkg/authclient"

	"github.com/go-freya/freya/services/inventory/internal/backup"
	"github.com/go-freya/freya/services/inventory/internal/enroll"
	"github.com/go-freya/freya/services/inventory/internal/events"
	"github.com/go-freya/freya/services/inventory/internal/hosts"
	"github.com/go-freya/freya/services/inventory/internal/memstore"
	"github.com/go-freya/freya/services/inventory/internal/registry"
	"github.com/go-freya/freya/services/inventory/internal/sealed"
	"github.com/go-freya/freya/services/inventory/internal/snapshots"
	"github.com/go-freya/freya/services/inventory/internal/stats"
	"github.com/go-freya/freya/services/inventory/internal/store"
	"github.com/go-freya/freya/services/inventory/internal/stream"
)

const (
	apiTenant = "11111111-1111-7111-8111-111111111111"
	apiAdmin  = "22222222-2222-7222-8222-222222222222"
)

// fakeVerifier maps a bearer token to a fixed identity.
type fakeVerifier struct {
	ids map[string]authclient.Identity
}

func (f fakeVerifier) Verify(_ context.Context, token string) (authclient.Identity, error) {
	if id, ok := f.ids[token]; ok {
		return id, nil
	}
	return authclient.Identity{}, ErrUnauthenticated
}

type apiFixture struct {
	s   *Server
	mem *memstore.Mem
}

func newAPI(t *testing.T) *apiFixture {
	t.Helper()
	mem := memstore.New()
	rt := testrt.New(t, testutil.MustCA("example.org"), "inventory")

	hostsSvc := hosts.New(mem)
	snapsSvc := snapshots.New(mem, hostsSvc, events.HubPublisher{}) // nil hub → no-op publisher
	env, err := sealed.NewEnvelope(make([]byte, 32))
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}

	v := fakeVerifier{ids: map[string]authclient.Identity{
		"admin": {UserID: apiAdmin, TenantID: apiTenant, Roles: []string{"admin"}},
	}}
	s, err := NewHandler(rt, WithVerifier(v))
	if err != nil {
		t.Fatal(err)
	}
	s.Register(Deps{
		Hosts: hostsSvc, Snapshots: snapsSvc, Stats: stats.New(mem),
		Backup: backup.New(mem), Enroll: enroll.New(mem, env), Registry: registry.NewMemory(),
	})
	return &apiFixture{s: s, mem: mem}
}

// req drives one JSON request as the caller (empty tok => no Authorization).
// Mutating methods carry the CSRF header the OpenAPI validator requires.
func (f *apiFixture) req(t *testing.T, method, path, tok, body string) *httptest.ResponseRecorder {
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
	return w
}

// seed ingests a snapshot for tenant apiTenant and returns the resolved host id.
func (f *apiFixture) seed(t *testing.T, hostname string, inv store.Inventory) string {
	t.Helper()
	snapsSvc := snapshots.New(f.mem, hosts.New(f.mem), events.HubPublisher{})
	inv.Identity.Hostname = hostname
	if inv.CollectedAt.IsZero() {
		inv.CollectedAt = time.Now().UTC()
	}
	snap, err := snapsSvc.Ingest(context.Background(), apiTenant, inv, store.SourceAgent)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return snap.HostID
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return m
}

const p = "/api/inventory/v1"

func TestUnauthenticated(t *testing.T) {
	f := newAPI(t)
	if w := f.req(t, "GET", p+"/hosts", "", ""); w.Code != 401 {
		t.Fatalf("no token: want 401, got %d (%s)", w.Code, w.Body)
	}
	if w := f.req(t, "GET", p+"/hosts", "bogus", ""); w.Code != 401 {
		t.Fatalf("bad token: want 401, got %d", w.Code)
	}
}

func TestHostsListGetTagsRetireDelete(t *testing.T) {
	f := newAPI(t)
	hostID := f.seed(t, "host-a", store.Inventory{
		Identity: store.Identity{HardwareUUID: "hw-1", MachineID: "m-1"},
		System:   store.SystemInfo{SerialNumber: "SER-1", Manufacturer: "Dell", ProductName: "Optiplex"},
		OS:       store.OSInfo{Name: "Windows", Version: "11"},
	})

	w := f.req(t, "GET", p+"/hosts", "admin", "")
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	items, _ := decodeBody(t, w)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want 1 host, got %d", len(items))
	}

	w = f.req(t, "GET", p+"/hosts/"+hostID, "admin", "")
	if w.Code != 200 || decodeBody(t, w)["hostname"] != "host-a" {
		t.Fatalf("get host: %d %s", w.Code, w.Body)
	}

	// Unknown host is a 404.
	if w := f.req(t, "GET", p+"/hosts/018f0000-0000-7000-8000-0000000000ff", "admin", ""); w.Code != 404 {
		t.Fatalf("missing host: want 404, got %d", w.Code)
	}

	w = f.req(t, "POST", p+"/hosts/"+hostID+"/tags", "admin", `{"tags":{"env":"prod"}}`)
	if w.Code != 200 {
		t.Fatalf("tags: %d %s", w.Code, w.Body)
	}
	tags, _ := decodeBody(t, w)["tags"].(map[string]any)
	if tags["env"] != "prod" {
		t.Fatalf("tags not set: %s", w.Body)
	}

	w = f.req(t, "POST", p+"/hosts/"+hostID+"/retire", "admin", "")
	if w.Code != 200 || decodeBody(t, w)["status"] != store.HostRetired {
		t.Fatalf("retire: %d %s", w.Code, w.Body)
	}

	if w := f.req(t, "DELETE", p+"/hosts/"+hostID, "admin", ""); w.Code != 204 {
		t.Fatalf("delete: want 204, got %d %s", w.Code, w.Body)
	}
	if w := f.req(t, "GET", p+"/hosts/"+hostID, "admin", ""); w.Code != 404 {
		t.Fatalf("get after delete: want 404, got %d", w.Code)
	}
}

func TestSnapshotsHistoryDiffChanges(t *testing.T) {
	f := newAPI(t)
	id := store.Identity{HardwareUUID: "hw-2", MachineID: "m-2"}
	hostID := f.seed(t, "host-b", store.Inventory{
		Identity: id, OS: store.OSInfo{Name: "Ubuntu", Version: "22.04"},
		Programs: []store.Program{{Name: "curl", Version: "7.0"}},
	})
	// Second snapshot with a changed program set (produces change history).
	f.seed(t, "host-b", store.Inventory{
		Identity: id, OS: store.OSInfo{Name: "Ubuntu", Version: "22.04"},
		Programs: []store.Program{{Name: "curl", Version: "8.0"}},
	})

	// Latest snapshot has a payload.
	w := f.req(t, "GET", p+"/hosts/"+hostID+"/latest", "admin", "")
	if w.Code != 200 {
		t.Fatalf("latest: %d %s", w.Code, w.Body)
	}
	if _, ok := decodeBody(t, w)["payload"]; !ok {
		t.Fatalf("latest missing payload: %s", w.Body)
	}

	// History lists both snapshots as summaries (no payload).
	w = f.req(t, "GET", p+"/hosts/"+hostID+"/snapshots", "admin", "")
	items, _ := decodeBody(t, w)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("want 2 snapshots, got %d: %s", len(items), w.Body)
	}
	a := items[0].(map[string]any)["id"].(string)
	b := items[1].(map[string]any)["id"].(string)
	if _, hasPayload := items[0].(map[string]any)["payload"]; hasPayload {
		t.Fatalf("summary must omit payload: %s", w.Body)
	}

	// Diff of the two snapshots reports the program change.
	w = f.req(t, "GET", p+"/snapshots/"+a+"/diff/"+b, "admin", "")
	if w.Code != 200 {
		t.Fatalf("diff: %d %s", w.Code, w.Body)
	}
	if ch, _ := decodeBody(t, w)["changes"].([]any); len(ch) == 0 {
		t.Fatalf("diff produced no changes: %s", w.Body)
	}

	// Recorded change history.
	w = f.req(t, "GET", p+"/hosts/"+hostID+"/changes", "admin", "")
	if ch, _ := decodeBody(t, w)["items"].([]any); w.Code != 200 {
		t.Fatalf("changes: %d %s", w.Code, w.Body)
	} else if len(ch) == 0 {
		t.Fatalf("no recorded changes: %s", w.Body)
	}

	// Get one snapshot by id (with payload), then delete it.
	if w := f.req(t, "GET", p+"/snapshots/"+a, "admin", ""); w.Code != 200 {
		t.Fatalf("get snapshot: %d %s", w.Code, w.Body)
	}
	if w := f.req(t, "DELETE", p+"/snapshots/"+a, "admin", ""); w.Code != 204 {
		t.Fatalf("delete snapshot: want 204, got %d", w.Code)
	}
}

func TestEnrollTokenMintSecretOnce(t *testing.T) {
	f := newAPI(t)
	w := f.req(t, "POST", p+"/agents/enroll-token", "admin", `{"label":"laptop-fleet","ttl_seconds":3600}`)
	if w.Code != 201 {
		t.Fatalf("mint: want 201, got %d %s", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	secret, _ := body["token"].(string)
	if secret == "" {
		t.Fatalf("mint returned no secret: %s", w.Body)
	}
	if body["id"] == "" || body["label"] != "laptop-fleet" {
		t.Fatalf("mint response malformed: %s", w.Body)
	}

	// The secret must never surface again: connected-agents listing is empty and
	// carries no credentials.
	w = f.req(t, "GET", p+"/agents", "admin", "")
	if w.Code != 200 {
		t.Fatalf("agents: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatalf("secret leaked in agents listing")
	}

	// Refresh with no connected agent reports delivered=false but still returns a
	// command id, never a credential.
	w = f.req(t, "POST", p+"/agents/018f0000-0000-7000-8000-0000000000aa/refresh", "admin", "")
	if w.Code != 200 {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	if decodeBody(t, w)["delivered"] != false {
		t.Fatalf("expected delivered=false: %s", w.Body)
	}
}

func TestStatisticsTenant(t *testing.T) {
	f := newAPI(t)
	f.seed(t, "host-c", store.Inventory{Identity: store.Identity{HardwareUUID: "hw-3"}, OS: store.OSInfo{Name: "macOS"}})

	w := f.req(t, "GET", p+"/statistics/tenant", "admin", "")
	if w.Code != 200 {
		t.Fatalf("tenant stats: %d %s", w.Code, w.Body)
	}
	if decodeBody(t, w)["hosts_total"].(float64) != 1 {
		t.Fatalf("hosts_total: %s", w.Body)
	}
}

func TestBackupExportImport(t *testing.T) {
	f := newAPI(t)
	f.seed(t, "host-d", store.Inventory{
		Identity: store.Identity{HardwareUUID: "hw-4"}, OS: store.OSInfo{Name: "Windows"},
	})

	w := f.req(t, "POST", p+"/backup/export", "admin", `{"include_history":true}`)
	if w.Code != 200 {
		t.Fatalf("export: %d %s", w.Code, w.Body)
	}
	backupJSON := w.Body.String()
	if !strings.Contains(backupJSON, `"schema_version":1`) {
		t.Fatalf("export missing schema version: %s", backupJSON)
	}

	// Import the exported backup into a fresh service with overwrite mode.
	f2 := newAPI(t)
	imp := `{"mode":"overwrite","backup":` + backupJSON + `}`
	w = f2.req(t, "POST", p+"/backup/import", "admin", imp)
	if w.Code != 200 {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	if decodeBody(t, w)["hosts_imported"].(float64) != 1 {
		t.Fatalf("import result: %s", w.Body)
	}

	// A bad schema version is a 422.
	w = f2.req(t, "POST", p+"/backup/import", "admin", `{"mode":"skip","backup":{"schema_version":99}}`)
	if w.Code != 422 {
		t.Fatalf("bad schema: want 422, got %d %s", w.Code, w.Body)
	}
}

// newAPIWithHub builds a fixture whose stream hub is wired, enabling GET /stream.
func newAPIWithHub(t *testing.T) *apiFixture {
	t.Helper()
	f := newAPI(t)
	// Re-register a fresh server with a hub so GET /stream is mounted.
	mem := memstore.New()
	rt := testrt.New(t, testutil.MustCA("example.org"), "inventory")
	hostsSvc := hosts.New(mem)
	snapsSvc := snapshots.New(mem, hostsSvc, events.HubPublisher{})
	env, _ := sealed.NewEnvelope(make([]byte, 32))
	hub := stream.NewHub(stream.NewMemory(), stream.Config{}, nil)
	t.Cleanup(hub.Close)
	v := fakeVerifier{ids: map[string]authclient.Identity{
		"admin": {UserID: apiAdmin, TenantID: apiTenant, Roles: []string{"admin"}},
	}}
	s, err := NewHandler(rt, WithVerifier(v))
	if err != nil {
		t.Fatal(err)
	}
	s.Register(Deps{
		Hosts: hostsSvc, Snapshots: snapsSvc, Stats: stats.New(mem),
		Backup: backup.New(mem), Enroll: enroll.New(mem, env), Registry: registry.NewMemory(), Hub: hub,
	})
	f.s = s
	f.mem = mem
	return f
}

func TestStreamNotWiredIs501(t *testing.T) {
	f := newAPI(t) // no hub
	if w := f.req(t, "GET", p+"/stream", "admin", ""); w.Code != 501 {
		t.Fatalf("stream without hub: want 501, got %d %s", w.Code, w.Body)
	}
}

func TestStreamSSE(t *testing.T) {
	f := newAPIWithHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("GET", "https://localhost"+p+"/stream", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer admin")
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("stream: want 200, got %d %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type: %q", ct)
	}
}
