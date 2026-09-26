package diff_test

import (
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/diff"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func netInv() store.Inventory {
	return store.Inventory{Networks: []store.NetIface{
		{Name: "eth0", MAC: "00:11:22:33:44:55", Type: store.IfaceEthernet, SpeedBps: 1e9, Gateway: "192.0.2.1", DHCP: true, Up: true,
			Master: "bond0", Addresses: []store.IfAddress{{Address: "192.0.2.10", PrefixLength: 24, Family: "ipv4", DHCP: true, Scope: "global"},
				{Address: "2001:db8::abcd", PrefixLength: 64, Family: "ipv6", Temporary: true, Scope: "global"}}},
		// bond and vlan share the slave's MAC: all three must be tracked.
		{Name: "bond0", MAC: "00:11:22:33:44:55", Type: store.IfaceBond},
		{Name: "bond0.10", MAC: "00:11:22:33:44:55", Type: store.IfaceVLAN, VLANID: 10},
	}}
}

func TestDiff_NetworkFields(t *testing.T) {
	base := netInv()
	if got := diff.Diff(base, netInv()); len(got) != 0 {
		t.Fatalf("self diff = %+v", got)
	}
	muts := map[string]func(*store.NetIface){
		"type":      func(n *store.NetIface) { n.Type = store.IfaceOther },
		"speed_bps": func(n *store.NetIface) { n.SpeedBps = 1e10 },
		"gateway":   func(n *store.NetIface) { n.Gateway = "192.0.2.254" },
		"dhcp":      func(n *store.NetIface) { n.DHCP = false },
		"addresses": func(n *store.NetIface) { n.Addresses[0].Address = "192.0.2.11" },
		"prefix":    func(n *store.NetIface) { n.Addresses[0].PrefixLength = 25 },
	}
	for name, m := range muts {
		next := netInv()
		m(&next.Networks[0])
		got := diff.Diff(base, next)
		if len(got) != 1 || got[0].Category != diff.CatNetwork || got[0].ChangeType != store.ChangeModified ||
			!strings.HasPrefix(got[0].ComponentKey, "eth0") {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// A rotated IPv6 privacy address or a deprecation flag is not a change.
	next := netInv()
	next.Networks[0].Addresses[1].Address = "2001:db8::beef"
	next.Networks[0].Addresses[1].Deprecated = true
	next.Networks[0].IPAddresses = []string{"2001:db8::beef/64"}
	if got := diff.Diff(base, next); len(got) != 0 {
		t.Fatalf("privacy address rotation recorded: %+v", got)
	}
	// Removing the VLAN is tracked separately from its parent with the same MAC.
	next = netInv()
	next.Networks = next.Networks[:2]
	got := diff.Diff(base, next)
	if len(got) != 1 || got[0].ChangeType != store.ChangeRemoved || !strings.HasPrefix(got[0].ComponentKey, "bond0.10") {
		t.Fatalf("vlan removal = %+v", got)
	}
}

func TestDiff_SoftwareAvailableVersion(t *testing.T) {
	prev := store.Inventory{Programs: []store.Program{{Name: "openssl", Version: "3.0.1"}}}
	next := store.Inventory{Programs: []store.Program{{Name: "openssl", Version: "3.0.1", AvailableVersion: "3.0.2", SecurityUpdate: true}}}
	got := diff.Diff(prev, next)
	if len(got) != 1 || got[0].Category != diff.CatSoftware || got[0].ChangeType != store.ChangeModified ||
		!strings.Contains(got[0].After, "3.0.2") {
		t.Fatalf("software = %+v", got)
	}
}

func TestDiff_HostReportCategories(t *testing.T) {
	prev := store.Inventory{
		Virtualization: store.Virtualization{Role: store.RoleVM, Kind: "kvm", Source: "dmi"},
		Bmc:            &store.Bmc{Address: "10.0.0.5"},
		HypervisorGuests: []store.HypervisorGuest{{ID: "100", Name: "a"}, {ID: "101", Name: "b"}},
		UpdateState: store.UpdateState{PackageManager: "apt", Status: store.UpdateUpToDate, RebootRequired: store.TriFalse,
			CheckedAt: time.Unix(1, 0).UTC()},
	}
	same := prev
	same.UpdateState.CheckedAt = time.Unix(99999, 0).UTC() // only the check time moved
	same.Bmc = &store.Bmc{Address: "10.0.0.5"}
	if got := diff.Diff(prev, same); len(got) != 0 {
		t.Fatalf("unchanged report recorded: %+v", got)
	}

	next := prev
	next.Virtualization = store.Virtualization{Role: store.RolePhysical, Source: "dmi"}
	next.Bmc = &store.Bmc{Address: "10.0.0.6"}
	next.HypervisorGuests = []store.HypervisorGuest{{ID: "100", Name: "renamed"}, {ID: "102", Name: "c"}}
	next.UpdateState = store.UpdateState{PackageManager: "apt", Status: store.UpdateAvailable, RebootRequired: store.TriTrue, PendingCount: 3}
	got := diff.Diff(prev, next)
	want := map[string]string{
		diff.CatVirtualization + "/" + diff.CatVirtualization: store.ChangeModified,
		diff.CatBMC + "/" + diff.CatBMC:                       store.ChangeModified,
		diff.CatGuest + "/100":                                store.ChangeModified,
		diff.CatGuest + "/101":                                store.ChangeRemoved,
		diff.CatGuest + "/102":                                store.ChangeAdded,
		diff.CatUpdate + "/" + diff.CatUpdate:                 store.ChangeModified,
	}
	if len(got) != len(want) {
		t.Fatalf("changes = %+v", got)
	}
	for _, c := range got {
		if want[c.Category+"/"+c.ComponentKey] != c.ChangeType {
			t.Errorf("unexpected %s/%s %s", c.Category, c.ComponentKey, c.ChangeType)
		}
	}
	// BMC appearing / disappearing.
	noBmc := prev
	noBmc.Bmc = nil
	if c, ok := find(diff.Diff(noBmc, prev), diff.CatBMC, diff.CatBMC); !ok || c.ChangeType != store.ChangeAdded {
		t.Fatalf("bmc added = %+v", c)
	}
	if c, ok := find(diff.Diff(prev, noBmc), diff.CatBMC, diff.CatBMC); !ok || c.ChangeType != store.ChangeRemoved {
		t.Fatalf("bmc removed = %+v", c)
	}
}
