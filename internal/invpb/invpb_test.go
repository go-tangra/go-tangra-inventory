package invpb

import (
	"reflect"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func TestRoundTrip(t *testing.T) {
	ifs := []store.NetIface{{
		Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", IPAddresses: []string{"192.0.2.1/24"}, Gateway: "192.0.2.254",
		DHCP: true, SpeedBps: 1e9, Type: store.IfaceEthernet, Up: true, DefaultRoute: true, Master: "bond0", VLANID: 3,
		Addresses: []store.IfAddress{{Address: "192.0.2.1", PrefixLength: 24, Family: "ipv4", DHCP: true, Scope: "global"},
			{Address: "fe80::1", PrefixLength: 64, Family: "ipv6", Temporary: true, Deprecated: true, Scope: "link"}},
	}}
	if got := NetIfacesFromPB(NetIfacesToPB(ifs)); !reflect.DeepEqual(got, ifs) {
		t.Fatalf("ifaces round trip:\n got %+v\nwant %+v", got, ifs)
	}
	if NetIfacesToPB(nil) != nil || NetIfacesFromPB(nil) != nil {
		t.Fatal("empty lists must map to nil")
	}

	v := store.Virtualization{Role: store.RoleVM, Kind: "kvm", Source: "dmi"}
	if got := VirtualizationFromPB(VirtualizationToPB(v)); got != v {
		t.Fatalf("virt = %+v", got)
	}
	if VirtualizationToPB(store.Virtualization{}) != nil || VirtualizationFromPB(nil) != (store.Virtualization{}) {
		t.Fatal("zero virtualization")
	}

	b := &store.Bmc{Address: "10.0.0.2", PrefixLength: 24, Gateway: "10.0.0.1", IPSource: "static", VLANID: 9,
		Ports: []store.BmcPort{{Channel: 1, MAC: "00:11:22:33:44:55", Address: "10.0.0.2"}}}
	if got := BmcFromPB(BmcToPB(b)); !reflect.DeepEqual(got, b) {
		t.Fatalf("bmc = %+v", got)
	}
	if BmcToPB(nil) != nil || BmcFromPB(nil) != nil {
		t.Fatal("nil bmc")
	}

	g := []store.HypervisorGuest{{ID: "100", Name: "vm", Kind: "vm", Platform: "proxmox", MACs: []string{"bc:24:11:00:00:01"}}}
	if got := GuestsFromPB(GuestsToPB(g)); !reflect.DeepEqual(got, g) {
		t.Fatalf("guests = %+v", got)
	}
	if GuestsToPB(nil) != nil || GuestsFromPB(nil) != nil {
		t.Fatal("empty guests")
	}

	u := store.UpdateState{PackageManager: "apt", Status: store.UpdateAvailable, RebootRequired: store.TriTrue,
		AutomaticUpdates: store.TriFalse, SecurityClassified: true, CheckedAt: time.Unix(1_700_000_000, 0).UTC(),
		PendingCount: 3, SecurityCount: 1}
	if got := UpdateStateFromPB(UpdateStateToPB(u)); got != u {
		t.Fatalf("update = %+v", got)
	}
	if UpdateStateToPB(store.UpdateState{}) != nil || !UpdateStateFromPB(nil).CheckedAt.IsZero() {
		t.Fatal("zero update state")
	}

	l := store.CollectionLimits{Interfaces: 1, Addresses: 2, Guests: 3, Packages: 4, BmcPorts: 5}
	if got := LimitsFromPB(LimitsToPB(l)); got != l {
		t.Fatalf("limits = %+v", got)
	}
	if LimitsToPB(store.CollectionLimits{}) != nil {
		t.Fatal("zero limits")
	}
}
