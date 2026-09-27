package upgrades

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func pol(start, end, tz string) store.AgentUpgradePolicy {
	return store.AgentUpgradePolicy{Enabled: true, WindowStart: start, WindowEnd: end, Timezone: tz, MaxConcurrent: 5}
}

func TestInWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 2, h, m, 0, 0, time.UTC) }
	cases := []struct {
		name string
		p    store.AgentUpgradePolicy
		now  time.Time
		want bool
	}{
		{"inside", pol("02:00", "04:00", "UTC"), at(3, 0), true},
		{"at start", pol("02:00", "04:00", "UTC"), at(2, 0), true},
		{"at end is outside", pol("02:00", "04:00", "UTC"), at(4, 0), false},
		{"before", pol("02:00", "04:00", "UTC"), at(1, 59), false},
		{"wrap: late evening", pol("22:00", "02:00", "UTC"), at(23, 30), true},
		{"wrap: after midnight", pol("22:00", "02:00", "UTC"), at(1, 0), true},
		{"wrap: midday outside", pol("22:00", "02:00", "UTC"), at(12, 0), false},
		{"start = end: whole day", pol("03:00", "03:00", "UTC"), at(17, 45), true},
		{"timezone shifts the window", pol("02:00", "04:00", "Europe/Sofia"), at(0, 30), true}, // 03:30 EEST
		{"timezone outside", pol("02:00", "04:00", "Europe/Sofia"), at(3, 0), false},           // 06:00 EEST
		{"bad timezone", pol("02:00", "04:00", "Mars/Olympus"), at(3, 0), false},
		{"bad window", pol("2:00", "04:00", "UTC"), at(3, 0), false},
	}
	for _, c := range cases {
		if got := InWindow(c.now, c.p); got != c.want {
			t.Errorf("%s: InWindow = %v, want %v", c.name, got, c.want)
		}
	}
	// DST: 02:00-04:00 Europe/Sofia is 23:00(-1)-01:00 UTC in summer (UTC+3), 00:00-02:00 UTC in winter (UTC+2).
	summer := time.Date(2026, 6, 30, 23, 30, 0, 0, time.UTC)
	winter := time.Date(2026, 11, 30, 23, 30, 0, 0, time.UTC)
	p := pol("02:00", "04:00", "Europe/Sofia")
	if !InWindow(summer, p) || InWindow(winter, p) {
		t.Fatalf("DST: summer %v winter %v", InWindow(summer, p), InWindow(winter, p))
	}
	if !InWindow(time.Date(2026, 12, 1, 0, 30, 0, 0, time.UTC), p) {
		t.Fatal("winter window at 02:30 local")
	}
}

func TestValidatePolicy(t *testing.T) {
	ok := store.AgentUpgradePolicy{WindowStart: "02:00", WindowEnd: "04:00", Timezone: "Europe/Sofia", MaxConcurrent: 1}
	if err := ValidatePolicy(ok); err != nil {
		t.Fatal(err)
	}
	ok.TargetVersion = "4.5.0"
	ok.MaxConcurrent = 100
	if err := ValidatePolicy(ok); err != nil {
		t.Fatal(err)
	}
	bad := []func(p *store.AgentUpgradePolicy){
		func(p *store.AgentUpgradePolicy) { p.WindowStart = "24:00" },
		func(p *store.AgentUpgradePolicy) { p.WindowEnd = "4:00" },
		func(p *store.AgentUpgradePolicy) { p.WindowEnd = "04:60" },
		func(p *store.AgentUpgradePolicy) { p.Timezone = "" },
		func(p *store.AgentUpgradePolicy) { p.Timezone = "Mars/Olympus" },
		func(p *store.AgentUpgradePolicy) { p.Timezone = "Local" },
		func(p *store.AgentUpgradePolicy) { p.Timezone = string(make([]byte, 65)) },
		func(p *store.AgentUpgradePolicy) { p.MaxConcurrent = 0 },
		func(p *store.AgentUpgradePolicy) { p.MaxConcurrent = 101 },
		func(p *store.AgentUpgradePolicy) { p.TargetVersion = "dev" },
		func(p *store.AgentUpgradePolicy) { p.TargetVersion = "4.3.0" }, // below the self-upgrade floor
	}
	for i, mut := range bad {
		p := ok
		mut(&p)
		if err := ValidatePolicy(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: %v, want ErrInvalid", i, err)
		}
	}
}

