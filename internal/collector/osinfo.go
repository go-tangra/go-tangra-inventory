package collector

import (
	"context"
	"runtime"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/shirou/gopsutil/v4/host"
	gnet "github.com/shirou/gopsutil/v4/net"
)

// collectOS fills OS and environment fields from gopsutil host info. On error it
// leaves the fields empty.
func collectOS(ctx context.Context, inv *store.Inventory) {
	info, err := host.InfoWithContext(ctx)
	if err != nil || info == nil {
		inv.OS.Arch = hostArch("")
		inv.OS.Family = osFamily()
		return
	}
	name := info.Platform
	if name == "" {
		name = info.OS
	}
	inv.OS = store.OSInfo{
		Name:      name,
		Version:   info.PlatformVersion,
		Kernel:    info.KernelVersion,
		Arch:      hostArch(info.KernelArch),
		UptimeSec: info.Uptime,
		Family:    osFamily(),
	}
	if info.BootTime > 0 {
		inv.OS.LastBoot = time.Unix(int64(info.BootTime), 0).UTC()
	}
	if inv.Identity.Hostname == "" {
		inv.Identity.Hostname = info.Hostname
	}
	if zone, _ := time.Now().Zone(); zone != "" {
		inv.Environment.Timezone = zone
	}
}

// networkBudget bounds the native network collection.
const networkBudget = 5 * time.Second

// collectNetworks fills interfaces (kind, speed, addresses with prefix and
// flags, gateway, default route), the primary addresses and truncation
// counters from the native collector, falling back to gopsutil (names, MACs,
// CIDR strings only) where it is unavailable.
func collectNetworks(ctx context.Context, inv *store.Inventory) {
	if n, ok := collectNetwork(ctx); ok {
		inv.Networks = n.Interfaces
		inv.PrimaryIPv4, inv.PrimaryIPv6 = n.PrimaryIPv4, n.PrimaryIPv6
		inv.Truncated.Interfaces += n.Truncated.Interfaces
		inv.Truncated.Addresses += n.Truncated.Addresses
		return
	}
	ifaces, err := gnet.InterfacesWithContext(ctx)
	if err != nil {
		return
	}
	for _, ifc := range ifaces {
		if len(inv.Networks) == store.MaxInterfaces {
			inv.Truncated.Interfaces++
			continue
		}
		ni := store.NetIface{
			Name: ifc.Name,
			MAC:  ifc.HardwareAddr,
			Up:   hasFlag(ifc.Flags, "up"),
		}
		for _, a := range ifc.Addrs {
			if a.Addr != "" && len(ni.IPAddresses) < store.MaxIfaceAddresses {
				ni.IPAddresses = append(ni.IPAddresses, a.Addr)
			}
		}
		inv.Networks = append(inv.Networks, ni)
	}
}

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if strings.EqualFold(f, want) {
			return true
		}
	}
	return false
}

// osFamily is the agent's OS family as reported in OSInfo.family.
func osFamily() string {
	switch runtime.GOOS {
	case "linux", "windows":
		return runtime.GOOS
	}
	return ""
}
