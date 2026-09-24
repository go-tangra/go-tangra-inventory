package collector

import (
	"context"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	gnet "github.com/shirou/gopsutil/v4/net"
)

// collectOS fills OS and environment fields from gopsutil host info. On error it
// leaves the fields empty.
func collectOS(ctx context.Context, inv *store.Inventory) {
	info, err := host.InfoWithContext(ctx)
	if err != nil || info == nil {
		inv.OS.Arch = hostArch("")
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

// collectNetworks maps every interface reported by gopsutil into a NetIface.
func collectNetworks(inv *store.Inventory) {
	ifaces, err := gnet.Interfaces()
	if err != nil {
		return
	}
	for _, ifc := range ifaces {
		ni := store.NetIface{
			Name: ifc.Name,
			MAC:  ifc.HardwareAddr,
			Up:   hasFlag(ifc.Flags, "up"),
		}
		for _, a := range ifc.Addrs {
			if a.Addr != "" {
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

// collectDisks maps mounted partitions (and their usage) into disks. Partitions
// are grouped under their backing device so each store.Disk carries its mounts.
func collectDisks(ctx context.Context, inv *store.Inventory) {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return
	}
	byDev := make(map[string]*store.Disk)
	var order []string
	for _, p := range parts {
		d, ok := byDev[p.Device]
		if !ok {
			d = &store.Disk{}
			byDev[p.Device] = d
			order = append(order, p.Device)
		}
		part := store.Partition{Mount: p.Mountpoint, FS: p.Fstype}
		if u, uerr := disk.UsageWithContext(ctx, p.Mountpoint); uerr == nil && u != nil {
			part.SizeBytes = u.Total
			part.FreeBytes = u.Free
		}
		d.Partitions = append(d.Partitions, part)
	}
	for _, dev := range order {
		inv.Disks = append(inv.Disks, *byDev[dev])
	}
}
