package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra/v4/freyatest/testrt"
	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/backup"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/stats"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func newAutoAPI(t *testing.T) *upgradeAPI {
	t.Helper()
	mem := memstore.New()
	rt := testrt.New(t, testutil.MustCA("example.org"), "inventory")
	env, _ := sealed.NewEnvelope(make([]byte, 32))
	v := fakeVerifier{ids: map[string]authclient.Identity{
		"admin":    {UserID: apiAdmin, TenantID: apiTenant, Roles: []string{"admin"}},
		"operator": {UserID: "33333333-3333-7333-8333-333333333333", TenantID: apiTenant, Roles: []string{"operator"}},
		"other":    {UserID: "44444444-4444-7444-8444-444444444444", TenantID: "55555555-5555-7555-8555-555555555555", Roles: []string{"admin"}},
	}}
	s, err := NewHandler(rt, WithVerifier(v))
	if err != nil {
		t.Fatal(err)
	}
	hostsSvc := hosts.New(mem)
	s.Register(Deps{Hosts: hostsSvc, Snapshots: snapshots.New(mem, hostsSvc, events.HubPublisher{}), Stats: stats.New(mem),
		Backup: backup.New(mem), Enroll: enroll.New(mem, env), Registry: registry.NewMemory(), AutoEnroll: autoenroll.New(mem, env)})
	return &upgradeAPI{s: s, mem: mem}
}

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("json %q: %v", body, err)
	}
	return m
}

func TestAutoEnrollAPI(t *testing.T) {
	f := newAutoAPI(t)
	const base = p + "/agents/auto-enroll"

	w := f.req(t, "GET", base, "admin", "", false)
	if w.Code != 200 {
		t.Fatalf("get %d %s", w.Code, w.Body)
	}
	got := decode(t, w.Body.String())
	if got["enabled"] != false || got["window_seconds"].(float64) != 300 || len(got["keys"].([]any)) != 0 || got["updated_at"] != nil {
		t.Fatalf("defaults %v", got)
	}
	if w = f.req(t, "PUT", base, "admin", `{"enabled":true}`, true); w.Code != 200 || decode(t, w.Body.String())["enabled"] != true {
		t.Fatalf("put %d %s", w.Code, w.Body)
	}
	if w = f.req(t, "PUT", base, "admin", `{}`, true); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("put empty %d", w.Code)
	}
	if w = f.req(t, "PUT", base, "admin", `{`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("put malformed %d", w.Code)
	}

	w = f.req(t, "POST", base+"/keys", "admin", `{"name":"lab","allowed_cidrs":["10.0.0.0/8"],"max_enrollments":3}`, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	created := decode(t, w.Body.String())
	key := created["key"].(map[string]any)
	secret := created["secret"].(string)
	id := key["id"].(string)
	if !strings.HasPrefix(secret, "aks_") || key["state"] != "active" || key["enrollments"].(float64) != 0 || key["key_id"] == "" {
		t.Fatalf("created %v", created)
	}
	// The secret never appears again.
	w = f.req(t, "GET", base, "admin", "", false)
	if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("secret in listing: %s", w.Body)
	}
	if w = f.req(t, "POST", base+"/keys", "admin", `{"name":"lab","allowed_cidrs":["10.0.0.0/8"]}`, true); w.Code != http.StatusConflict {
		t.Fatalf("dup %d", w.Code)
	}
	w = f.req(t, "POST", base+"/keys", "admin", `{"name":"x","allowed_cidrs":["0.0.0.0/0"]}`, true)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "allowed_cidrs") {
		t.Fatalf("bad cidr %d %s", w.Code, w.Body)
	}
	if w = f.req(t, "POST", base+"/keys", "admin", `{"name":`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed create %d", w.Code)
	}

	// Patch: disable, set and clear expiry, bad expiry.
	w = f.req(t, "PATCH", base+"/keys/"+id, "admin", `{"enabled":false,"expires_at":"2099-01-01T00:00:00Z"}`, true)
	if w.Code != 200 || decode(t, w.Body.String())["state"] != "disabled" || decode(t, w.Body.String())["expires_at"] == nil {
		t.Fatalf("patch %d %s", w.Code, w.Body)
	}
	if w = f.req(t, "PATCH", base+"/keys/"+id, "admin", `{"expires_at":null,"enabled":true}`, true); w.Code != 200 || decode(t, w.Body.String())["expires_at"] != nil {
		t.Fatalf("clear expiry %d %s", w.Code, w.Body)
	}
	if w = f.req(t, "PATCH", base+"/keys/"+id, "admin", `{"expires_at":"soon"}`, true); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad expiry %d %s", w.Code, w.Body)
	}
	if w = f.req(t, "PATCH", base+"/keys/"+id, "admin", `{"name":"  "}`, true); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("blank name %d", w.Code)
	}
	if w = f.req(t, "PATCH", base+"/keys/"+id, "admin", `[`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed patch %d", w.Code)
	}
	if w = f.req(t, "PATCH", base+"/keys/00000000-0000-7000-8000-000000000000", "admin", `{}`, true); w.Code != http.StatusNotFound {
		t.Fatalf("missing patch %d", w.Code)
	}

	// Rotate returns a new secret once.
	w = f.req(t, "POST", base+"/keys/"+id+"/rotate", "admin", "", true)
	if w.Code != 200 || decode(t, w.Body.String())["secret"] == secret {
		t.Fatalf("rotate %d %s", w.Code, w.Body)
	}
	if w = f.req(t, "POST", base+"/keys/00000000-0000-7000-8000-000000000000/rotate", "admin", "", true); w.Code != http.StatusNotFound {
		t.Fatalf("rotate missing %d", w.Code)
	}

	// Another tenant sees and touches nothing.
	if w = f.req(t, "GET", base, "other", "", false); len(decode(t, w.Body.String())["keys"].([]any)) != 0 {
		t.Fatalf("cross-tenant listing %s", w.Body)
	}
	if w = f.req(t, "DELETE", base+"/keys/"+id, "other", "", true); w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete %d", w.Code)
	}
	// No caller, no CSRF.
	if w = f.req(t, "GET", base, "", "", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous %d", w.Code)
	}
	if w = f.req(t, "PUT", base, "admin", `{"enabled":false}`, false); w.Code == 200 {
		t.Fatal("PUT without CSRF accepted")
	}

	if w = f.req(t, "DELETE", base+"/keys/"+id, "admin", "", true); w.Code != http.StatusNoContent {
		t.Fatalf("delete %d %s", w.Code, w.Body)
	}
	if w = f.req(t, "DELETE", base+"/keys/"+id, "admin", "", true); w.Code != http.StatusNotFound {
		t.Fatalf("delete again %d", w.Code)
	}
}

func TestViewAutoKeyStates(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	cases := map[string]store.AutoEnrollKey{
		"active":    {Enabled: true},
		"disabled":  {Enabled: false},
		"expired":   {Enabled: true, ExpiresAt: &past},
		"exhausted": {Enabled: true, MaxEnrollments: 2, Enrollments: 2},
	}
	for want, k := range cases {
		v := viewAutoKey(k, now)
		if v.State != want || v.AllowedCIDRs == nil {
			t.Errorf("%s: %+v", want, v)
		}
	}
}
