//go:build linux

package collector

import (
	"context"
	"net"
	"os"
	"sort"
	"syscall"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
)

const sysClassNet = "/sys/class/net"

// collectNetwork reads interfaces from sysfs and addresses/default routes
// from rtnetlink (stdlib), within networkBudget. ok is false when nothing
// could be read, so the caller falls back to the portable collector.
func collectNetwork(ctx context.Context) (agentfacts.Network, bool) {
	ctx, cancel := context.WithTimeout(ctx, networkBudget)
	defer cancel()
	ch := make(chan agentfacts.Network, 1)
	go func() { ch <- linuxNetwork() }()
	select {
	case n := <-ch:
		return n, len(n.Interfaces) > 0
	case <-ctx.Done():
		return agentfacts.Network{}, false
	}
}

func linuxNetwork() agentfacts.Network {
	entries, err := os.ReadDir(sysClassNet)
	if err != nil {
		return agentfacts.Network{}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var vlans map[string]uint32
	if b, err := os.ReadFile("/proc/net/vlan/config"); err == nil {
		vlans = agentfacts.ParseVLANConfig(string(b))
	}
	fsys := os.DirFS(sysClassNet)
	facts := make([]agentfacts.IfaceFacts, 0, len(names))
	for _, n := range names {
		facts = append(facts, agentfacts.ReadSysfsIface(fsys, n, vlans))
	}
	index := map[int]string{}
	if ifs, err := net.Interfaces(); err == nil {
		for _, i := range ifs {
			index[i.Index] = i.Name
		}
	}
	var addrs []agentfacts.AddrEntry
	if raw, err := syscall.NetlinkRIB(syscall.RTM_GETADDR, syscall.AF_UNSPEC); err == nil {
		addrs, _ = agentfacts.ParseAddrMessages(raw)
	}
	var routes []agentfacts.DefaultRoute
	if raw, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_UNSPEC); err == nil {
		routes, _ = agentfacts.ParseRouteMessages(raw)
	}
	return agentfacts.BuildInterfaces(facts, index, addrs, routes)
}
