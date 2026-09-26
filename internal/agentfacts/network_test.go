package agentfacts

import (
	"encoding/binary"
	"fmt"
	iofs "io/fs"
	"net/netip"
	"testing"
	"testing/fstest"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// ---- netlink message builders (the kernel's wire layout, native endian)

func nlAttr(typ uint16, data []byte) []byte {
	l := 4 + len(data)
	b := make([]byte, (l+3)&^3)
	binary.NativeEndian.PutUint16(b[0:], uint16(l))
	binary.NativeEndian.PutUint16(b[2:], typ)
	copy(b[4:], data)
	return b
}

func nlMsg(typ uint16, body []byte) []byte {
	b := make([]byte, 16+len(body))
	binary.NativeEndian.PutUint32(b[0:], uint32(len(b)))
	binary.NativeEndian.PutUint16(b[4:], typ)
	copy(b[16:], body)
	return b
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.NativeEndian.PutUint32(b, v)
	return b
}

// addrMsg is one RTM_NEWADDR: family, prefix, ifa_flags byte, scope, index,
// optional IFA_FLAGS, and the address (IFA_LOCAL for v4, IFA_ADDRESS for v6).
func addrMsg(addr string, prefix uint8, flags uint32, scope uint8, index uint32, withIFAFlags bool) []byte {
	a := netip.MustParseAddr(addr)
	fam := uint8(afInet)
	if a.Is6() {
		fam = afInet6
	}
	body := []byte{fam, prefix, byte(flags), scope}
	body = append(body, u32(index)...)
	raw := a.AsSlice()
	if a.Is4() {
		body = append(body, nlAttr(ifaAddress, netip.MustParseAddr("192.0.2.254").AsSlice())...) // peer, ignored for v4
		body = append(body, nlAttr(ifaLocal, raw)...)
	} else {
		body = append(body, nlAttr(ifaAddress, raw)...)
	}
	if withIFAFlags {
		body = append(body, nlAttr(ifaFlags, u32(flags))...)
	}
	return nlMsg(rtmNewAddr, body)
}

// routeMsg is one RTM_NEWROUTE in the main table.
func routeMsg(family uint8, dstLen uint8, gw string, oif, prio uint32, table uint8, rtype uint8) []byte {
	body := []byte{family, dstLen, 0, 0, table, 0, 0, rtype, 0, 0, 0, 0}
	if gw != "" {
		body = append(body, nlAttr(rtaGateway, netip.MustParseAddr(gw).AsSlice())...)
	}
	body = append(body, nlAttr(rtaOIF, u32(oif))...)
	if prio > 0 {
		body = append(body, nlAttr(rtaPriority, u32(prio))...)
	}
	return body
}

func routeNL(family uint8, dstLen uint8, gw string, oif, prio uint32) []byte {
	return nlMsg(rtmNewRoute, routeMsg(family, dstLen, gw, oif, prio, rtTableMain, rtnUnicast))
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestParseAddrMessages(t *testing.T) {
	dump := concat(
		addrMsg("192.0.2.10", 24, 0, scopeUniverse, 2, false),                               // dynamic (no PERMANENT) → dhcp
		addrMsg("10.1.1.1", 16, ifaFPermanent, scopeUniverse, 2, true),                      // static
		addrMsg("2001:db8::abcd", 64, ifaFTemporary|ifaFDeprecated, scopeUniverse, 2, true), // privacy, deprecated
		addrMsg("fe80::1", 64, ifaFPermanent, scopeLink, 2, false),
		addrMsg("127.0.0.1", 8, ifaFPermanent, scopeHost, 1, false),
		addrMsg("fec0::1", 10, ifaFPermanent, scopeSite, 2, false),
		nlMsg(nlmsgDone, nil),
		addrMsg("198.51.100.1", 24, 0, 0, 9, false), // after DONE: ignored
	)
	got, err := ParseAddrMessages(dump)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("entries = %d: %+v", len(got), got)
	}
	want := []store.IfAddress{
		{Address: "192.0.2.10", PrefixLength: 24, Family: "ipv4", DHCP: true, Scope: "global"},
		{Address: "10.1.1.1", PrefixLength: 16, Family: "ipv4", Scope: "global"},
		{Address: "2001:db8::abcd", PrefixLength: 64, Family: "ipv6", DHCP: true, Temporary: true, Deprecated: true, Scope: "global"},
		{Address: "fe80::1", PrefixLength: 64, Family: "ipv6", Scope: "link"},
		{Address: "127.0.0.1", PrefixLength: 8, Family: "ipv4", Scope: "host"},
		{Address: "fec0::1", PrefixLength: 10, Family: "ipv6", Scope: "site"},
	}
	for i, w := range want {
		if got[i].Addr != w {
			t.Errorf("entry %d = %+v, want %+v", i, got[i].Addr, w)
		}
	}
	if got[0].Index != 2 || got[4].Index != 1 {
		t.Fatalf("indexes = %d %d", got[0].Index, got[4].Index)
	}
}

func TestParseAddrMessagesErrors(t *testing.T) {
	if _, err := ParseAddrMessages([]byte{1, 2, 3}); err == nil {
		t.Fatal("short header accepted")
	}
	bad := nlMsg(rtmNewAddr, []byte{afInet, 24})
	if _, err := ParseAddrMessages(bad); err == nil {
		t.Fatal("short ifaddrmsg accepted")
	}
	errMsg := nlMsg(nlmsgError, u32(0xffffffff))
	if _, err := ParseAddrMessages(errMsg); err == nil {
		t.Fatal("NLMSG_ERROR accepted")
	}
	// Broken attribute length: the entry is skipped, parsing continues.
	body := append([]byte{afInet, 24, 0, 0}, u32(3)...)
	body = append(body, 0xff, 0x00, 0x02, 0x00)
	got, err := ParseAddrMessages(concat(nlMsg(rtmNewAddr, body), addrMsg("10.0.0.1", 8, 0, 0, 3, false)))
	if err != nil || len(got) != 1 {
		t.Fatalf("broken attr: %v %v", got, err)
	}
	// Unknown family and wrong-size address are skipped.
	other := append([]byte{7, 24, 0, 0}, u32(3)...)
	other = append(other, nlAttr(ifaAddress, []byte{1, 2, 3, 4})...)
	short := append([]byte{afInet, 24, 0, 0}, u32(3)...)
	short = append(short, nlAttr(ifaLocal, []byte{1, 2, 3})...)
	got, _ = ParseAddrMessages(concat(nlMsg(rtmNewAddr, other), nlMsg(rtmNewAddr, short), nlMsg(99, nil)))
	if len(got) != 0 {
		t.Fatalf("junk parsed: %+v", got)
	}
}

func TestParseRouteMessages(t *testing.T) {
	dump := concat(
		routeNL(afInet, 0, "192.0.2.1", 2, 100),
		routeNL(afInet, 0, "10.1.0.1", 3, 50),
		routeNL(afInet, 24, "", 2, 0), // not a default route
		routeNL(afInet6, 0, "fe80::1", 2, 1024),
		nlMsg(rtmNewRoute, routeMsg(afInet, 0, "198.51.100.1", 4, 1, 100, rtnUnicast)),                            // other table
		nlMsg(rtmNewRoute, routeMsg(afInet, 0, "", 5, 1, rtTableMain, 7)),                                         // blackhole-ish type
		nlMsg(rtmNewRoute, append(routeMsg(afInet, 0, "", 6, 0, 252, rtnUnicast), nlAttr(rtaTable, u32(254))...)), // RTA_TABLE main
		nlMsg(nlmsgDone, nil),
	)
	got, err := ParseRouteMessages(dump)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("routes = %+v", got)
	}
	if got[0] != (DefaultRoute{Index: 2, Gateway: "192.0.2.1", Metric: 100, Family: "ipv4"}) ||
		got[2] != (DefaultRoute{Index: 2, Gateway: "fe80::1", Metric: 1024, Family: "ipv6"}) ||
		got[3].Index != 6 || got[3].Gateway != "" {
		t.Fatalf("routes = %+v", got)
	}
	if _, err := ParseRouteMessages(nlMsg(rtmNewRoute, []byte{1, 2})); err == nil {
		t.Fatal("short rtmsg accepted")
	}
}

