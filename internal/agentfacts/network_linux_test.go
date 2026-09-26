//go:build linux

package agentfacts

import (
	"syscall"
	"testing"
)

// The portable decoders read this machine's real netlink dumps: loopback
// 127.0.0.1/8 with host scope is always present.
func TestParseLiveNetlink(t *testing.T) {
	raw, err := syscall.NetlinkRIB(syscall.RTM_GETADDR, syscall.AF_UNSPEC)
	if err != nil {
		t.Skipf("netlink unavailable: %v", err)
	}
	addrs, err := ParseAddrMessages(raw)
	if err != nil {
		t.Fatalf("live addr dump: %v", err)
	}
	found := false
	for _, a := range addrs {
		if a.Addr.Address == "127.0.0.1" && a.Addr.PrefixLength == 8 && a.Addr.Scope == "host" && !a.Addr.DHCP {
			found = true
		}
	}
	if !found {
		t.Fatalf("loopback not decoded: %+v", addrs)
	}
	rraw, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_UNSPEC)
	if err != nil {
		t.Skipf("netlink routes unavailable: %v", err)
	}
	if _, err := ParseRouteMessages(rraw); err != nil {
		t.Fatalf("live route dump: %v", err)
	}
}
