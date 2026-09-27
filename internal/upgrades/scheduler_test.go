package upgrades

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func repoFilter() repo.UpgradeFilter { return repo.UpgradeFilter{} }

var admin = Actor{Kind: ActorUser, ID: "admin-1"}

func policyInput() PolicyInput {
	return PolicyInput{Enabled: true, WindowStart: "09:00", WindowEnd: "11:00", Timezone: "UTC", MaxConcurrent: 2}
}

func TestPolicyGetUpdateResume(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p, err := f.svc.GetPolicy(ctx, tenant)
	if err != nil || p.Enabled || p.MaxConcurrent != 5 || p.Timezone != "UTC" {
		t.Fatalf("defaults = %+v %v", p, err)
	}

	in := policyInput()
	in.TargetVersion = "4.4.0"
	p, err = f.svc.UpdatePolicy(ctx, tenant, admin, in)
	if err != nil || !p.Enabled || p.TargetVersion != "4.4.0" || p.UpdatedBy != "admin-1" || !p.UpdatedAt.Equal(t0) || p.MaxConcurrent != 2 {
		t.Fatalf("update = %+v %v", p, err)
	}
	rows := f.mem.AuditRows()
	if len(rows) != 1 || rows[0].Action != "upgrade_policy_updated" || rows[0].SubjectKind != "upgrade_policy" || rows[0].SubjectID != tenant ||
		rows[0].ActorID != "admin-1" {
		t.Fatalf("audit = %+v", rows)
	}
	changes := rows[0].Detail["changes"].(map[string]any)
	if len(changes) != 5 || changes["enabled"].(map[string]any)["before"] != false || changes["enabled"].(map[string]any)["after"] != true ||
		changes["target_version"].(map[string]any)["after"] != "4.4.0" {
		t.Fatalf("changes = %+v", changes)
	}
	if f.pub.types[len(f.pub.types)-1] != EventPolicy || f.pub.events[len(f.pub.events)-1]["paused"] != false {
		t.Fatalf("event = %v %v", f.pub.types, f.pub.events)
	}
	// Target follows the pin.
	if v, pinned, _ := f.svc.Target(ctx, tenant); v != "4.4.0" || !pinned {
		t.Fatalf("target = %s %v", v, pinned)
	}

	// No change: no audit row.
	if _, err := f.svc.UpdatePolicy(ctx, tenant, admin, in); err != nil || len(f.mem.AuditRows()) != 1 {
		t.Fatalf("no-op update: %v, %d rows", err, len(f.mem.AuditRows()))
	}

	// Validation and unknown pinned version.
	bad := in
	bad.Timezone = "Mars/Olympus"
	if _, err := f.svc.UpdatePolicy(ctx, tenant, admin, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad tz = %v", err)
	}
	bad = in
	bad.TargetVersion = "4.9.9"
	if _, err := f.svc.UpdatePolicy(ctx, tenant, admin, bad); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("unknown version = %v", err)
	}
	f.mem.FailNext("UpdateUpgradePolicy")
	if _, err := f.svc.UpdatePolicy(ctx, tenant, admin, policyInput()); err == nil {
		t.Fatal("store failure hidden")
	}
	f.mem.FailNext("GetUpgradePolicy")
	if _, err := f.svc.GetPolicy(ctx, tenant); err == nil {
		t.Fatal("get failure hidden")
	}

	// Resume of a policy that is not paused is a no-op without an audit row.
	n := len(f.mem.AuditRows())
	if p, err := f.svc.ResumePolicy(ctx, tenant, admin); err != nil || p.Paused || len(f.mem.AuditRows()) != n {
		t.Fatalf("resume unpaused = %+v %v", p, err)
	}
	f.mem.FailNext("UpdateUpgradePolicy")
	if _, err := f.svc.ResumePolicy(ctx, tenant, admin); err == nil {
		t.Fatal("resume failure hidden")
	}
}

func TestPolicyUnknownVersionWhenReleasesUnreadable(t *testing.T) {
	f := newFixture(t)
	f.rel.listFail = errors.New("db down")
	in := policyInput()
	in.TargetVersion = "4.5.0"
	if _, err := f.svc.UpdatePolicy(context.Background(), tenant, admin, in); err == nil || errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("releases failure = %v", err)
	}
}

