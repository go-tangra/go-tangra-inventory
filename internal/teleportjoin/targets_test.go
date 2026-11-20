package teleportjoin

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func TestCapability(t *testing.T) {
	linux := &store.Agent{OS: "linux", Capabilities: []string{store.CapTeleportV1}}
	for name, c := range map[string]struct {
		enabled, ambiguous bool
		a                  *store.Agent
		want               string
	}{
		"disabled":      {false, false, linux, CapabilityDisabledOnServer},
		"no agent":      {true, false, nil, CapabilityNoAgent},
		"ambiguous":     {true, true, linux, CapabilityAmbiguousAgent},
		"windows":       {true, false, &store.Agent{OS: "windows"}, CapabilityNotSupported},
		"enabled":       {true, false, linux, CapabilityEnabled},
		"old linux":     {true, false, &store.Agent{OS: "linux", Capabilities: []string{store.CapModuleV1}}, CapabilityUpgradeRequired},
		"no platform":   {true, false, &store.Agent{}, CapabilityUpgradeRequired},
		"cap, platform": {true, false, &store.Agent{Capabilities: []string{store.CapTeleportV1}}, CapabilityEnabled},
	} {
		if got := Capability(c.enabled, c.a, c.ambiguous); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

// TestListTargetsQuery: non-retired hosts matching the query, by hostname,
// with their facts, presence and capability; the limit truncates.
func TestListTargetsQuery(t *testing.T) {
	f := newFix(t)
	f.tpHost(t, "web-2", true)
	f.tpHost(t, "web-1", false)
	f.host(t, "web-3")
	f.host(t, "db-1")
	f.tpHost(t, "web-old", true)
	if err := f.mem.RetireHost(ctx, tenant, f.hosts["web-old"].ID); err != nil {
		t.Fatal(err)
	}
	got, truncated, err := f.svc.ListTargets(ctx, tenant, TargetQuery{Query: "web"})
	if err != nil || truncated || len(got) != 3 {
		t.Fatalf("targets = %+v %v %v", got, truncated, err)
	}
	if got[0].Hostname != "web-1" || got[1].Hostname != "web-2" || got[2].Hostname != "web-3" {
		t.Fatalf("order = %+v", got)
	}
	w2 := got[1]
	if !w2.AgentOnline || w2.Capability != CapabilityEnabled || w2.OSName != "Debian" || w2.OSVersion != "12" || w2.OSArch != "amd64" ||
		w2.Manufacturer != "Dell Inc." || w2.Model != "R650" || w2.Tags["env"] != "prod" || w2.HostID != f.hosts["web-2"].ID {
		t.Fatalf("web-2 = %+v", w2)
	}
	if got[0].AgentOnline || got[2].Capability != CapabilityNoAgent {
		t.Fatalf("web-1/web-3 = %+v %+v", got[0], got[2])
	}
	got, truncated, err = f.svc.ListTargets(ctx, tenant, TargetQuery{Limit: 2})
	if err != nil || !truncated || len(got) != 2 || got[0].Hostname != "db-1" {
		t.Fatalf("limited = %+v %v %v", got, truncated, err)
	}
}

// TestListTargetsHostIDs: exactly the hosts asked (unknown omitted,
// repeated once), retired ones with capability retired; never truncated.
func TestListTargetsHostIDs(t *testing.T) {
	f := newFix(t)
	a, _ := f.tpHost(t, "web-1", true)
	b, _ := f.tpHost(t, "web-2", true)
	f.host(t, "web-3")
	if err := f.mem.RetireHost(ctx, tenant, b.ID); err != nil {
		t.Fatal(err)
	}
	got, truncated, err := f.svc.ListTargets(ctx, tenant, TargetQuery{Limit: 1, HostIDs: []string{b.ID, strings.ToUpper(a.ID), a.ID, eid(9)}})
	if err != nil || truncated || len(got) != 2 || got[0].HostID != a.ID || got[1].Capability != CapabilityRetired {
		t.Fatalf("by id = %+v %v %v", got, truncated, err)
	}
}

func TestListTargetsValidation(t *testing.T) {
	f := newFix(t)
	many := make([]string, MaxTargetHosts+1)
	for i := range many {
		many[i] = eid(i)
	}
	for name, q := range map[string]TargetQuery{
		"query long":     {Query: strings.Repeat("a", MaxTargetQuery+1)},
		"query utf8":     {Query: "\xff"},
		"limit negative": {Limit: -1},
		"limit high":     {Limit: MaxTargetLimit + 1},
		"too many ids":   {HostIDs: many},
		"ids and query":  {Query: "web", HostIDs: []string{eid(1)}},
		"id malformed":   {HostIDs: []string{"web-1"}},
	} {
		var ie *InvalidError
		if _, _, err := f.svc.ListTargets(ctx, tenant, q); !errors.As(err, &ie) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestListTargetsErrors(t *testing.T) {
	f := newFix(t)
	h, _ := f.tpHost(t, "web-1", true)
	for _, method := range []string{"ListHosts", "ListAgents"} {
		f.mem.FailNext(method)
		if _, _, err := f.svc.ListTargets(ctx, tenant, TargetQuery{}); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
	f.mem.FailNext("GetHost")
	if _, _, err := f.svc.ListTargets(ctx, tenant, TargetQuery{HostIDs: []string{h.ID}}); err == nil {
		t.Error("GetHost: no error")
	}
	f.reg.failList = errBoom
	if _, _, err := f.svc.ListTargets(ctx, tenant, TargetQuery{}); !errors.Is(err, errBoom) {
		t.Errorf("ListConnected: %v", err)
	}
}

// TestHostAgents: agents without a host are skipped; of two agents on a
// host the most recently seen one is kept and the host is ambiguous.
func TestHostAgents(t *testing.T) {
	agents := []store.Agent{{ID: "a0"}, {ID: "a1", HostID: "h1", LastSeen: t0}, {ID: "a2", HostID: "h1", LastSeen: t0.Add(time.Second)},
		{ID: "a3", HostID: "h2", LastSeen: t0}}
	of, amb := hostAgents(agents)
	if len(of) != 2 || of["h1"].ID != "a2" || of["h2"].ID != "a3" || !amb["h1"] || amb["h2"] {
		t.Fatalf("agents %v ambiguous %v", of, amb)
	}
}

// TestListTargetsSameHostname: equal hostnames are ordered by host id.
func TestListTargetsSameHostname(t *testing.T) {
	f := newFix(t)
	for _, mid := range []string{"m1", "m2"} {
		if _, err := f.mem.ResolveHost(ctx, tenant, store.Host{Hostname: "twin", MachineID: mid, Status: store.HostActive}); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := f.svc.ListTargets(ctx, tenant, TargetQuery{Query: "twin"})
	if err != nil || len(got) != 2 || got[0].HostID > got[1].HostID {
		t.Fatalf("twins = %+v %v", got, err)
	}
}