func sysfsFixture() fstest.MapFS {
	f := fstest.MapFS{}
	put := func(name, file, val string) { f[name+"/"+file] = &fstest.MapFile{Data: []byte(val + "\n")} }
	dir := func(name, sub string) { f[name+"/"+sub] = &fstest.MapFile{Mode: iofs.ModeDir | 0o755} }
	// eth0: physical ethernet, enslaved to bond0
	put("eth0", "address", "00:11:22:33:44:55")
	put("eth0", "type", "1")
	put("eth0", "speed", "1000")
	put("eth0", "operstate", "up")
	dir("eth0", "device")
	f["eth0/master"] = &fstest.MapFile{Data: []byte("../../bond0"), Mode: iofs.ModeSymlink | 0o777}
	// bond0
	put("bond0", "address", "00:11:22:33:44:55")
	put("bond0", "type", "1")
	put("bond0", "operstate", "up")
	put("bond0", "speed", "2000")
	dir("bond0", "bonding")
	// br0
	put("br0", "address", "aa:aa:aa:aa:aa:01")
	put("br0", "type", "1")
	put("br0", "operstate", "unknown")
	dir("br0", "bridge")
	// wlan0
	put("wlan0", "address", "aa:aa:aa:aa:aa:02")
	put("wlan0", "type", "1")
	put("wlan0", "operstate", "down")
	put("wlan0", "speed", "-1")
	dir("wlan0", "device")
	dir("wlan0", "wireless")
	// eth0.100 vlan
	put("eth0.100", "address", "00:11:22:33:44:55")
	put("eth0.100", "type", "1")
	put("eth0.100", "operstate", "up")
	// veth (no device link) → virtual
	put("veth1", "address", "aa:aa:aa:aa:aa:03")
	put("veth1", "type", "1")
	put("veth1", "operstate", "up")
	// lo
	put("lo", "address", "00:00:00:00:00:00")
	put("lo", "type", "772")
	put("lo", "operstate", "unknown")
	// ib0: infiniband (type 32) with device → other
	put("ib0", "type", "32")
	put("ib0", "operstate", "up")
	dir("ib0", "device")
	return f
}