// schedFixture: tenant policy enabled in a window containing t0 (10:00 UTC).
func schedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	if _, err := f.svc.UpdatePolicy(context.Background(), tenant, admin, policyInput()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		f.enroll(t, id, "4.4.0", deb, store.CapUpgradeV1)
		f.reg.online[id] = true
	}
	f.enroll(t, "a4", "4.4.0", deb, store.CapUpgradeV1) // offline
	return f
}

func TestSchedulerCreatesPolicyRequestsUpToCapacity(t *testing.T) {
	f := schedFixture(t)
	ctx := context.Background()
	n, err := f.svc.RunPolicies(ctx)
	if err != nil || n != 2 {
		t.Fatalf("run = %d %v", n, err)
	}
	list, _ := f.svc.List(ctx, tenant, repoFilter())
	if len(list) != 2 {
		t.Fatalf("requests = %+v", list)
	}
	for _, u := range list {
		if u.Origin != store.OriginPolicy || u.RequestedBy != PolicyActorID || u.AgentID == "a4" || u.TargetVersion != "4.5.0" {
			t.Fatalf("policy request = %+v", u)
		}
	}
	var sys bool
	for _, r := range f.mem.AuditRows() {
		if r.Action == "agent_upgrade_requested" {
			sys = r.ActorKind == "system" && r.ActorID == PolicyActorID
		}
	}
	if !sys {
		t.Fatal("policy requests not audited as the system actor")
	}
	// At capacity: nothing more until one finishes.
	if n, _ := f.svc.RunPolicies(ctx); n != 0 {
		t.Fatalf("second run = %d", n)
	}
	// Outside the window: nothing.
	report(t, f, list[0].AgentID, list[0].ID, StateDownloading, "")
	report(t, f, list[0].AgentID, list[0].ID, StateInstalling, "")
	if err := f.mem.TouchAgent(ctx, list[0].AgentID, "4.5.0", "h-"+list[0].AgentID, t0); err != nil {
		t.Fatal(err)
	}
	report(t, f, list[0].AgentID, list[0].ID, StateSucceeded, "")
	f.now = t0.Add(3 * time.Hour)
	if n, _ := f.svc.RunPolicies(ctx); n != 0 {
		t.Fatalf("outside window = %d", n)
	}
	f.now = t0
	if n, _ := f.svc.RunPolicies(ctx); n != 1 {
		t.Fatalf("after success = %d", n)
	}
}

func TestSchedulerPerTenantLock(t *testing.T) {
	f := schedFixture(t)
	ctx := context.Background()
	var inner int
	acquired, err := f.mem.TryTenantLock(ctx, PolicyLockKey(tenant), func(ctx context.Context) error {
		var err error
		inner, err = f.svc.RunPolicies(ctx) // another replica holds the tenant lock
		return err
	})
	if !acquired || err != nil || inner != 0 {
		t.Fatalf("locked run = %v %v %d", acquired, err, inner)
	}
	if n, _ := f.svc.RunPolicies(ctx); n != 2 {
		t.Fatalf("unlocked run = %d", n)
	}
}

func TestSchedulerFailurePausesPolicyAndResume(t *testing.T) {
	f := schedFixture(t)
	ctx := context.Background()
	f.svc.OnFinished(f.svc.PauseOnFailure)
	if n, _ := f.svc.RunPolicies(ctx); n != 2 {
		t.Fatal("no policy requests")
	}
	list, _ := f.svc.List(ctx, tenant, repoFilter())
	report(t, f, list[0].AgentID, list[0].ID, StateFailed, "install_failed")
	p, _ := f.svc.GetPolicy(ctx, tenant)
	if !p.Paused || p.PausedReason != "failed:"+list[0].ID {
		t.Fatalf("policy after failure = %+v", p)
	}
	var paused []store.AuditRow
	for _, r := range f.mem.AuditRows() {
		if r.Action == "upgrade_policy_paused" {
			paused = append(paused, r)
		}
	}
	if len(paused) != 1 || paused[0].Outcome != "error" || paused[0].ActorKind != "system" || paused[0].Detail["request_id"] != list[0].ID ||
		paused[0].Reason != "install_failed" {
		t.Fatalf("paused audit = %+v", paused)
	}
	if f.pub.types[len(f.pub.types)-1] != EventPolicy || f.pub.events[len(f.pub.events)-1]["paused"] != true {
		t.Fatalf("pause event = %v", f.pub.types)
	}
	// A second failure while paused adds no second pause row.
	report(t, f, list[1].AgentID, list[1].ID, StateDownloading, "")
	report(t, f, list[1].AgentID, list[1].ID, StateInstalling, "")
	report(t, f, list[1].AgentID, list[1].ID, StateRolledBack, "start_timeout")
	if u := f.upgrade(t, list[1].ID); u.State != store.UpgradeRolledBack {
		t.Fatalf("second request = %+v", u)
	}
	count := 0
	for _, r := range f.mem.AuditRows() {
		if r.Action == "upgrade_policy_paused" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("pause rows = %d", count)
	}
	// Paused: the scheduler creates nothing.
	if n, _ := f.svc.RunPolicies(ctx); n != 0 {
		t.Fatalf("paused run = %d", n)
	}
	// Resume clears the pause (audited); the scheduler works again.
	p, err := f.svc.ResumePolicy(ctx, tenant, admin)
	if err != nil || p.Paused || p.PausedReason != "" {
		t.Fatalf("resume = %+v %v", p, err)
	}
	if acts := f.actions(); acts[len(acts)-1] != "upgrade_policy_resumed" {
		t.Fatalf("audit = %v", acts)
	}
	if n, _ := f.svc.RunPolicies(ctx); n != 1 { // a3; a1/a2 failed for this target
		t.Fatalf("after resume = %d", n)
	}
}

