package upgrades

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const tenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

var t0 = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

// fakeReleases serves a fixed current version and artifact set.
type fakeReleases struct {
	current   string
	artifacts map[string]bool // version|os|arch|install
	listFail  error
}

func (f *fakeReleases) CurrentVersion(context.Context) string { return f.current }
func (f *fakeReleases) HasArtifact(_ context.Context, v string, p agentrelease.Platform) bool {
	return f.artifacts[v+"|"+p.OS+"|"+p.Arch+"|"+p.InstallType]
}
func (f *fakeReleases) Releases(context.Context) ([]store.AgentRelease, error) {
	if f.listFail != nil {
		return nil, f.listFail
	}
	var out []store.AgentRelease
	seen := map[string]bool{}
	for k := range f.artifacts {
		v := strings.SplitN(k, "|", 2)[0]
		if !seen[v] {
			seen[v] = true
			out = append(out, store.AgentRelease{Version: v})
		}
	}
	return out, nil
}

func (f *fakeReleases) add(v string, p ...string) {
	for _, pl := range p {
		f.artifacts[v+"|"+pl] = true
	}
}

// fakeRegistry records deliveries; online agents get delivered.
type fakeRegistry struct {
	mu        sync.Mutex
	online    map[string]bool
	delivered []registry.Command
	to        []string
	fail      error
	listFail  error
}

func (f *fakeRegistry) Deliver(_ context.Context, agentID string, cmd registry.Command) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return false, f.fail
	}
	if !f.online[agentID] {
		return false, nil
	}
	f.delivered = append(f.delivered, cmd)
	f.to = append(f.to, agentID)
	return true, nil
}

func (f *fakeRegistry) ListConnected(_ context.Context, tenantID string) ([]registry.ConnectedAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listFail != nil {
		return nil, f.listFail
	}
	var out []registry.ConnectedAgent
	for id := range f.online {
		out = append(out, registry.ConnectedAgent{AgentID: id, TenantID: tenantID, ConnectedAt: t0})
	}
	return out, nil
}

// fakePub records realtime events.
type fakePub struct {
	mu     sync.Mutex
	events []map[string]any
	types  []string
}

func (f *fakePub) Publish(_ context.Context, _ string, typ string, payload any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.types = append(f.types, typ)
	f.events = append(f.events, payload.(map[string]any))
}

// countingMetrics counts transitions.
type countingMetrics struct {
	mu     sync.Mutex
	states map[string]int
}

func (c *countingMetrics) Transition(state, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.states[state+":"+reason]++
}

type fixture struct {
	mem   *memstore.Mem
	rel   *fakeReleases
	reg   *fakeRegistry
	pub   *fakePub
	met   *countingMetrics
	svc   *Service
	now   time.Time
	agent map[string]store.Agent
}

const (
	deb = "linux|amd64|deb"
	rpm = "linux|amd64|rpm"
	win = "windows|amd64|binary"
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{mem: memstore.New(), rel: &fakeReleases{current: "4.5.0", artifacts: map[string]bool{}},
		reg: &fakeRegistry{online: map[string]bool{}}, pub: &fakePub{}, met: &countingMetrics{states: map[string]int{}},
		now: t0, agent: map[string]store.Agent{}}
	f.rel.add("4.5.0", deb, rpm, win, "linux|amd64|binary")
	f.rel.add("4.4.0", deb)
	ids := 0
	f.svc = New(f.mem, f.rel, f.reg, f.pub, Config{RequestTTL: 7 * 24 * time.Hour, ProgressTimeout: 15 * time.Minute})
	f.svc.now = func() time.Time { return f.now }
	f.svc.newID = func() string { ids++; return fmt.Sprintf("00000000-0000-7000-8000-%012d", ids) }
	f.svc.SetMetrics(f.met)
	return f
}