func cand(id, version string, online bool, mods ...func(*Candidate)) Candidate {
	c := Candidate{Agent: store.Agent{ID: id, TenantID: tenant, AgentVersion: version, OS: "linux", Arch: "amd64", InstallType: "deb",
		Capabilities: []string{store.CapUpgradeV1}}, Online: online, Available: true}
	for _, m := range mods {
		m(&c)
	}
	return c
}

func TestPlanAuto(t *testing.T) {
	now := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	p := pol("02:00", "04:00", "UTC")
	p.MaxConcurrent = 3
	cands := []Candidate{
		cand("a-z", "4.4.0", true),
		cand("a-b", "4.4.1", true),
		cand("a-a", "4.4.0", true),
		cand("a-old", "4.4.0-rc.1", true),
		cand("a-off", "4.4.0", false), // offline
		cand("a-man", "4.4.0", true, func(c *Candidate) { c.Agent.Capabilities = nil }), // incapable
		cand("a-uns", "4.4.0", true, func(c *Candidate) { c.Agent.InstallType = "" }),   // unsupported platform
		cand("a-act", "4.4.0", true, func(c *Candidate) { c.Last.State = store.UpgradeDownloading }),
		cand("a-uptodate", "4.5.0", true),
		cand("a-newer", "4.6.0", true),
		cand("a-dev", "dev", true),
		cand("a-norel", "4.4.0", true, func(c *Candidate) { c.Available = false }),
		cand("a-unsinst", "4.4.0", true, func(c *Candidate) {
			c.Last = store.AgentUpgrade{State: store.UpgradeFailed, Reason: ReasonUnsupportedInstall, TargetVersion: "4.4.9"}
		}),
		cand("a-failed", "4.4.0", true, func(c *Candidate) {
			c.Last = store.AgentUpgrade{State: store.UpgradeRolledBack, TargetVersion: "4.5.0"}
		}),
	}
	// Oldest version first, then agent id; capacity = max_concurrent - active.
	if got := PlanAuto(now, p, "4.5.0", false, cands, 0); !slices.Equal(got, []string{"a-old", "a-a", "a-z"}) {
		t.Fatalf("plan = %v", got)
	}
	if got := PlanAuto(now, p, "4.5.0", false, cands, 1); !slices.Equal(got, []string{"a-old", "a-a"}) {
		t.Fatalf("plan with 1 active = %v", got)
	}
	if got := PlanAuto(now, p, "4.5.0", false, cands, 3); got != nil {
		t.Fatalf("plan at capacity = %v", got)
	}
	p.MaxConcurrent = 100
	if got := PlanAuto(now, p, "4.5.0", false, cands, 0); !slices.Equal(got, []string{"a-old", "a-a", "a-z", "a-b"}) {
		t.Fatalf("plan all = %v", got)
	}
	// A lower pin downgrades newer agents (only the pin path allows it).
	if got := PlanAuto(now, p, "4.4.1", true, cands, 0); !slices.Equal(got, []string{"a-old", "a-a", "a-failed", "a-z", "a-uptodate", "a-newer"}) {
		t.Fatalf("pinned plan = %v", got)
	}
	// Outside the window, disabled, paused or without a target: nothing.
	if got := PlanAuto(now.Add(2*time.Hour), p, "4.5.0", false, cands, 0); got != nil {
		t.Fatalf("outside window = %v", got)
	}
	off := p
	off.Enabled = false
	paused := p
	paused.Paused = true
	for _, q := range []store.AgentUpgradePolicy{off, paused} {
		if got := PlanAuto(now, q, "4.5.0", false, cands, 0); got != nil {
			t.Fatalf("policy %+v planned %v", q, got)
		}
	}
	if got := PlanAuto(now, p, "", false, cands, 0); got != nil {
		t.Fatalf("no target planned %v", got)
	}
}
