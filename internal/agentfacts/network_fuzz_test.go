package agentfacts

import (
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// FuzzNetlinkAddr feeds arbitrary bytes to the netlink address and route
// decoders: they must never panic, and whatever they return, the built
// interface list stays within its bounds.
func FuzzNetlinkAddr(f *testing.F) {
	f.Add(concat(addrMsg("192.0.2.10", 24, 0, scopeUniverse, 2, true), routeNL(afInet, 0, "192.0.2.1", 2, 100)))
	f.Add(addrMsg("2001:db8::1", 64, ifaFTemporary, scopeUniverse, 1, false))
	f.Add(nlMsg(rtmNewRoute, routeMsg(afInet6, 0, "fe80::1", 3, 5, rtTableMain, rtnUnicast)))
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 20, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		addrs, _ := ParseAddrMessages(data)
		routes, _ := ParseRouteMessages(data)
		index := map[int]string{}
		var facts []IfaceFacts
		for _, a := range addrs {
			if _, ok := index[a.Index]; !ok && len(index) < 300 {
				name := "if" + string(rune('a'+len(index)%26)) + string(rune('0'+len(index)/26))
				index[a.Index] = name
				facts = append(facts, IfaceFacts{Name: name})
			}
			if a.Addr.PrefixLength > 128 || (a.Addr.Family != "ipv4" && a.Addr.Family != "ipv6") {
				t.Fatalf("bad entry %+v", a)
			}
		}
		for _, r := range routes {
			if r.Index == 0 || (r.Family != "ipv4" && r.Family != "ipv6") {
				t.Fatalf("bad route %+v", r)
			}
		}
		res := BuildInterfaces(facts, index, addrs, routes)
		if len(res.Interfaces) > store.MaxInterfaces {
			t.Fatalf("interfaces = %d", len(res.Interfaces))
		}
		for _, n := range res.Interfaces {
			if len(n.Addresses) > store.MaxIfaceAddresses {
				t.Fatalf("addresses = %d", len(n.Addresses))
			}
		}
	})
}
