package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra/v4/freyatest/testrt"
	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"
	"github.com/go-tangra/go-tangra/v4/transport/edge"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/authz"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/backup"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/stats"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const apiUser = "33333333-3333-7333-8333-333333333333"

func edgeConfig() edge.Config { return edge.Config{Addr: "127.0.0.1:0"} }

// fullFixture is like apiFixture but keeps references to the registry and enroll
// service so tests can pre-register connected agents and enroll credentials.
type fullFixture struct {
	*apiFixture
	reg    *registry.Memory
	enroll *enroll.Service
}

func newAPIFull(t *testing.T) *fullFixture {
	t.Helper()
	mem := memstore.New()
	rt := testrt.New(t, testutil.MustCA("example.org"), "inventory")
	hostsSvc := hosts.New(mem)
	snapsSvc := snapshots.New(mem, hostsSvc, events.HubPublisher{})
	env, err := sealed.NewEnvelope(make([]byte, 32))
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	reg := registry.NewMemory()
	enr := enroll.New(mem, env)
	v := fakeVerifier{ids: map[string]authclient.Identity{
		"admin": {UserID: apiAdmin, TenantID: apiTenant, Roles: []string{"admin"}},
		"user":  {UserID: apiUser, TenantID: apiTenant, Roles: []string{"member"}},
	}}
	s, err := NewHandler(rt, WithVerifier(v))
	if err != nil {
		t.Fatal(err)
	}
	s.Register(Deps{
		Hosts: hostsSvc, Snapshots: snapsSvc, Stats: stats.New(mem),
		Backup: backup.New(mem), Enroll: enr, Registry: reg,
	})
	return &fullFixture{apiFixture: &apiFixture{s: s, mem: mem}, reg: reg, enroll: enr}
}

func TestStatisticsSystemAdminVsNonAdmin(t *testing.T) {
	f := newAPIFull(t)
	f.seed(t, "host-sys", store.Inventory{Identity: store.Identity{HardwareUUID: "hw-sys"}})

	if w := f.req(t, "GET", p+"/statistics/system", "admin", ""); w.Code != 200 {
		t.Fatalf("system stats admin: want 200, got %d %s", w.Code, w.Body)
	} else if _, ok := decodeBody(t, w)["tenants"]; !ok {
		t.Fatalf("system stats missing tenants: %s", w.Body)
	}
	if w := f.req(t, "GET", p+"/statistics/system", "user", ""); w.Code != 403 {
		t.Fatalf("system stats non-admin: want 403, got %d %s", w.Code, w.Body)
	}
}