// enroll adds an agent with platform and version.
func (f *fixture) enroll(t *testing.T, id, version, platform string, caps ...string) store.Agent {
	t.Helper()
	a := store.Agent{ID: id, TenantID: tenant, HostID: "h-" + id, AgentVersion: version, EnrolledAt: t0, LastSeen: t0}
	if err := f.mem.CreateAgent(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if platform != "" {
		p := strings.Split(platform, "|")
		if err := f.mem.SetAgentPlatform(context.Background(), id, p[0], p[1], p[2], caps, t0); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := f.mem.GetAgent(context.Background(), tenant, id)
	f.agent[id] = got
	return got
}

func (f *fixture) upgrade(t *testing.T, id string) store.AgentUpgrade {
	t.Helper()
	u, err := f.mem.GetAgentUpgrade(context.Background(), tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (f *fixture) actions() []string {
	var out []string
	for _, r := range f.mem.AuditRows() {
		out = append(out, r.Action)
	}
	return out
}

var user = Actor{Kind: ActorUser, ID: "u1"}

func TestRequestOneSelectionAndAllOutdated(t *testing.T) {
	f := newFixture(t)
	f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	f.enroll(t, "a2", "4.5.0", deb, store.CapUpgradeV1)                  // up to date
	f.enroll(t, "a3", "4.3.1", "")                                       // before 023: manual
	f.enroll(t, "a4", "4.4.0", "linux|arm64|deb", store.CapUpgradeV1)    // no arm64 artifact
	f.enroll(t, "a5", "dev", deb, store.CapUpgradeV1)                    // not comparable
	f.enroll(t, "a6", "4.4.0", rpm, store.CapUpgradeV1)                  // offline
	f.enroll(t, "a7", "4.4.0", "linux|amd64|binary", store.CapUpgradeV1) // later: unsupported
	f.enroll(t, "a8", "4.6.0", deb, store.CapUpgradeV1)                  // newer than current
	f.reg.online["a1"] = true

	res, err := f.svc.Request(context.Background(), tenant, user, []string{"a1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.TargetVersion != "4.5.0" || len(res.Created) != 1 || len(res.Skipped) != 0 {
		t.Fatalf("one = %+v", res)
	}
	u := f.upgrade(t, res.Created[0].ID)
	if u.State != store.UpgradeDelivered || u.FromVersion != "4.4.0" || u.TargetVersion != "4.5.0" || u.Origin != store.OriginUser ||
		u.RequestedBy != "u1" || u.AllowDowngrade || !u.ExpiresAt.Equal(t0.Add(7*24*time.Hour)) || u.HostID != "h-a1" || u.DeliveredAt == nil {
		t.Fatalf("request = %+v", u)
	}
	if len(f.reg.delivered) != 1 || f.reg.delivered[0].Type != registry.CommandUpgrade || f.reg.delivered[0].Upgrade == nil ||
		f.reg.delivered[0].Upgrade.RequestID != u.ID || f.reg.delivered[0].Upgrade.TargetVersion != "4.5.0" {
		t.Fatalf("delivery = %+v", f.reg.delivered)
	}
	if got := f.actions(); strings.Join(got, ",") != "agent_upgrade_requested,agent_upgrade_delivered" {
		t.Fatalf("audit = %v", got)
	}
	row := f.mem.AuditRows()[0]
	if row.ActorKind != "user" || row.ActorID != "u1" || row.SubjectKind != "agent" || row.SubjectID != "a1" ||
		row.Detail["to_version"] != "4.5.0" || row.Detail["from_version"] != "4.4.0" || row.Detail["origin"] != "user" || row.Detail["request_id"] != u.ID {
		t.Fatalf("requested audit = %+v", row)
	}

	// A second request for a1 is refused while one is active.
	res, _ = f.svc.Request(context.Background(), tenant, user, []string{"a1", "a2", "a3", "a4", "a5", "missing"}, false)
	skips := map[string]string{}
	for _, s := range res.Skipped {
		skips[s.AgentID] = s.Reason
	}
	want := map[string]string{"a1": SkipActive, "a2": SkipUpToDate, "a3": SkipManual, "a4": SkipNoRelease, "missing": SkipNotFound}
	for id, reason := range want {
		if skips[id] != reason {
			t.Errorf("skip %s = %q, want %q", id, skips[id], reason)
		}
	}
	// An explicitly selected dev build is upgraded (manual choice).
	if len(res.Created) != 1 || res.Created[0].AgentID != "a5" {
		t.Fatalf("selection created = %+v", res.Created)
	}

	// All outdated: a6 (offline: stays pending), never dev or newer agents.
	res, err = f.svc.Request(context.Background(), tenant, user, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	created := map[string]store.AgentUpgrade{}
	for _, c := range res.Created {
		created[c.AgentID] = c
	}
	if len(created) != 2 || created["a6"].State != store.UpgradePending || created["a7"].ID == "" {
		t.Fatalf("all outdated created = %+v skipped %+v", res.Created, res.Skipped)
	}
	for _, s := range res.Skipped {
		if s.AgentID == "a8" && s.Reason != SkipUpToDate {
			t.Fatalf("newer agent = %+v", s)
		}
	}
}

func TestRequestValidation(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Request(context.Background(), tenant, user, nil, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("neither ids nor all = %v", err)
	}
	if _, err := f.svc.Request(context.Background(), tenant, user, []string{"a"}, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("both = %v", err)
	}
	ids := make([]string, MaxBatch+1)
	for i := range ids {
		ids[i] = fmt.Sprint("a", i)
	}
	if _, err := f.svc.Request(context.Background(), tenant, user, ids, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("> 1000 ids = %v", err)
	}
	f.rel.current = ""
	if _, err := f.svc.Request(context.Background(), tenant, user, []string{"a"}, false); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("no current release = %v", err)
	}
	f.rel.current = "4.5.0"
	f.mem.FailNext("ListAgents")
	if _, err := f.svc.Request(context.Background(), tenant, user, []string{"a"}, false); err == nil {
		t.Fatal("store error swallowed")
	}
	f.mem.FailNext("GetUpgradePolicy")
	if _, err := f.svc.Request(context.Background(), tenant, user, []string{"a"}, false); err == nil {
		t.Fatal("policy error swallowed")
	}
	f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	f.mem.FailNext("LatestAgentUpgrades")
	if _, err := f.svc.Request(context.Background(), tenant, user, []string{"a1"}, false); err == nil {
		t.Fatal("latest error swallowed")
	}
	f.mem.FailNext("CreateAgentUpgrade")
	if _, err := f.svc.Request(context.Background(), tenant, user, []string{"a1"}, false); err == nil {
		t.Fatal("create error swallowed")
	}
}

func TestPinnedTargetAndDowngrade(t *testing.T) {
	f := newFixture(t)
	f.enroll(t, "a1", "4.5.0", deb, store.CapUpgradeV1)
	f.enroll(t, "a2", "4.3.9", deb, store.CapUpgradeV1)
	pin(t, f, "4.4.0")
	v, pinned, err := f.svc.Target(context.Background(), tenant)
	if err != nil || v != "4.4.0" || !pinned {
		t.Fatalf("target = %q %v %v", v, pinned, err)
	}
	res, err := f.svc.Request(context.Background(), tenant, user, []string{"a1", "a2"}, false)
	if err != nil {
		t.Fatal(err)
	}
	byAgent := map[string]store.AgentUpgrade{}
	for _, c := range res.Created {
		byAgent[c.AgentID] = c
	}
	if !byAgent["a1"].AllowDowngrade || byAgent["a1"].TargetVersion != "4.4.0" {
		t.Fatalf("pinned downgrade = %+v", byAgent["a1"])
	}
	if byAgent["a2"].AllowDowngrade {
		t.Fatalf("upgrade to the pin must not allow a downgrade: %+v", byAgent["a2"])
	}
	// Without a pin, an agent newer than the platform current is up to date.
	f2 := newFixture(t)
	f2.enroll(t, "a1", "4.6.0", deb, store.CapUpgradeV1)
	res, _ = f2.svc.Request(context.Background(), tenant, user, []string{"a1"}, false)
	if len(res.Created) != 0 || res.Skipped[0].Reason != SkipUpToDate {
		t.Fatalf("no downgrade without a pin: %+v", res)
	}
}

func pin(t *testing.T, f *fixture, v string) {
	t.Helper()
	if _, err := f.mem.UpdateUpgradePolicy(context.Background(), tenant, func(p *store.AgentUpgradePolicy) ([]store.AuditRow, error) {
		p.TargetVersion = v
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCancel(t *testing.T) {
	f := newFixture(t)
	f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	res, _ := f.svc.Request(context.Background(), tenant, user, []string{"a1"}, false)
	id := res.Created[0].ID
	u, err := f.svc.Cancel(context.Background(), tenant, user, id)
	if err != nil || u.State != store.UpgradeCancelled || u.FinishedAt == nil {
		t.Fatalf("cancel = %+v %v", u, err)
	}
	if _, err := f.svc.Cancel(context.Background(), tenant, user, id); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("cancel twice = %v", err)
	}
	if _, err := f.svc.Cancel(context.Background(), tenant, user, "nope"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	if _, err := f.svc.Cancel(context.Background(), "other-tenant", user, id); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("other tenant = %v", err)
	}
	if last := f.actions(); last[len(last)-1] != "agent_upgrade_cancelled" {
		t.Fatalf("audit = %v", last)
	}
	// A running upgrade cannot be cancelled.
	res, _ = f.svc.Request(context.Background(), tenant, user, []string{"a1"}, false)
	report(t, f, "a1", res.Created[0].ID, StateDownloading, "")
	if _, err := f.svc.Cancel(context.Background(), tenant, user, res.Created[0].ID); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("cancel downloading = %v", err)
	}
}

func report(t *testing.T, f *fixture, agentID, id, state, reason string) bool {
	t.Helper()
	a, _ := f.mem.GetAgent(context.Background(), tenant, agentID)
	ok, err := f.svc.Report(context.Background(), a, Report{RequestID: id, State: state, FromVersion: a.AgentVersion, ToVersion: "4.5.0", Reason: reason})
	if err != nil {
		t.Fatalf("report %s: %v", state, err)
	}
	return ok
}

func TestLifecycleSucceeded(t *testing.T) {
	f := newFixture(t)
	f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	f.reg.online["a1"] = true
	res, _ := f.svc.Request(context.Background(), tenant, user, []string{"a1"}, false)
	id := res.Created[0].ID
	f.now = t0.Add(time.Minute)
	if !report(t, f, "a1", id, StateDownloading, "") {
		t.Fatal("downloading refused")
	}
	if !report(t, f, "a1", id, StateInstalling, "") {
		t.Fatal("installing refused")
	}
	// succeeded requires the agent to run the target version.
	if report(t, f, "a1", id, StateSucceeded, "") {
		t.Fatal("succeeded accepted while the agent still reports 4.4.0")
	}
	if err := f.mem.TouchAgent(context.Background(), "a1", "4.5.0", "", t0); err != nil {
		t.Fatal(err)
	}
	f.now = t0.Add(3 * time.Minute)
	if !report(t, f, "a1", id, StateSucceeded, "") {
		t.Fatal("succeeded refused")
	}
	u := f.upgrade(t, id)
	if u.State != store.UpgradeSucceeded || u.StartedAt == nil || !u.StartedAt.Equal(t0.Add(time.Minute)) || u.FinishedAt == nil {
		t.Fatalf("request = %+v", u)
	}
	want := "agent_upgrade_requested,agent_upgrade_delivered,agent_upgrade_started,agent_upgrade_installing,agent_upgrade_succeeded"
	if got := strings.Join(f.actions(), ","); got != want {
		t.Fatalf("audit = %s", got)
	}
	last := f.mem.AuditRows()[4]
	if last.ActorKind != "agent" || last.ActorID != "a1" || last.Detail["duration_seconds"] != int64(120) {
		t.Fatalf("succeeded audit = %+v", last)
	}
	// Reports for a terminal request are ignored.
	if report(t, f, "a1", id, StateFailed, "install_failed") {
		t.Fatal("report after success accepted")
	}
	if len(f.pub.types) != 5 || f.pub.types[0] != EventUpgrade || f.pub.events[0]["state"] != store.UpgradePending || f.pub.events[4]["state"] != store.UpgradeSucceeded {
		t.Fatalf("events = %v %v", f.pub.types, f.pub.events)
	}
	if f.met.states["succeeded:"] != 1 || f.met.states["downloading:"] != 1 {
		t.Fatalf("metrics = %v", f.met.states)
	}
}

func TestLifecycleFailuresAndRollback(t *testing.T) {
	f := newFixture(t)
	f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	res, _ := f.svc.Request(context.Background(), tenant, user, []string{"a1"}, false)
	id := res.Created[0].ID
	// pending -> failed directly (e.g. busy, downgrade refused) is valid.
	if !report(t, f, "a1", id, StateFailed, "checksum_mismatch") {
		t.Fatal("failed refused")
	}
	u := f.upgrade(t, id)
	if u.State != store.UpgradeFailed || u.Reason != "checksum_mismatch" {
		t.Fatalf("failed = %+v", u)
	}
	row := f.mem.AuditRows()[len(f.mem.AuditRows())-1]
	if row.Action != "agent_upgrade_failed" || row.Outcome != "error" || row.Reason != "checksum_mismatch" {
		t.Fatalf("failed audit = %+v", row)
	}
	res, _ = f.svc.Request(context.Background(), tenant, user, []string{"a1"}, false)
	id = res.Created[0].ID
	report(t, f, "a1", id, StateDownloading, "")
	report(t, f, "a1", id, StateInstalling, "")
	if !report(t, f, "a1", id, StateRolledBack, "start_timeout") {
		t.Fatal("rollback refused")
	}
	if u := f.upgrade(t, id); u.State != store.UpgradeRolledBack || u.Reason != "start_timeout" {
		t.Fatalf("rolled back = %+v", u)
	}
}

func TestTransitionTable(t *testing.T) {
	valid := map[string][]string{
		store.UpgradePending:     {store.UpgradeDelivered, store.UpgradeDownloading, store.UpgradeSucceeded, store.UpgradeFailed, store.UpgradeCancelled, store.UpgradeExpired},
		store.UpgradeDelivered:   {store.UpgradeDownloading, store.UpgradeSucceeded, store.UpgradeFailed, store.UpgradeCancelled, store.UpgradeExpired},
		store.UpgradeDownloading: {store.UpgradeInstalling, store.UpgradeFailed},
		store.UpgradeInstalling:  {store.UpgradeSucceeded, store.UpgradeFailed, store.UpgradeRolledBack},
	}
	all := []string{store.UpgradePending, store.UpgradeDelivered, store.UpgradeDownloading, store.UpgradeInstalling, store.UpgradeSucceeded,
		store.UpgradeFailed, store.UpgradeRolledBack, store.UpgradeExpired, store.UpgradeCancelled}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, ok := range valid[from] {
				want = want || ok == to
			}
			if got := canTransition(from, to); got != want {
				t.Errorf("%s -> %s = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestReportValidationAndRefusals(t *testing.T) {
	f := newFixture(t)
	a1 := f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	a2 := f.enroll(t, "a2", "4.4.0", deb, store.CapUpgradeV1)
	res, _ := f.svc.Request(context.Background(), tenant, user, []string{"a1"}, false)
	id := res.Created[0].ID
	for name, r := range map[string]Report{
		"unknown state":  {RequestID: id, State: "done"},
		"unknown reason": {RequestID: id, State: StateFailed, Reason: "cosmic_rays"},
		"long detail":    {RequestID: id, State: StateFailed, Reason: "install_failed", Detail: strings.Repeat("x", 257)},
		"no request id":  {State: StateFailed},
		"control detail": {RequestID: id, State: StateFailed, Detail: "a\x00b"},
	} {
		if _, err := f.svc.Report(context.Background(), a1, r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// Another agent's request: not found, audited as refused.
	before := len(f.mem.AuditRows())
	if _, err := f.svc.Report(context.Background(), a2, Report{RequestID: id, State: StateDownloading}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("foreign request = %v", err)
	}
	rows := f.mem.AuditRows()
	if len(rows) != before+1 || rows[before].Action != "agent_upgrade_refused" || rows[before].Outcome != "refused" || rows[before].ActorID != "a2" {
		t.Fatalf("refusal audit = %+v", rows[before:])
	}
	if _, err := f.svc.Report(context.Background(), a1, Report{RequestID: "missing", State: StateDownloading}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("missing request = %v", err)
	}
	// Another tenant's agent cannot see the request.
	other := a1
	other.TenantID = "11111111-1111-7111-8111-111111111111"
	if _, err := f.svc.Report(context.Background(), other, Report{RequestID: id, State: StateDownloading}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("cross-tenant = %v", err)
	}
	// Out-of-order transition: ignored, not an error.
	if ok := report(t, f, "a1", id, StateRolledBack, "start_timeout"); ok {
		t.Fatal("pending -> rolled_back accepted")
	}
	// Store failure: the transition and its audit row are both absent.
	n := len(f.mem.AuditRows())
	f.mem.FailNext("UpdateAgentUpgrade.commit")
	if _, err := f.svc.Report(context.Background(), a1, Report{RequestID: id, State: StateDownloading}); err == nil {
		t.Fatal("commit failure swallowed")
	}
	if u := f.upgrade(t, id); u.State != store.UpgradePending || len(f.mem.AuditRows()) != n {
		t.Fatalf("partial write: %+v / %d audit rows", u, len(f.mem.AuditRows())-n)
	}
	f.mem.FailNext("GetAgentUpgrade")
	if _, err := f.svc.Report(context.Background(), a1, Report{RequestID: id, State: StateDownloading}); err == nil || errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("read failure = %v", err)
	}
}

func TestDeliveryOnConnect(t *testing.T) {
	f := newFixture(t)
	a1 := f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	f.enroll(t, "a2", "4.4.0", deb, store.CapUpgradeV1)
	res, _ := f.svc.Request(context.Background(), tenant, user, []string{"a1", "a2"}, false)
	if len(f.reg.delivered) != 0 || res.Created[0].State != store.UpgradePending {
		t.Fatal("offline agents must not be delivered")
	}
	cmds, err := f.svc.OnConnect(context.Background(), a1)
	if err != nil || len(cmds) != 1 || cmds[0].Upgrade.TargetVersion != "4.5.0" || cmds[0].Type != registry.CommandUpgrade {
		t.Fatalf("on connect = %+v %v", cmds, err)
	}
	var id string
	for _, c := range res.Created {
		if c.AgentID == "a1" {
			id = c.ID
		}
	}
	if u := f.upgrade(t, id); u.State != store.UpgradeDelivered {
		t.Fatalf("state after connect = %s", u.State)
	}
	// A reconnect re-delivers a delivered request (the agent deduplicates).
	cmds, _ = f.svc.OnConnect(context.Background(), a1)
	if len(cmds) != 1 || cmds[0].Upgrade.RequestID != id {
		t.Fatalf("redelivery = %+v", cmds)
	}
	// Nothing for agents without an active request.
	report(t, f, "a1", id, StateFailed, "busy")
	if cmds, _ := f.svc.OnConnect(context.Background(), a1); len(cmds) != 0 {
		t.Fatalf("terminal request redelivered: %+v", cmds)
	}
	f.mem.FailNext("ListAgentUpgrades")
	if _, err := f.svc.OnConnect(context.Background(), a1); err == nil {
		t.Fatal("store error swallowed")
	}
	// Delivery errors leave the request pending.
	f.reg.online["a3"] = true
	f.enroll(t, "a3", "4.4.0", deb, store.CapUpgradeV1)
	f.reg.fail = errors.New("valkey down")
	res, _ = f.svc.Request(context.Background(), tenant, user, []string{"a3"}, false)
	if res.Created[0].State != store.UpgradePending {
		t.Fatalf("delivery error = %+v", res.Created[0])
	}
	f.reg.fail = nil
	// The delivered transition failing is not fatal either.
	f.enroll(t, "a4", "4.4.0", deb, store.CapUpgradeV1)
	f.reg.online["a4"] = true
	f.mem.FailNext("UpdateAgentUpgrade")
	res, err = f.svc.Request(context.Background(), tenant, user, []string{"a4"}, false)
	if err != nil || len(res.Created) != 1 {
		t.Fatalf("mark delivered failure = %+v %v", res, err)
	}
}

func TestSweeper(t *testing.T) {
	f := newFixture(t)
	f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	f.enroll(t, "a2", "4.4.0", deb, store.CapUpgradeV1)
	f.enroll(t, "a3", "4.4.0", deb, store.CapUpgradeV1)
	res, _ := f.svc.Request(context.Background(), tenant, user, []string{"a1", "a2", "a3"}, false)
	ids := map[string]string{}
	for _, c := range res.Created {
		ids[c.AgentID] = c.ID
	}
	f.now = t0.Add(time.Minute)
	report(t, f, "a2", ids["a2"], StateDownloading, "")
	f.now = t0.Add(10 * time.Minute)
	report(t, f, "a3", ids["a3"], StateDownloading, "")

	f.now = t0.Add(20 * time.Minute) // a2 silent for 19 min (> 15), a3 for 10
	expired, failed, err := f.svc.Sweep(context.Background())
	if err != nil || expired != 0 || failed != 1 {
		t.Fatalf("sweep = %d %d %v", expired, failed, err)
	}
	if u := f.upgrade(t, ids["a2"]); u.State != store.UpgradeFailed || u.Reason != ReasonStartTimeout {
		t.Fatalf("stale = %+v", u)
	}
	row := f.mem.AuditRows()[len(f.mem.AuditRows())-1]
	if row.Action != "agent_upgrade_failed" || row.ActorKind != "system" || row.ActorID != "inventory" {
		t.Fatalf("stale audit = %+v", row)
	}
	f.now = t0.Add(8 * 24 * time.Hour)
	expired, failed, err = f.svc.Sweep(context.Background())
	if err != nil || expired != 1 || failed != 1 {
		t.Fatalf("second sweep = %d %d %v", expired, failed, err)
	}
	if u := f.upgrade(t, ids["a1"]); u.State != store.UpgradeExpired || u.Reason != "expired" {
		t.Fatalf("expired = %+v", u)
	}
	f.mem.FailNext("ListStaleUpgrades")
	if _, _, err := f.svc.Sweep(context.Background()); err == nil {
		t.Fatal("store error swallowed")
	}
	// A request that changed between listing and locking is left alone.
	f.enroll(t, "a4", "4.4.0", deb, store.CapUpgradeV1)
	res, _ = f.svc.Request(context.Background(), tenant, user, []string{"a4"}, false)
	u := f.upgrade(t, res.Created[0].ID)
	if sweepOne(&u, f.now.Add(-time.Hour), f.now.Add(-time.Hour)) {
		t.Fatal("a fresh request must not be swept")
	}
	raced := New(staleRepo{Mem: f.mem, extra: []store.AgentUpgrade{u}}, f.rel, f.reg, f.pub, f.svc.cfg)
	raced.now = func() time.Time { return f.now }
	if e, fl, err := raced.Sweep(context.Background()); err != nil || e != 0 || fl != 0 {
		t.Fatalf("raced sweep = %d %d %v", e, fl, err)
	}
	// A swept failure runs the finished hook.
	var finished []string
	f.svc.OnFinished(func(_ context.Context, u store.AgentUpgrade) { finished = append(finished, u.ID) })
	report(t, f, "a4", u.ID, StateDownloading, "")
	f.now = f.now.Add(time.Hour)
	if _, fl, err := f.svc.Sweep(context.Background()); err != nil || fl != 1 || len(finished) != 1 || finished[0] != u.ID {
		t.Fatalf("finished hook on sweep = %d %v %v", fl, finished, err)
	}
	f.enroll(t, "a5", "4.4.0", deb, store.CapUpgradeV1)
	if _, err := f.svc.Request(context.Background(), tenant, user, []string{"a5"}, false); err != nil {
		t.Fatal(err)
	}
	f.mem.FailNext("UpdateAgentUpgrade")
	f.now = f.now.Add(30 * 24 * time.Hour)
	if _, _, err := f.svc.Sweep(context.Background()); err == nil {
		t.Fatal("update error swallowed")
	}
}

func TestCheckAndDownloadAuthorization(t *testing.T) {
	f := newFixture(t)
	a1 := f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	a2 := f.enroll(t, "a2", "4.4.0", deb, store.CapUpgradeV1)
	p := agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "deb"}

	c, err := f.svc.Check(context.Background(), a1, "4.4.0", p, false)
	if err != nil || !c.Available || c.TargetVersion != "4.5.0" || c.RequestID != "" {
		t.Fatalf("check = %+v %v", c, err)
	}
	c, err = f.svc.Check(context.Background(), a1, "4.4.0", p, true)
	if err != nil || !c.Available || c.RequestID == "" {
		t.Fatalf("apply = %+v %v", c, err)
	}
	u := f.upgrade(t, c.RequestID)
	if u.Origin != store.OriginAgent || u.RequestedBy != "a1" || f.mem.AuditRows()[0].ActorKind != "agent" {
		t.Fatalf("agent request = %+v", u)
	}
	// While active the same request is returned.
	c2, _ := f.svc.Check(context.Background(), a1, "4.4.0", p, true)
	if c2.RequestID != c.RequestID || c2.Reason != ReasonActive {
		t.Fatalf("active = %+v", c2)
	}
	if c, _ := f.svc.Check(context.Background(), a2, "4.5.0", p, true); c.Available || c.Reason != ReasonUpToDate {
		t.Fatalf("up to date = %+v", c)
	}
	if c, _ := f.svc.Check(context.Background(), a2, "4.4.0", agentrelease.Platform{OS: "linux", Arch: "arm64", InstallType: "rpm"}, false); c.Available || c.Reason != ReasonNoRelease {
		t.Fatalf("no release = %+v", c)
	}
	if c, _ := f.svc.Check(context.Background(), a2, "dev", p, false); !c.Available || c.Reason != ReasonNotComparable {
		t.Fatalf("dev build = %+v", c)
	}
	if _, err := f.svc.Check(context.Background(), a2, "4.4.0", agentrelease.Platform{OS: "plan9"}, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad platform = %v", err)
	}
	pin(t, f, "4.4.0")
	if c, _ := f.svc.Check(context.Background(), a2, "4.5.0", p, false); !c.Available || !c.AllowDowngrade || c.TargetVersion != "4.4.0" {
		t.Fatalf("pinned downgrade = %+v", c)
	}
	pin(t, f, "")
	f.rel.current = ""
	if c, _ := f.svc.Check(context.Background(), a2, "4.4.0", p, false); c.Available || c.Reason != ReasonNoRelease {
		t.Fatalf("no current = %+v", c)
	}
	f.rel.current = "4.5.0"
	f.mem.FailNext("GetUpgradePolicy")
	if _, err := f.svc.Check(context.Background(), a2, "4.4.0", p, false); err == nil {
		t.Fatal("policy error swallowed")
	}
	f.mem.FailNext("ListAgentUpgrades")
	if _, err := f.svc.Check(context.Background(), a2, "4.4.0", p, false); err == nil {
		t.Fatal("list error swallowed")
	}
	f.mem.FailNext("CreateAgentUpgrade")
	if _, err := f.svc.Check(context.Background(), a2, "4.4.0", p, true); err == nil {
		t.Fatal("create error swallowed")
	}

	// Download: target of the own active request, or the own current version.
	v, err := f.svc.AuthorizeDownload(context.Background(), a1, c.RequestID, "4.5.0")
	if err != nil || v != "4.5.0" {
		t.Fatalf("authorized = %q %v", v, err)
	}
	if v, err := f.svc.AuthorizeDownload(context.Background(), a1, "", "4.4.0"); err != nil || v != "4.4.0" {
		t.Fatalf("rollback package = %q %v", v, err)
	}
	if _, err := f.svc.AuthorizeDownload(context.Background(), a1, "", "4.5.0"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("no request, other version = %v", err)
	}
	if _, err := f.svc.AuthorizeDownload(context.Background(), a1, c.RequestID, "4.6.0"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("version != target = %v", err)
	}
	n := len(f.mem.AuditRows())
	if _, err := f.svc.AuthorizeDownload(context.Background(), a2, c.RequestID, "4.5.0"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("foreign request = %v", err)
	}
	if rows := f.mem.AuditRows(); len(rows) != n+1 || rows[n].Action != "agent_upgrade_refused" {
		t.Fatal("foreign download not audited")
	}
	report(t, f, "a1", c.RequestID, StateFailed, "disk_full")
	if _, err := f.svc.AuthorizeDownload(context.Background(), a1, c.RequestID, "4.5.0"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("inactive request = %v", err)
	}
	f.mem.FailNext("GetAgentUpgrade")
	if _, err := f.svc.AuthorizeDownload(context.Background(), a1, c.RequestID, "4.5.0"); err == nil || errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("read error = %v", err)
	}
}

func TestFleet(t *testing.T) {
	f := newFixture(t)
	f.enroll(t, "a1", "4.5.0", deb, store.CapUpgradeV1)                  // up to date
	f.enroll(t, "a2", "4.4.0", deb, store.CapUpgradeV1)                  // available (online)
	f.enroll(t, "a3", "4.3.1", "")                                       // manual
	f.enroll(t, "a4", "4.4.0", deb, store.CapUpgradeV1)                  // pending
	f.enroll(t, "a5", "4.4.0", deb, store.CapUpgradeV1)                  // in progress
	f.enroll(t, "a6", "4.4.0", deb, store.CapUpgradeV1)                  // failed
	f.enroll(t, "a7", "4.4.0", deb, store.CapUpgradeV1)                  // rolled back
	f.enroll(t, "a8", "4.4.0", "linux|amd64|binary", store.CapUpgradeV1) // unsupported
	f.enroll(t, "a9", "4.5.0", "")                                       // old-agent style but current: up to date
	f.enroll(t, "b1", "4.4.0", deb, store.CapUpgradeV1)                  // old failure (> 7 days): available
	f.reg.online["a2"] = true
	if _, err := f.mem.ResolveHost(context.Background(), tenant, store.Host{ID: "h-a2", Hostname: "node-2"}); err != nil {
		t.Fatal(err)
	}
	res, _ := f.svc.Request(context.Background(), tenant, user, []string{"a4", "a5", "a6", "a7", "a8", "b1"}, false)
	ids := map[string]string{}
	for _, c := range res.Created {
		ids[c.AgentID] = c.ID
	}
	report(t, f, "a5", ids["a5"], StateDownloading, "")
	report(t, f, "a6", ids["a6"], StateFailed, "checksum_mismatch")
	report(t, f, "a7", ids["a7"], StateDownloading, "")
	report(t, f, "a7", ids["a7"], StateInstalling, "")
	report(t, f, "a7", ids["a7"], StateRolledBack, "start_timeout")
	report(t, f, "a8", ids["a8"], StateFailed, ReasonUnsupportedInstall)
	f.now = t0.Add(-8 * 24 * time.Hour)
	report(t, f, "b1", ids["b1"], StateFailed, "busy")
	f.now = t0.Add(time.Hour)

	entries, current, err := f.svc.Fleet(context.Background(), tenant, FleetFilter{})
	if err != nil || current != "4.5.0" || len(entries) != 10 {
		t.Fatalf("fleet = %d %q %v", len(entries), current, err)
	}
	got := map[string]FleetEntry{}
	for _, e := range entries {
		got[e.AgentID] = e
	}
	want := map[string]string{"a1": FleetUpToDate, "a2": FleetAvailable, "a3": FleetManual, "a4": FleetPending, "a5": FleetInProgress,
		"a6": FleetFailed, "a7": FleetRolledBack, "a8": FleetUnsupported, "a9": FleetUpToDate, "b1": FleetAvailable}
	for id, state := range want {
		if got[id].UpgradeState != state {
			t.Errorf("%s = %q, want %q", id, got[id].UpgradeState, state)
		}
	}
	if !got["a2"].Online || got["a1"].Online || got["a2"].Hostname != "node-2" || got["a6"].UpgradeReason != "checksum_mismatch" ||
		got["a7"].UpgradeReason != "start_timeout" || got["a4"].UpgradeID != ids["a4"] || got["a2"].TargetVersion != "4.5.0" ||
		got["a2"].OS != "linux" || got["a2"].InstallType != "deb" {
		t.Fatalf("entries = %+v", got)
	}
	// Filters and paging.
	out, _, _ := f.svc.Fleet(context.Background(), tenant, FleetFilter{State: FleetAvailable})
	if len(out) != 2 {
		t.Fatalf("state filter = %+v", out)
	}
	out, _, _ = f.svc.Fleet(context.Background(), tenant, FleetFilter{Outdated: true})
	if len(out) != 8 { // everyone below 4.5.0
		t.Fatalf("outdated filter = %d", len(out))
	}
	page, _, _ := f.svc.Fleet(context.Background(), tenant, FleetFilter{Limit: 3})
	next, _, _ := f.svc.Fleet(context.Background(), tenant, FleetFilter{Limit: 3, Cursor: page[2].AgentID})
	if len(page) != 3 || len(next) != 3 || next[0].AgentID <= page[2].AgentID {
		t.Fatalf("paging = %v / %v", page, next)
	}
	e, recent, err := f.svc.Agent(context.Background(), tenant, "a7")
	if err != nil || e.UpgradeState != FleetRolledBack || len(recent) != 1 {
		t.Fatalf("agent = %+v %d %v", e, len(recent), err)
	}
	if _, _, err := f.svc.Agent(context.Background(), tenant, "nope"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("unknown agent = %v", err)
	}
	for _, m := range []string{"ListAgents", "LatestAgentUpgrades", "GetUpgradePolicy", "ListHosts"} {
		f.mem.FailNext(m)
		if _, _, err := f.svc.Fleet(context.Background(), tenant, FleetFilter{}); err == nil {
			t.Errorf("%s error swallowed", m)
		}
	}
	f.reg.listFail = errors.New("valkey down")
	if out, _, err := f.svc.Fleet(context.Background(), tenant, FleetFilter{}); err != nil || len(out) != 10 {
		t.Fatalf("registry failure must degrade to offline: %d %v", len(out), err)
	}
	f.reg.listFail = nil
	f.mem.FailNext("ListAgentUpgrades")
	if _, _, err := f.svc.Agent(context.Background(), tenant, "a7"); err == nil {
		t.Fatal("recent error swallowed")
	}
	f.mem.FailNext("ListAgents")
	if _, _, err := f.svc.Agent(context.Background(), tenant, "a7"); err == nil {
		t.Fatal("fleet error swallowed")
	}
	list, err := f.svc.List(context.Background(), tenant, repo.UpgradeFilter{AgentID: "a7"})
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v %v", list, err)
	}
}

func TestMetricsDefaultAndDeliveryHook(t *testing.T) {
	f := newFixture(t)
	f.svc.SetMetrics(nil) // nil falls back to no-op
	f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	var finished []store.AgentUpgrade
	f.svc.OnFinished(func(_ context.Context, u store.AgentUpgrade) { finished = append(finished, u) })
	res, _ := f.svc.Request(context.Background(), tenant, user, []string{"a1"}, false)
	report(t, f, "a1", res.Created[0].ID, StateFailed, "install_failed")
	if len(finished) != 1 || finished[0].State != store.UpgradeFailed {
		t.Fatalf("finished hook = %+v", finished)
	}
}

func TestVocabulary(t *testing.T) {
	for _, r := range []string{"signature_invalid", "unknown_key", "checksum_mismatch", "size_mismatch", "platform_mismatch", "version_mismatch",
		"downgrade_refused", "disk_full", "download_failed", "install_failed", "start_timeout", "unsupported_install", "busy",
		"package_db_mismatch", "cancelled_locally"} {
		if !ValidReason(r) {
			t.Errorf("reason %s", r)
		}
	}
	if ValidReason("expired") || ValidReason("x") {
		t.Error("server-only or unknown reasons are not agent reasons")
	}
	if !ValidCapability("upgrade.v1") || ValidCapability("Upgrade") || ValidCapability(strings.Repeat("a", 33)) {
		t.Error("capability pattern")
	}
}

// staleRepo lists a request as stale although it is not (it changed between
// listing and locking).
type staleRepo struct {
	*memstore.Mem
	extra []store.AgentUpgrade
}

func (r staleRepo) ListStaleUpgrades(context.Context, time.Time, time.Time, int) ([]store.AgentUpgrade, error) {
	return r.extra, nil
}

func TestEdges(t *testing.T) {
	// Default clock and ids.
	if New(memstore.New(), &fakeReleases{}, &fakeRegistry{}, &fakePub{}, Config{}).now().IsZero() {
		t.Fatal("default clock")
	}
	f := newFixture(t)
	f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	f.enroll(t, "a2", "4.4.0", "darwin|amd64|binary", store.CapUpgradeV1)
	f.enroll(t, "a3", "dev", deb, store.CapUpgradeV1)
	// The same agent twice in one batch: the second hits the active index.
	res, err := f.svc.Request(context.Background(), tenant, user, []string{"a1", "a1", "a2"}, false)
	if err != nil || len(res.Created) != 1 || res.Skipped[0].Reason != SkipActive || res.Skipped[1].Reason != SkipUnsup {
		t.Fatalf("duplicate / bad platform = %+v %v", res, err)
	}
	// Dev builds are never upgraded automatically.
	res, _ = f.svc.Request(context.Background(), tenant, user, nil, true)
	for _, c := range res.Created {
		if c.AgentID == "a3" {
			t.Fatal("dev build upgraded by all_outdated")
		}
	}
	// A failed unsupported install is not retried.
	f.enroll(t, "a4", "4.4.0", "linux|amd64|binary", store.CapUpgradeV1)
	res, _ = f.svc.Request(context.Background(), tenant, user, []string{"a4"}, false)
	report(t, f, "a4", res.Created[0].ID, StateFailed, ReasonUnsupportedInstall)
	res, _ = f.svc.Request(context.Background(), tenant, user, []string{"a4"}, false)
	if len(res.Created) != 0 || res.Skipped[0].Reason != SkipUnsup {
		t.Fatalf("unsupported retry = %+v", res)
	}
	// An invalid actor fails the audit row (and the request).
	f.enroll(t, "a5", "4.4.0", deb, store.CapUpgradeV1)
	if _, err := f.svc.Request(context.Background(), tenant, Actor{Kind: "robot", ID: "r"}, []string{"a5"}, false); err == nil {
		t.Fatal("invalid actor accepted")
	}
}