func TestUserFailureDoesNotPause(t *testing.T) {
	f := schedFixture(t)
	ctx := context.Background()
	f.svc.OnFinished(f.svc.PauseOnFailure)
	res, _ := f.svc.Request(ctx, tenant, user, []string{"a1"}, false)
	report(t, f, "a1", res.Created[0].ID, StateFailed, "install_failed")
	if p, _ := f.svc.GetPolicy(ctx, tenant); p.Paused {
		t.Fatal("user-origin failure paused the policy")
	}
	// A succeeded policy request never pauses either.
	f.svc.PauseOnFailure(ctx, store.AgentUpgrade{TenantID: tenant, Origin: store.OriginPolicy, State: store.UpgradeSucceeded})
	if p, _ := f.svc.GetPolicy(ctx, tenant); p.Paused {
		t.Fatal("success paused the policy")
	}
	// Store failure while pausing is swallowed (the scheduler retries nothing; it is logged by the caller).
	f.mem.FailNext("UpdateUpgradePolicy")
	f.svc.PauseOnFailure(ctx, store.AgentUpgrade{ID: "x", TenantID: tenant, Origin: store.OriginPolicy, State: store.UpgradeFailed})
	if p, _ := f.svc.GetPolicy(ctx, tenant); p.Paused {
		t.Fatal("failed pause applied")
	}
}

func TestSchedulerErrors(t *testing.T) {
	ctx := context.Background()
	for _, method := range []string{"ListEnabledUpgradePolicies", "TryTenantLock", "GetUpgradePolicy", "ListAgents", "LatestAgentUpgrades"} {
		f := schedFixture(t)
		f.mem.FailNext(method)
		if _, err := f.svc.RunPolicies(ctx); err == nil || !strings.Contains(err.Error(), method) {
			t.Errorf("%s: err = %v", method, err)
		}
	}
	// Registry failure: nobody is online, nothing planned.
	f := schedFixture(t)
	f.reg.listFail = errors.New("valkey down")
	if n, err := f.svc.RunPolicies(ctx); err == nil || n != 0 {
		t.Fatalf("registry failure = %d %v", n, err)
	}
	// No release: the tenant is skipped without error.
	f = schedFixture(t)
	f.rel.current = ""
	if n, err := f.svc.RunPolicies(ctx); err != nil || n != 0 {
		t.Fatalf("no release = %d %v", n, err)
	}
	// The policy was disabled between listing and locking.
	f = schedFixture(t)
	in := policyInput()
	in.Enabled = false
	pol, _ := f.svc.GetPolicy(ctx, tenant)
	_, _ = f.svc.UpdatePolicy(ctx, tenant, admin, in)
	if n, err := f.svc.runPolicy(ctx, pol.TenantID); err != nil || n != 0 {
		t.Fatalf("disabled in between = %d %v", n, err)
	}
	// Creating the requests fails.
	f = schedFixture(t)
	f.mem.FailNext("CreateAgentUpgrade")
	if _, err := f.svc.RunPolicies(ctx); err == nil {
		t.Fatal("create failure hidden")
	}
}