func TestAgentsRefreshDeliveredAndRevoke(t *testing.T) {
	f := newAPIFull(t)

	// Pre-register a live agent whose HostID matches the refresh target.
	hostID := "018f0000-0000-7000-8000-0000000000bb"
	_, unregister, err := f.reg.Register(context.Background(), registry.ConnectedAgent{
		AgentID: "agent-live", TenantID: apiTenant, HostID: hostID, Version: "1.0",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer unregister()

	// The connected agent shows up in the listing.
	w := f.req(t, "GET", p+"/agents", "admin", "")
	if items, _ := decodeBody(t, w)["items"].([]any); w.Code != 200 || len(items) != 1 {
		t.Fatalf("agents listing: %d %s", w.Code, w.Body)
	}

	// Refresh delivers to the connected agent (delivered=true).
	w = f.req(t, "POST", p+"/agents/"+hostID+"/refresh", "admin", "")
	if w.Code != 200 {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	if decodeBody(t, w)["delivered"] != true {
		t.Fatalf("expected delivered=true: %s", w.Body)
	}

	// Enroll an agent, then revoke it via the HTTP route (happy path).
	secret, _, err := f.enroll.MintToken(context.Background(), apiTenant, apiAdmin, "lab", 3600_000_000_000)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	agentID, _, err := f.enroll.Enroll(context.Background(), secret, store.Identity{Hostname: "h"}, "1.0")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	w = f.req(t, "POST", p+"/agents/"+agentID+"/revoke", "admin", "")
	if w.Code != 200 || decodeBody(t, w)["revoked"] != true {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
}

func TestEnrollTokenAndExportNoBody(t *testing.T) {
	f := newAPIFull(t)
	f.seed(t, "host-x", store.Inventory{Identity: store.Identity{HardwareUUID: "hw-x"}})

	// enroll-token with no body uses the default TTL (exercises the empty-body branch).
	if w := f.req(t, "POST", p+"/agents/enroll-token", "admin", ""); w.Code != 201 {
		t.Fatalf("enroll-token no body: want 201, got %d %s", w.Code, w.Body)
	}
	// backup export with no body defaults include_history=false.
	if w := f.req(t, "POST", p+"/backup/export", "admin", ""); w.Code != 200 {
		t.Fatalf("export no body: want 200, got %d %s", w.Code, w.Body)
	}
}

func TestFailSvcMappings(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"hosts not found", hosts.ErrNotFound, http.StatusNotFound},
		{"snapshots not found", snapshots.ErrNotFound, http.StatusNotFound},
		{"forbidden", authz.ErrForbidden, http.StatusForbidden},
		{"bad schema", backup.ErrBadSchema, http.StatusUnprocessableEntity},
		{"token invalid", enroll.ErrTokenInvalid, http.StatusUnprocessableEntity},
		{"unauthenticated", ErrUnauthenticated, http.StatusUnauthorized},
		{"enroll unauth", enroll.ErrUnauthenticated, http.StatusUnauthorized},
		{"other", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			failSvc(w, tc.err)
			if w.Code != tc.want {
				t.Fatalf("failSvc(%v) = %d, want %d", tc.err, w.Code, tc.want)
			}
		})
	}
	// repo.ErrConflict -> 409.
	w := httptest.NewRecorder()
	failSvc(w, repo.ErrConflict)
	if w.Code != http.StatusConflict {
		t.Fatalf("failSvc(conflict) = %d, want 409", w.Code)
	}
	// repo.ErrNotFound -> 404.
	w = httptest.NewRecorder()
	failSvc(w, repo.ErrNotFound)
	if w.Code != http.StatusNotFound {
		t.Fatalf("failSvc(repo not found) = %d, want 404", w.Code)
	}
}

func TestStatusAndFail(t *testing.T) {
	if code, reason := Status(ErrForbidden); code != 403 || reason != "forbidden" {
		t.Fatalf("Status(*Error): %d %q", code, reason)
	}
	if code, _ := Status(store.ErrNotFound); code != 404 {
		t.Fatalf("Status(store.ErrNotFound): %d", code)
	}
	if code, _ := Status(store.ErrConflict); code != 409 {
		t.Fatalf("Status(store.ErrConflict): %d", code)
	}
	if code, _ := Status(errors.New("x")); code != 503 {
		t.Fatalf("Status(unknown): %d", code)
	}

	// Fail with a >=500 error and a logger exercises the log branch.
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(HeaderRequestID, "rid-1")
	w := httptest.NewRecorder()
	Fail(w, r, slog.Default(), errors.New("kaboom"))
	if w.Code != 503 {
		t.Fatalf("Fail 500-class: %d", w.Code)
	}
	if RequestID(r) != "rid-1" {
		t.Fatalf("RequestID: %q", RequestID(r))
	}
	// Fail with a <500 error and no logger.
	w = httptest.NewRecorder()
	Fail(w, r, nil, ErrNotFound)
	if w.Code != 404 {
		t.Fatalf("Fail 404: %d", w.Code)
	}
}

func TestErrorAndDetailAndJSONRaw(t *testing.T) {
	if ErrValidation.Error() != "validation_failed" {
		t.Fatalf("Error(): %q", ErrValidation.Error())
	}
	w := httptest.NewRecorder()
	WriteDetail(w, ErrValidation, map[string]any{"field": "x"})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"detail"`) {
		t.Fatalf("WriteDetail: %d %s", w.Code, w.Body)
	}
	if _, ok := jsonRaw([]byte(`{"a":1}`)).(interface{ MarshalJSON() ([]byte, error) }); !ok {
		// json.RawMessage implements Marshaler.
		t.Fatalf("jsonRaw valid should be RawMessage")
	}
	if s, ok := jsonRaw([]byte("not json")).(string); !ok || s != "not json" {
		t.Fatalf("jsonRaw invalid should be string, got %T", jsonRaw([]byte("not json")))
	}
}

func TestExtensionHelpers(t *testing.T) {
	cases := []struct {
		v    any
		want int64
		ok   bool
	}{
		{float64(5), 5, true},
		{int(6), 6, true},
		{int64(7), 7, true},
		{json.Number("8"), 8, true},
		{json.Number("bad"), 0, false},
		{"nope", 0, false},
	}
	for _, c := range cases {
		got, ok := extensionInt(c.v)
		if got != c.want || ok != c.ok {
			t.Fatalf("extensionInt(%v) = %d,%v want %d,%v", c.v, got, ok, c.want, c.ok)
		}
	}
	if !extensionBool(true) || extensionBool(false) || extensionBool("x") {
		t.Fatal("extensionBool")
	}
}

func TestAtoiDefault(t *testing.T) {
	if atoiDefault("", 3) != 3 {
		t.Fatal("empty -> default")
	}
	if atoiDefault("bad", 4) != 4 {
		t.Fatal("invalid -> default")
	}
	if atoiDefault("12", 0) != 12 {
		t.Fatal("valid parse")
	}
}

func TestDecodeJSONErrors(t *testing.T) {
	newReq := func(body string) *http.Request {
		r := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		return r
	}
	var v struct {
		A string `json:"a"`
	}
	if err := DecodeJSON(newReq(`{"a":"ok"}`), &v, 0); err != nil {
		t.Fatalf("valid decode: %v", err)
	}
	if err := DecodeJSON(newReq(`{`), &v, 0); !errors.Is(err, ErrMalformed) {
		t.Fatalf("malformed: %v", err)
	}
	if err := DecodeJSON(newReq(`{"unknown":1}`), &v, 0); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unknown field: %v", err)
	}
	if err := DecodeJSON(newReq(`{"a":"ok"}{"a":"x"}`), &v, 0); !errors.Is(err, ErrMalformed) {
		t.Fatalf("trailing data: %v", err)
	}
	if err := DecodeJSON(newReq(`{"a":"aaaaaaaaaa"}`), &v, 5); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("too large: %v", err)
	}
}

func TestServerAccessors(t *testing.T) {
	f := newAPI(t)
	s := f.s
	if len(s.Declared()) == 0 {
		t.Fatal("Declared empty")
	}
	if len(s.Implemented()) == 0 {
		t.Fatal("Implemented empty")
	}
	// GET /stream is declared but only implemented when a hub is wired, so it is
	// the one route reported missing here.
	missing := s.Missing()
	foundStream := false
	for _, rt := range missing {
		if rt.Path == p+"/stream" {
			foundStream = true
		}
	}
	if !foundStream {
		t.Fatalf("expected /stream in Missing, got %v", missing)
	}
	if s.Document() == nil {
		t.Fatal("Document nil")
	}
	// Handle on an undeclared route is refused.
	if err := s.Handle("GET", "/nope/not-declared", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
		t.Fatal("Handle undeclared should error")
	}
	// IsPublic reflects the document (all inventory routes are protected).
	if s.IsPublic("GET", p+"/hosts") {
		t.Fatal("hosts route should not be public")
	}
	// Route stringer.
	if (Route{Method: "GET", Path: "/x"}).String() != "GET /x" {
		t.Fatal("Route.String")
	}
}

func TestValidateBranches(t *testing.T) {
	f := newAPI(t)
	hostID := f.seed(t, "host-v", store.Inventory{Identity: store.Identity{HardwareUUID: "hw-v"}})

	// Wrong method on a declared path -> 405 method_not_allowed.
	if w := f.req(t, "PUT", p+"/hosts/"+hostID, "admin", ""); w.Code != 405 {
		t.Fatalf("method not allowed: want 405, got %d %s", w.Code, w.Body)
	}
	// Body that violates the OpenAPI schema (tags must be an object) is refused
	// by the validation layer (malformed_body / validation_failed).
	if w := f.req(t, "POST", p+"/hosts/"+hostID+"/tags", "admin", `{"tags":123}`); w.Code != 400 && w.Code != 422 {
		t.Fatalf("schema violation: want 400/422, got %d %s", w.Code, w.Body)
	}
	// Unknown path -> 404 (falls through validate's ErrPathNotFound branch).
	if w := f.req(t, "GET", "/api/inventory/v1/nonexistent", "admin", ""); w.Code != 404 {
		t.Fatalf("unknown path: want 404, got %d", w.Code)
	}
}

func TestNewBindsEdgeListener(t *testing.T) {
	rt := testrt.New(t, testutil.MustCA("example.org"), "inventory")
	v := fakeVerifier{ids: map[string]authclient.Identity{}}
	s, err := New(rt, edgeConfig(), WithVerifier(v))
	if err != nil {
		t.Skipf("edge server unavailable in test env: %v", err)
	}
	if s.Edge() == nil {
		t.Fatal("Edge() nil after New")
	}
	if s.Handler() == nil {
		t.Fatal("Handler() nil after New")
	}

	// Start the listener in the background and stop it promptly.
	errc := make(chan error, 1)
	go func() { errc <- s.Start(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-errc:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestRemoteHandlerServesAssets(t *testing.T) {
	rt := testrt.New(t, testutil.MustCA("example.org"), "inventory")
	dist := fstest.MapFS{
		"index.html":        {Data: []byte("<html></html>")},
		"sw.js":             {Data: []byte("self.x=1")},
		"assets/app.abc.js": {Data: []byte("console.log(1)")},
	}
	v := fakeVerifier{ids: map[string]authclient.Identity{}}
	s, err := NewHandler(rt, WithVerifier(v), WithRemote(dist))
	if err != nil {
		t.Fatal(err)
	}

	do := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://localhost"+path, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}

	// Hashed asset -> immutable cache.
	w := do(RemotePrefix + "/assets/app.abc.js")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d cc=%q", w.Code, w.Header().Get("Cache-Control"))
	}
	// Non-hashed entry file -> no-cache.
	w = do(RemotePrefix + "/sw.js")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("sw.js: %d cc=%q", w.Code, w.Header().Get("Cache-Control"))
	}
	// Missing file -> 404.
	if w := do(RemotePrefix + "/does-not-exist.js"); w.Code != 404 {
		t.Fatalf("missing asset: %d", w.Code)
	}
	// Directory (trailing slash) -> 404.
	if w := do(RemotePrefix + "/"); w.Code != 404 {
		t.Fatalf("directory listing: %d", w.Code)
	}
}
