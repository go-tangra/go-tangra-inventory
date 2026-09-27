package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra/v4/freyatest/testrt"
	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/backup"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/releases"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/stats"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrades"
)

type nopAudit struct{}

func (nopAudit) Record(context.Context, audit.Event) error { return nil }

type upgradeAPI struct {
	s   *Server
	mem *memstore.Mem
	reg *registry.Memory
	upg *upgrades.Service
}

// newUpgradeAPI wires the API with releases 4.4.0/4.5.0 (linux/amd64 deb)
// and the upgrade service.
func newUpgradeAPI(t *testing.T) *upgradeAPI {
	t.Helper()
	mem := memstore.New()
	rt := testrt.New(t, testutil.MustCA("example.org"), "inventory")
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := agentrelease.Keyring{"test-key": pub}
	dir := t.TempDir()
	for _, v := range []string{"4.4.0", "4.5.0"} {
		d := filepath.Join(dir, v)
		_ = os.MkdirAll(d, 0o755)
		data := []byte("agent " + v)
		sum := sha256.Sum256(data)
		_ = os.WriteFile(filepath.Join(d, "a.deb"), data, 0o644)
		m := agentrelease.Manifest{Schema: 1, Version: v, CreatedAt: time.Now().UTC().Truncate(time.Second), KeyID: "test-key",
			Artifacts: []agentrelease.Artifact{{OS: "linux", Arch: "amd64", InstallType: "deb", File: "a.deb", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
		b := m.Encode()
		_ = os.WriteFile(filepath.Join(d, agentrelease.ManifestFile), b, 0o644)
		_ = os.WriteFile(filepath.Join(d, agentrelease.SignatureFile), []byte(agentrelease.EncodeSignature(ed25519.Sign(priv, b))), 0o644)
	}
	rel := releases.New(mem, keys, nopAudit{}, slog.New(slog.DiscardHandler), releases.Config{BundleDir: dir, KeepVersions: 5, ChunkBytes: 65536})
	rel.SeedBundles(context.Background())
	reg := registry.NewMemory()
	upg := upgrades.New(mem, rel, reg, events.HubPublisher{}, upgrades.Config{RequestTTL: time.Hour, ProgressTimeout: 15 * time.Minute})
	hostsSvc := hosts.New(mem)
	env, _ := sealed.NewEnvelope(make([]byte, 32))
	v := fakeVerifier{ids: map[string]authclient.Identity{
		"admin":    {UserID: apiAdmin, TenantID: apiTenant, Roles: []string{"admin"}},
		"operator": {UserID: "33333333-3333-7333-8333-333333333333", TenantID: apiTenant, Roles: []string{"operator"}},
	}}
	s, err := NewHandler(rt, WithVerifier(v))
	if err != nil {
		t.Fatal(err)
	}
	s.Register(Deps{Hosts: hostsSvc, Snapshots: snapshots.New(mem, hostsSvc, events.HubPublisher{}), Stats: stats.New(mem),
		Backup: backup.New(mem), Enroll: enroll.New(mem, env), Registry: reg, Upgrades: upg, Releases: rel})
	return &upgradeAPI{s: s, mem: mem, reg: reg, upg: upg}
}

func (f *upgradeAPI) req(t *testing.T, method, path, tok, body string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://localhost"+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if csrf {
		r.Header.Set("X-CSRF-Token", "t")
	}
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	return w
}

// agent adds an enrolled agent (id is a uuid-shaped string).
func (f *upgradeAPI) agent(t *testing.T, n int, version string, capable bool) string {
	t.Helper()
	id := fmt.Sprintf("018f0000-0000-7000-8000-%012d", n)
	if err := f.mem.CreateAgent(context.Background(), store.Agent{ID: id, TenantID: apiTenant, AgentVersion: version}); err != nil {
		t.Fatal(err)
	}
	var caps []string
	if capable {
		caps = []string{store.CapUpgradeV1}
	}
	if err := f.mem.SetAgentPlatform(context.Background(), id, "linux", "amd64", "deb", caps, time.Now()); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestFleetList(t *testing.T) {
	f := newUpgradeAPI(t)
	a1 := f.agent(t, 1, "4.4.0", true)
	f.agent(t, 2, "4.5.0", true)
	f.agent(t, 3, "4.3.1", false)
	_, unregister, _ := f.reg.Register(context.Background(), registry.ConnectedAgent{AgentID: a1, TenantID: apiTenant, Version: "4.4.0"})
	defer unregister()
	w := f.req(t, "GET", p+"/agents", "admin", "", false)
	body := decodeBody(t, w)
	items, _ := body["items"].([]any)
	if w.Code != 200 || len(items) != 3 || body["current_version"] != "4.5.0" {
		t.Fatalf("fleet = %d %s", w.Code, w.Body)
	}
	first := items[0].(map[string]any)
	for _, k := range []string{"agent_id", "version", "online", "upgrade_state", "target_version", "last_seen", "connected_at"} {
		if _, ok := first[k]; !ok {
			t.Errorf("fleet entry lacks %q: %v", k, first)
		}
	}
	if first["online"] != true || first["upgrade_state"] != "available" {
		t.Fatalf("a1 = %v", first)
	}
	w = f.req(t, "GET", p+"/agents?state=manual_upgrade_required", "admin", "", false)
	if items, _ := decodeBody(t, w)["items"].([]any); len(items) != 1 {
		t.Fatalf("state filter = %s", w.Body)
	}
	w = f.req(t, "GET", p+"/agents?outdated=true&limit=1", "admin", "", false)
	if items, _ := decodeBody(t, w)["items"].([]any); len(items) != 1 {
		t.Fatalf("outdated+limit = %s", w.Body)
	}
	w = f.req(t, "GET", p+"/agents?cursor="+a1, "admin", "", false)
	if items, _ := decodeBody(t, w)["items"].([]any); len(items) != 2 {
		t.Fatalf("cursor = %s", w.Body)
	}
	if w := f.req(t, "GET", p+"/agents?limit=501", "admin", "", false); w.Code != 422 {
		t.Fatalf("limit bound = %d", w.Code)
	}
	w = f.req(t, "GET", p+"/agents/"+a1, "admin", "", false)
	if w.Code != 200 || decodeBody(t, w)["agent_id"] != a1 {
		t.Fatalf("get agent = %d %s", w.Code, w.Body)
	}
	if w := f.req(t, "GET", p+"/agents/018f0000-0000-7000-8000-999999999999", "admin", "", false); w.Code != 404 {
		t.Fatalf("unknown agent = %d", w.Code)
	}
	if w := f.req(t, "GET", p+"/agents", "", "", false); w.Code != 401 {
		t.Fatalf("no token = %d", w.Code)
	}
	f.mem.FailNext("ListAgents")
	if w := f.req(t, "GET", p+"/agents", "admin", "", false); w.Code != 503 {
		t.Fatalf("store error = %d", w.Code)
	}
}

func TestUpgradeRequestsAndCancel(t *testing.T) {
	f := newUpgradeAPI(t)
	a1 := f.agent(t, 1, "4.4.0", true)
	a2 := f.agent(t, 2, "4.4.0", true)
	a3 := f.agent(t, 3, "4.3.1", false)
	w := f.req(t, "POST", p+"/agents/upgrades", "operator", `{"agent_ids":["`+a1+`","`+a3+`"]}`, true)
	body := decodeBody(t, w)
	created, _ := body["created"].([]any)
	skipped, _ := body["skipped"].([]any)
	if w.Code != 202 || body["target_version"] != "4.5.0" || len(created) != 1 || len(skipped) != 1 ||
		skipped[0].(map[string]any)["reason"] != "manual_upgrade_required" {
		t.Fatalf("request = %d %s", w.Code, w.Body)
	}
	id := created[0].(map[string]any)["id"].(string)
	w = f.req(t, "POST", p+"/agents/upgrades", "admin", `{"all_outdated":true}`, true)
	if created, _ := decodeBody(t, w)["created"].([]any); w.Code != 202 || len(created) != 1 || created[0].(map[string]any)["agent_id"] != a2 {
		t.Fatalf("all outdated = %d %s", w.Code, w.Body)
	}
	for name, body := range map[string]string{
		"both":          `{"agent_ids":["` + a1 + `"],"all_outdated":true}`,
		"neither":       `{}`,
		"unknown field": `{"agent_ids":["` + a1 + `"],"target_version":"4.3.0"}`,
		"not json":      `{`,
		"all false":     `{"all_outdated":false}`,
	} {
		if w := f.req(t, "POST", p+"/agents/upgrades", "admin", body, true); w.Code != 400 {
			t.Errorf("%s = %d %s", name, w.Code, w.Body)
		}
	}
	ids := make([]string, upgrades.MaxBatch+1)
	for i := range ids {
		ids[i] = fmt.Sprintf(`"018f0000-0000-7000-8000-%012d"`, i)
	}
	if w := f.req(t, "POST", p+"/agents/upgrades", "admin", `{"agent_ids":[`+strings.Join(ids, ",")+`]}`, true); w.Code != 400 {
		t.Fatalf("> 1000 ids = %d", w.Code)
	}
	big := `{"agent_ids":["` + strings.Repeat("x", 70<<10) + `"]}`
	if w := f.req(t, "POST", p+"/agents/upgrades", "admin", big, true); w.Code != 413 {
		t.Fatalf("oversized body = %d", w.Code)
	}
	if w := f.req(t, "POST", p+"/agents/upgrades", "admin", `{"all_outdated":true}`, false); w.Code < 400 {
		t.Fatalf("missing CSRF accepted: %d", w.Code)
	}
	// Listing.
	w = f.req(t, "GET", p+"/agents/upgrades?agent_id="+a1, "admin", "", false)
	if items, _ := decodeBody(t, w)["items"].([]any); w.Code != 200 || len(items) != 1 {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}
	w = f.req(t, "GET", p+"/agents/upgrades?limit=1", "admin", "", false)
	b := decodeBody(t, w)
	if items, _ := b["items"].([]any); len(items) != 1 || b["next_cursor"] == "" {
		t.Fatalf("paged list = %s", w.Body)
	}
	w = f.req(t, "GET", p+"/agents/upgrades?limit=1&cursor="+b["next_cursor"].(string), "admin", "", false)
	if b2 := decodeBody(t, w); b2["next_cursor"] != "" {
		t.Fatalf("last page = %s", w.Body)
	}
	w = f.req(t, "GET", p+"/agents/upgrades?state=pending", "admin", "", false)
	if items, _ := decodeBody(t, w)["items"].([]any); len(items) != 2 {
		t.Fatalf("state filter = %s", w.Body)
	}
	// Cancel.
	w = f.req(t, "POST", p+"/agents/upgrades/"+id+"/cancel", "admin", "", true)
	if w.Code != 200 || decodeBody(t, w)["state"] != "cancelled" {
		t.Fatalf("cancel = %d %s", w.Code, w.Body)
	}
	if w := f.req(t, "POST", p+"/agents/upgrades/"+id+"/cancel", "admin", "", true); w.Code != 409 || decodeBody(t, w)["reason"] != "not_cancellable" {
		t.Fatalf("cancel twice = %d %s", w.Code, w.Body)
	}
	if w := f.req(t, "POST", p+"/agents/upgrades/018f0000-0000-7000-8000-999999999999/cancel", "admin", "", true); w.Code != 404 {
		t.Fatalf("cancel unknown = %d", w.Code)
	}
	f.mem.FailNext("ListAgentUpgrades")
	if w := f.req(t, "GET", p+"/agents/upgrades", "admin", "", false); w.Code != 503 {
		t.Fatalf("list error = %d", w.Code)
	}
	f.mem.FailNext("ListAgents")
	if w := f.req(t, "POST", p+"/agents/upgrades", "admin", `{"all_outdated":true}`, true); w.Code != 503 {
		t.Fatalf("request error = %d", w.Code)
	}
}

func TestAgentReleasesList(t *testing.T) {
	f := newUpgradeAPI(t)
	w := f.req(t, "GET", p+"/agent-releases", "admin", "", false)
	b := decodeBody(t, w)
	items, _ := b["items"].([]any)
	if w.Code != 200 || b["current_version"] != "4.5.0" || len(items) != 2 {
		t.Fatalf("releases = %d %s", w.Code, w.Body)
	}
	first := items[0].(map[string]any)
	plats, _ := first["platforms"].([]any)
	if first["version"] != "4.5.0" || first["source"] != "bundled" || first["key_id"] != "test-key" || len(plats) != 1 ||
		plats[0].(map[string]any)["install_type"] != "deb" {
		t.Fatalf("release = %v", first)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("manifest")) || bytes.Contains(w.Body.Bytes(), []byte("signature")) {
		t.Fatal("manifest or signature bytes returned")
	}
	f.mem.FailNext("ListAgentReleases")
	if w := f.req(t, "GET", p+"/agent-releases", "admin", "", false); w.Code != 503 {
		t.Fatalf("store error = %d", w.Code)
	}
}

func TestUpgradeRoutesNotMountedWithoutService(t *testing.T) {
	f := newAPIFull(t)
	if w := f.req(t, "GET", p+"/agent-releases", "admin", ""); w.Code != 501 {
		t.Fatalf("releases without service = %d", w.Code)
	}
}

func TestUpgradePolicyRoutes(t *testing.T) {
	f := newUpgradeAPI(t)
	w := f.req(t, "GET", p+"/agents/upgrade-policy", "operator", "", false)
	b := decodeBody(t, w)
	if w.Code != 200 || b["enabled"] != false || b["timezone"] != "UTC" || b["max_concurrent"] != float64(5) || b["paused"] != false {
		t.Fatalf("defaults = %d %s", w.Code, w.Body)
	}
	good := `{"enabled":true,"window_start":"22:00","window_end":"02:00","timezone":"Europe/Sofia","max_concurrent":3,"target_version":"4.4.0"}`
	w = f.req(t, "PUT", p+"/agents/upgrade-policy", "admin", good, true)
	b = decodeBody(t, w)
	if w.Code != 200 || b["enabled"] != true || b["target_version"] != "4.4.0" || b["updated_by"] != apiAdmin || b["window_end"] != "02:00" {
		t.Fatalf("put = %d %s", w.Code, w.Body)
	}
	var updated *store.AuditRow
	for _, r := range f.mem.AuditRows() {
		if r.Action == "upgrade_policy_updated" {
			updated = &r
		}
	}
	if updated == nil || updated.ActorID != apiAdmin || updated.SubjectKind != "upgrade_policy" {
		t.Fatalf("audit = %+v", f.mem.AuditRows())
	}
	ch := updated.Detail["changes"].(map[string]any)
	if ch["target_version"].(map[string]any)["before"] != "" || ch["target_version"].(map[string]any)["after"] != "4.4.0" {
		t.Fatalf("changes = %+v", ch)
	}
	// A pin lower than an agent's version downgrades it (the pin path).
	a := f.agent(t, 1, "4.5.0", true)
	w = f.req(t, "POST", p+"/agents/upgrades", "admin", `{"agent_ids":["`+a+`"]}`, true)
	created, _ := decodeBody(t, w)["created"].([]any)
	if len(created) != 1 || created[0].(map[string]any)["allow_downgrade"] != true || created[0].(map[string]any)["target_version"] != "4.4.0" {
		t.Fatalf("pinned downgrade = %s", w.Body)
	}

	for name, c := range map[string]struct {
		body string
		code int
	}{
		"bad window":       {`{"enabled":true,"window_start":"2:00","window_end":"04:00","timezone":"UTC","max_concurrent":3,"target_version":""}`, 400},
		"bad timezone":     {`{"enabled":true,"window_start":"02:00","window_end":"04:00","timezone":"Mars/Olympus","max_concurrent":3,"target_version":""}`, 400},
		"zero concurrent":  {`{"enabled":true,"window_start":"02:00","window_end":"04:00","timezone":"UTC","max_concurrent":0,"target_version":""}`, 400},
		"101 concurrent":   {`{"enabled":true,"window_start":"02:00","window_end":"04:00","timezone":"UTC","max_concurrent":101,"target_version":""}`, 400},
		"unknown field":    {`{"enabled":true,"window_start":"02:00","window_end":"04:00","timezone":"UTC","max_concurrent":3,"target_version":"","paused":false}`, 400},
		"missing field":    {`{"enabled":true,"window_start":"02:00","window_end":"04:00","timezone":"UTC","max_concurrent":3}`, 400},
		"unknown version":  {`{"enabled":true,"window_start":"02:00","window_end":"04:00","timezone":"UTC","max_concurrent":3,"target_version":"4.9.9"}`, 409},
		"garbage version":  {`{"enabled":true,"window_start":"02:00","window_end":"04:00","timezone":"UTC","max_concurrent":3,"target_version":"latest"}`, 400},
		"not json":         {`{`, 400},
		"oversized (4KiB)": {`{"enabled":true,"window_start":"02:00","window_end":"04:00","timezone":"` + strings.Repeat("x", 5000) + `","max_concurrent":3,"target_version":""}`, 413},
	} {
		if w := f.req(t, "PUT", p+"/agents/upgrade-policy", "admin", c.body, true); w.Code != c.code {
			t.Errorf("%s = %d %s", name, w.Code, w.Body)
		}
	}
	if w := f.req(t, "PUT", p+"/agents/upgrade-policy", "admin", good, false); w.Code < 400 {
		t.Fatalf("missing CSRF accepted: %d", w.Code)
	}
	if w := f.req(t, "POST", p+"/agents/upgrade-policy/resume", "admin", "", false); w.Code < 400 {
		t.Fatalf("resume without CSRF accepted: %d", w.Code)
	}

	// Pause through a failed policy request, then resume (audited).
	f.upg.PauseOnFailure(context.Background(), store.AgentUpgrade{ID: "r1", TenantID: apiTenant, Origin: store.OriginPolicy, State: store.UpgradeFailed, Reason: "install_failed"})
	w = f.req(t, "GET", p+"/agents/upgrade-policy", "admin", "", false)
	if b := decodeBody(t, w); b["paused"] != true || b["paused_reason"] != "failed:r1" {
		t.Fatalf("paused = %s", w.Body)
	}
	w = f.req(t, "POST", p+"/agents/upgrade-policy/resume", "admin", "", true)
	if b := decodeBody(t, w); w.Code != 200 || b["paused"] != false {
		t.Fatalf("resume = %d %s", w.Code, w.Body)
	}
	if acts := f.mem.AuditRows(); acts[len(acts)-1].Action != "upgrade_policy_resumed" {
		t.Fatalf("resume audit = %v", acts[len(acts)-1])
	}
	if w := f.req(t, "POST", p+"/agents/upgrade-policy/resume", "admin", strings.Repeat("x", 2048), true); w.Code != 413 {
		t.Fatalf("oversized resume = %d", w.Code)
	}

	// Store failures and missing identity.
	f.mem.FailNext("GetUpgradePolicy")
	if w := f.req(t, "GET", p+"/agents/upgrade-policy", "admin", "", false); w.Code != 503 {
		t.Fatalf("get error = %d", w.Code)
	}
	f.mem.FailNext("UpdateUpgradePolicy")
	if w := f.req(t, "PUT", p+"/agents/upgrade-policy", "admin", good, true); w.Code != 503 {
		t.Fatalf("put error = %d", w.Code)
	}
	f.mem.FailNext("UpdateUpgradePolicy")
	if w := f.req(t, "POST", p+"/agents/upgrade-policy/resume", "admin", "", true); w.Code != 503 {
		t.Fatalf("resume error = %d", w.Code)
	}
	for _, c := range []struct{ m, path, body string }{{"GET", "/agents/upgrade-policy", ""}, {"PUT", "/agents/upgrade-policy", good}, {"POST", "/agents/upgrade-policy/resume", ""}} {
		if w := f.req(t, c.m, p+c.path, "", c.body, true); w.Code != 401 {
			t.Errorf("%s %s without token = %d", c.m, c.path, w.Code)
		}
	}
}
