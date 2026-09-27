package collector

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// diskBudget bounds the physical disk enumeration (sysfs reads or the
// PowerShell queries run inside their own timeouts).
const diskBudget = 5 * time.Second

// fsMount is a mounted filesystem with its usage.
type fsMount struct {
	Device, Mount, FS string
	Size, Free        uint64
}

// collectDisks reports the physical disks (feature 023: model, serial,
// size, media, interface, removable) and the mounted filesystems with the
// disks they live on. With collect_disks disabled only filesystems are
// reported and the disk availability is "unsupported".
func collectDisks(ctx context.Context, inv *store.Inventory, opts Options) {
	mounts := listMounts(ctx)
	res := agentfacts.BlockDevices{Availability: store.AvailUnsupported}
	var resolver agentfacts.DiskResolver
	if opts.CollectDisks {
		res, resolver = physicalDisks(ctx)
	}
	applyDisks(inv, res, resolver, mounts)
}

// listMounts lists the mounted filesystems with their usage (gopsutil).
func listMounts(ctx context.Context) []fsMount {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil
	}
	out := make([]fsMount, 0, len(parts))
	for _, p := range parts {
		m := fsMount{Device: p.Device, Mount: p.Mountpoint, FS: p.Fstype}
		if u, uerr := disk.UsageWithContext(ctx, p.Mountpoint); uerr == nil && u != nil {
			m.Size, m.Free = u.Total, u.Free
		}
		out = append(out, m)
	}
	return out
}

// applyDisks puts the disks, filesystems (bounded) and availability on the
// inventory. It is pure given its inputs.
func applyDisks(inv *store.Inventory, res agentfacts.BlockDevices, resolver agentfacts.DiskResolver, mounts []fsMount) {
	inv.Disks = res.Disks
	inv.Truncated.Disks += res.Truncated
	inv.Availability.Disks = res.Availability
	for _, m := range mounts {
		if len(inv.Filesystems) == store.MaxFilesystems {
			inv.Truncated.Filesystems++
			continue
		}
		f := store.Filesystem{Mount: m.Mount, FS: m.FS, Device: m.Device, SizeBytes: m.Size, FreeBytes: m.Free}
		if resolver != nil {
			var dropped uint32
			f.Disks, dropped = resolver.Disks(m.Device)
			inv.Truncated.Disks += dropped
		}
		inv.Filesystems = append(inv.Filesystems, f)
	}
}
