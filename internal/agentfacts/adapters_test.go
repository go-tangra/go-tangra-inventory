package agentfacts

import (
	"fmt"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func TestFromAdapters(t *testing.T) {
	adapters := []Adapter{
		{
			Name: "Ethernet", Description: "Intel(R) Ethernet Connection", MAC: "00-11-22-33-44-55", IfType: ifTypeEthernet,
			Up: true, SpeedBps: 1_000_000_000, Ipv4Metric: 25, Ipv6Metric: 25,
			Gateways: []string{"fe80::1", "192.0.2.1"},
			Unicast: []UnicastAddr{
				{Address: "192.0.2.10", PrefixLength: 24, PrefixOrigin: prefixOriginDhcp, SuffixOrigin: suffixOriginDhcp, DadState: dadPreferred},
				{Address: "2001:db8::10", PrefixLength: 64, PrefixOrigin: prefixOriginRouterAdvertisement, SuffixOrigin: suffixOriginLinkLayer, DadState: dadPreferred},
				{Address: "2001:db8::beef", PrefixLength: 64, PrefixOrigin: prefixOriginRouterAdvertisement, SuffixOrigin: suffixOriginRandom, DadState: dadPreferred},
				{Address: "2001:db8::dead", PrefixLength: 64, PrefixOrigin: prefixOriginRouterAdvertisement, SuffixOrigin: suffixOriginRandom, DadState: dadDeprecated},
				{Address: "fe80::5%12", PrefixLength: 64, PrefixOrigin: prefixOriginWellKnown, SuffixOrigin: suffixOriginLinkLayer, DadState: dadPreferred},
				{Address: "bogus", PrefixLength: 8},
			},
		},
		{Name: "Wi-Fi", MAC: "aa-bb-cc-dd-ee-ff", IfType: ifTypeWireless, SpeedBps: ^uint64(0)},
		{Name: "Loopback Pseudo-Interface 1", IfType: ifTypeLoopback, Up: true,
			Unicast: []UnicastAddr{{Address: "127.0.0.1", PrefixLength: 8, PrefixOrigin: prefixOriginWellKnown, DadState: dadPreferred}}},
		{Name: "vEthernet (Default Switch)", MAC: "00-15-5d-00-00-01", IfType: ifTypeEthernet, Up: true,
			Gateways: []string{"172.16.0.1"}, Ipv4Metric: 5000,
			Unicast: []UnicastAddr{{Address: "172.16.0.5", PrefixLength: 20, PrefixOrigin: prefixOriginManual, SuffixOrigin: suffixOriginManual, DadState: dadPreferred}}},
		{Name: "Team", Description: "Microsoft Network Adapter Multiplexor Driver", IfType: ifTypeEthernet},
		{Name: "VPN", IfType: ifTypeTunnel},
		{Name: "PPP", IfType: ifTypePropVirtual},
		{Name: "Modem", IfType: 23},
		{Name: "", IfType: ifTypeEthernet}, // unnamed: skipped
	}
	res := FromAdapters(adapters)
	if len(res.Interfaces) != 8 {
		t.Fatalf("interfaces = %+v", res.Interfaces)
	}
	eth := res.Interfaces[0]
	if eth.Type != store.IfaceEthernet || eth.MAC != "00:11:22:33:44:55" || eth.SpeedBps != 1e9 || !eth.DHCP || !eth.Up ||
		!eth.DefaultRoute || eth.Gateway != "192.0.2.1" || len(eth.Addresses) != 5 {
		t.Fatalf("ethernet = %+v", eth)
	}
	a := eth.Addresses
	if !a[0].DHCP || a[0].Scope != "global" || a[0].Family != "ipv4" ||
		!a[1].DHCP || a[1].Temporary ||
		!a[2].Temporary || !a[2].DHCP ||
		!a[3].Deprecated ||
		a[4].Address != "fe80::5" || a[4].Scope != "link" || a[4].DHCP {
		t.Fatalf("addresses = %+v", a)
	}
	want := map[string]string{"Wi-Fi": store.IfaceWireless, "Loopback Pseudo-Interface 1": store.IfaceLoopback,
		"vEthernet (Default Switch)": store.IfaceVirtual, "Team": store.IfaceBond, "VPN": store.IfaceVirtual,
		"PPP": store.IfaceVirtual, "Modem": store.IfaceOther}
	for _, n := range res.Interfaces[1:] {
		if n.Type != want[n.Name] {
			t.Errorf("%s kind = %q", n.Name, n.Type)
		}
	}
	if w := res.Interfaces[1]; w.SpeedBps != 0 || w.Up {
		t.Fatalf("wifi = %+v", w)
	}
	if res.Interfaces[2].Addresses[0].Scope != "host" {
		t.Fatal("loopback scope")
	}
	// Lowest metric default-route interface wins: Ethernet (25) over vEthernet (5000).
	if res.PrimaryIPv4 != "192.0.2.10" || res.PrimaryIPv6 != "2001:db8::10" {
		t.Fatalf("primary = %q %q", res.PrimaryIPv4, res.PrimaryIPv6)
	}
}

func TestFromAdaptersMetricAndBounds(t *testing.T) {
	adapters := []Adapter{
		{Name: "A", IfType: ifTypeEthernet, Gateways: []string{"10.0.0.1"}, Ipv4Metric: 50,
			Unicast: []UnicastAddr{{Address: "10.0.0.2", PrefixLength: 24, DadState: dadPreferred}}},
		{Name: "B", IfType: ifTypeEthernet, Gateways: []string{"10.1.0.1"}, Ipv4Metric: 10,
			Unicast: []UnicastAddr{{Address: "10.1.0.2", PrefixLength: 24, DadState: dadPreferred}}},
	}
	if res := FromAdapters(adapters); res.PrimaryIPv4 != "10.1.0.2" {
		t.Fatalf("primary = %q", res.PrimaryIPv4)
	}
	var many []Adapter
	for i := 0; i < 260; i++ {
		many = append(many, Adapter{Name: string(rune('A'+i%26)) + string(rune('a'+i/26)), IfType: ifTypeEthernet})
	}
	for i := 0; i < 70; i++ {
		many[0].Unicast = append(many[0].Unicast, UnicastAddr{Address: fmt.Sprintf("10.0.0.%d", i+1), PrefixLength: 24})
	}
	res := FromAdapters(many)
	if len(res.Interfaces) != store.MaxInterfaces || res.Truncated.Interfaces != 4 || res.Truncated.Addresses != 70-store.MaxIfaceAddresses {
		t.Fatalf("bounds: %d %+v", len(res.Interfaces), res.Truncated)
	}
}
