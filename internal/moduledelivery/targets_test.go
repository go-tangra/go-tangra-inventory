package moduledelivery

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func snapshot(hostID string, inv store.Inventory) store.Snapshot {
	return store.Snapshot{ID: store.NewID(), TenantID: tenant, HostID: hostID, CollectedAt: t0, ReceivedAt: t0, Payload: inv}
}

// TestListTargets (T006): capability per host, online flag, the latest
// snapshot's addresses, hostname filter, order, limit and truncation;
// retired hosts are not targets.
func TestListTargets(t *testing.T) {
	f := newFix(t)
	gw, _ := f.modHost(t, "gw-1", true)
	f.host(t, "bare")
	f.host(t, "win")
	f.agentFor(t, "win", "windows", "4.8.0", false)
	f.modHost(t, "gone", true)
	_ = f.mem.RetireHost(ctx, tenant, f.hosts["gone"].ID)
	old := snapshot(gw.ID, store.Inventory{PrimaryIPv4: "10.0.0.9"})
	old.CollectedAt = t0.Add(-time.Hour)
	_ = f.mem.InsertSnapshot(ctx, old)
	_ = f.mem.InsertSnapshot(ctx, snapshot(gw.ID, store.Inventory{PrimaryIPv4: "192.0.2.10", PrimaryIPv6: "2001:db8::10",
		Networks: []store.NetIface{
			{Name: "lo", Type: store.IfaceLoopback, Addresses: []store.IfAddress{{Address: "127.0.0.1"}}},
			{Name: "eth0", Type: store.IfaceEthernet, Addresses: []store.IfAddress{{Address: "192.0.2.10"}, {Address: "fe80::1"},
				{Address: "198.51.100.7"}}, IPAddresses: []string{"203.0.113.5/24", "::ffff:198.51.100.8", "garbage", "0.0.0.0", "224.0.0.1"}},
		}}))
	hosts, truncated, err := f.svc.ListTargets(ctx, tenant, "", 0)
	if err != nil || truncated || len(hosts) != 3 {
		t.Fatalf("targets = %+v %v %v", hosts, truncated, err)
	}
	if hosts[0].Hostname != "bare" || hosts[0].Capability != CapabilityNoAgent || len(hosts[0].IPAddresses) != 0 || hosts[0].IPAddresses == nil {
		t.Fatalf("bare = %+v", hosts[0])
	}
	g := hosts[1]
	if g.HostID != gw.ID || g.Capability != CapabilityEnabled || !g.AgentOnline || g.OSName != "Debian" ||
		strings.Join(g.IPAddresses, ",") != "192.0.2.10,2001:db8::10,198.51.100.7,203.0.113.5,198.51.100.8" {
		t.Fatalf("gw-1 = %+v", g)
	}
	if hosts[2].Capability != CapabilityNotSupported || hosts[2].AgentOnline {
		t.Fatalf("win = %+v", hosts[2])
	}
	// Two hosts of the same name are ordered by id.
	twin, _ := f.mem.ResolveHost(ctx, tenant, store.Host{Hostname: "bare", MachineID: "mid-twin", Status: store.HostActive})
	if hosts, _, _ := f.svc.ListTargets(ctx, tenant, "bare", 0); len(hosts) != 2 || (hosts[0].HostID > hosts[1].HostID) || twin.ID == "" {
		t.Fatalf("twins = %+v", hosts)
	}
	if hosts, truncated, _ := f.svc.ListTargets(ctx, tenant, "GW", 0); len(hosts) != 1 || truncated {
		t.Fatalf("query = %+v", hosts)
	}
	if hosts, truncated, _ := f.svc.ListTargets(ctx, tenant, "", 3); len(hosts) != 3 || !truncated {
		t.Fatalf("limit = %+v %v", hosts, truncated)
	}
	for name, c := range map[string]struct {
		q string
		n int
	}{"query": {strings.Repeat("a", 65), 0}, "query utf8": {"\xff", 0}, "limit low": {"", -1}, "limit high": {"", 201}} {
		var ie *InvalidError
		if _, _, err := f.svc.ListTargets(ctx, tenant, c.q, c.n); !errors.As(err, &ie) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, method := range []string{"ListHosts", "ListAgents", "GetLatestForHost"} {
		f.mem.FailNext(method)
		if _, _, err := f.svc.ListTargets(ctx, tenant, "", 0); err == nil {
			t.Errorf("%s failure hidden", method)
		}
	}
	f.reg.failList = errBoom
	if _, _, err := f.svc.ListTargets(ctx, tenant, "", 0); err == nil {
		t.Fatal("registry failure hidden")
	}
}

func TestSnapshotIPsBounded(t *testing.T) {
	var addrs []store.IfAddress
	for i := 1; i < 40; i++ {
		addrs = append(addrs, store.IfAddress{Address: fmt.Sprintf("10.0.0.%d", i)})
	}
	got := snapshotIPs(store.Inventory{Networks: []store.NetIface{{Addresses: addrs}}})
	if len(got) != MaxTargetIPs {
		t.Fatalf("ips = %v", got)
	}
}

func TestCapability(t *testing.T) {
	a := &store.Agent{OS: "linux", AgentVersion: "4.8.0", Capabilities: []string{store.CapModuleV1}}
	if Capability(false, a, false) != CapabilityDisabledOnServer || Capability(true, a, false) != CapabilityEnabled ||
		Capability(true, a, true) != CapabilityAmbiguousAgent {
		t.Fatal("capability")
	}
	// Agents without a host binding are ignored; the newest of two agents wins.
	older := store.Agent{ID: "a1", HostID: "h", LastSeen: t0}
	newer := store.Agent{ID: "a2", HostID: "h", LastSeen: t0.Add(time.Hour)}
	agentOf, amb := hostAgents([]store.Agent{{ID: "x"}, older, newer})
	if agentOf["h"].ID != "a2" || !amb["h"] || len(agentOf) != 1 {
		t.Fatalf("hostAgents = %v %v", agentOf, amb)
	}
}

func TestInternals(t *testing.T) {
	if utcNow().Location() != time.UTC {
		t.Fatal("utcNow")
	}
	f := newFix(t)
	if f.svc.online(ctx, "") {
		t.Fatal("online without agent")
	}
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	f.svc.markDelivered(ctx, v.ModuleDeliveryItem) // already delivered: no second row
	if f.count("module_delivery_delivered") != 1 || a.ID == "" {
		t.Fatal("delivered twice")
	}
}