func TestReadSysfsIfaceKinds(t *testing.T) {
	fs := sysfsFixture()
	vlans := ParseVLANConfig("VLAN Dev name\t | VLAN ID\nName-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD\neth0.100       | 100  | eth0\nbad line\nx | notnum | y\n")
	cases := map[string]struct {
		kind   string
		speed  uint64
		up     bool
		master string
		vlan   uint32
		mac    string
	}{
		"eth0":     {store.IfaceEthernet, 1_000_000_000, true, "bond0", 0, "00:11:22:33:44:55"},
		"bond0":    {store.IfaceBond, 2_000_000_000, true, "", 0, "00:11:22:33:44:55"},
		"br0":      {store.IfaceBridge, 0, true, "", 0, "aa:aa:aa:aa:aa:01"},
		"wlan0":    {store.IfaceWireless, 0, false, "", 0, "aa:aa:aa:aa:aa:02"},
		"eth0.100": {store.IfaceVLAN, 0, true, "", 100, "00:11:22:33:44:55"},
		"veth1":    {store.IfaceVirtual, 0, true, "", 0, "aa:aa:aa:aa:aa:03"},
		"lo":       {store.IfaceLoopback, 0, true, "", 0, ""},
		"ib0":      {store.IfaceOther, 0, true, "", 0, ""},
	}
	for name, want := range cases {
		f := ReadSysfsIface(fs, name, vlans)
		got := f.Iface()
		if got.Name != name || got.Type != want.kind || got.SpeedBps != want.speed || got.Up != want.up ||
			got.Master != want.master || got.VLANID != want.vlan || got.MAC != want.mac {
			t.Errorf("%s = %+v", name, got)
		}
	}
	if len(vlans) != 1 {
		t.Fatalf("vlans = %v", vlans)
	}
}

func TestBuildInterfaces(t *testing.T) {
	facts := []IfaceFacts{
		{Name: "lo", Kind: store.IfaceLoopback, Up: true},
		{Name: "eth0", MAC: "00:11:22:33:44:55", Kind: store.IfaceEthernet, Up: true, SpeedMbps: 1000},
		{Name: "eth1", MAC: "00:11:22:33:44:66", Kind: store.IfaceEthernet, Up: true},
	}
	index := map[int]string{1: "lo", 2: "eth0", 3: "eth1"}
	addrs := []AddrEntry{
		{Index: 1, Addr: store.IfAddress{Address: "127.0.0.1", PrefixLength: 8, Family: "ipv4", Scope: "host"}},
		{Index: 2, Addr: store.IfAddress{Address: "fe80::1", PrefixLength: 64, Family: "ipv6", Scope: "link"}},
		{Index: 2, Addr: store.IfAddress{Address: "2001:db8::99", PrefixLength: 64, Family: "ipv6", Temporary: true, DHCP: true, Scope: "global"}},
		{Index: 2, Addr: store.IfAddress{Address: "2001:db8::1", PrefixLength: 64, Family: "ipv6", Scope: "global"}},
		{Index: 2, Addr: store.IfAddress{Address: "192.0.2.10", PrefixLength: 24, Family: "ipv4", DHCP: true, Scope: "global"}},
		{Index: 3, Addr: store.IfAddress{Address: "10.1.1.1", PrefixLength: 16, Family: "ipv4", Scope: "global"}},
		{Index: 99, Addr: store.IfAddress{Address: "10.9.9.9", PrefixLength: 8, Family: "ipv4"}}, // unknown ifindex
	}
	routes := []DefaultRoute{
		{Index: 3, Gateway: "10.1.0.1", Metric: 200, Family: "ipv4"},
		{Index: 2, Gateway: "192.0.2.1", Metric: 100, Family: "ipv4"},
		{Index: 2, Gateway: "fe80::1", Metric: 1024, Family: "ipv6"},
	}
	res := BuildInterfaces(facts, index, addrs, routes)
	if len(res.Interfaces) != 3 {
		t.Fatalf("ifaces = %+v", res.Interfaces)
	}
	eth0 := res.Interfaces[1]
	if !eth0.DefaultRoute || eth0.Gateway != "192.0.2.1" || !eth0.DHCP || eth0.SpeedBps != 1e9 || len(eth0.Addresses) != 4 ||
		len(eth0.IPAddresses) != 4 || eth0.IPAddresses[3] != "192.0.2.10/24" {
		t.Fatalf("eth0 = %+v", eth0)
	}
	eth1 := res.Interfaces[2]
	if !eth1.DefaultRoute || eth1.Gateway != "10.1.0.1" || eth1.DHCP {
		t.Fatalf("eth1 = %+v", eth1)
	}
	if res.PrimaryIPv4 != "192.0.2.10" || res.PrimaryIPv6 != "2001:db8::1" {
		t.Fatalf("primary = %q %q", res.PrimaryIPv4, res.PrimaryIPv6)
	}
	if res.Truncated != (store.CollectionLimits{}) {
		t.Fatalf("truncated = %+v", res.Truncated)
	}
	// IPv6-only gateway on an interface without an IPv4 default.
	res = BuildInterfaces(facts[:2], index, addrs[:4], routes[2:])
	if res.Interfaces[1].Gateway != "fe80::1" || res.PrimaryIPv4 != "" || res.PrimaryIPv6 != "2001:db8::1" {
		t.Fatalf("v6 only = %+v %q %q", res.Interfaces[1], res.PrimaryIPv4, res.PrimaryIPv6)
	}
	// No default route → no primary.
	res = BuildInterfaces(facts, index, addrs, nil)
	if res.PrimaryIPv4 != "" || res.PrimaryIPv6 != "" {
		t.Fatalf("primary without default route: %q %q", res.PrimaryIPv4, res.PrimaryIPv6)
	}
}

func TestBuildInterfacesBounds(t *testing.T) {
	var facts []IfaceFacts
	index := map[int]string{}
	for i := 0; i < 300; i++ {
		name := fmt.Sprint("veth", i)
		facts = append(facts, IfaceFacts{Name: name, Kind: store.IfaceVirtual})
		index[i+1] = name
	}
	var addrs []AddrEntry
	for i := 0; i < 70; i++ {
		addrs = append(addrs, AddrEntry{Index: 1, Addr: store.IfAddress{Address: fmt.Sprint("10.0.0.", i+1), PrefixLength: 24, Family: "ipv4", Scope: "global"}})
	}
	res := BuildInterfaces(facts, index, addrs, nil)
	if len(res.Interfaces) != store.MaxInterfaces || len(res.Interfaces[0].Addresses) != store.MaxIfaceAddresses ||
		res.Truncated.Interfaces != 300-store.MaxInterfaces || res.Truncated.Addresses != 70-store.MaxIfaceAddresses {
		t.Fatalf("bounds: %d ifaces, %d addrs, %+v", len(res.Interfaces), len(res.Interfaces[0].Addresses), res.Truncated)
	}
}
