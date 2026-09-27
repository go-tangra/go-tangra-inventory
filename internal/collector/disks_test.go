package collector

import (
	"reflect"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

type fakeResolver map[string][]string

func (f fakeResolver) Disks(dev string) ([]string, uint32) {
	if dev == "/dev/huge" {
		return f[dev], 3
	}
	return f[dev], 0
}

func TestApplyDisks(t *testing.T) {
	mounts := []fsMount{
		{Device: "/dev/nvme0n1p2", Mount: "/", FS: "ext4", Size: 100, Free: 40},
		{Device: "/dev/md0", Mount: "/srv", FS: "xfs", Size: 200, Free: 10},
		{Device: "tmpfs", Mount: "/run", FS: "tmpfs", Size: 5, Free: 5},
		{Device: "/dev/huge", Mount: "/big", FS: "xfs"},
	}
	res := agentfacts.BlockDevices{Disks: []store.Disk{{Name: "nvme0n1"}, {Name: "sda"}, {Name: "sdb"}}, Truncated: 2, Availability: store.AvailOK}
	var inv store.Inventory
	applyDisks(&inv, res, fakeResolver{"/dev/nvme0n1p2": {"nvme0n1"}, "/dev/md0": {"sda", "sdb"}, "/dev/huge": {"sda"}}, mounts)
	if len(inv.Disks) != 3 || inv.Availability.Disks != store.AvailOK || inv.Truncated.Disks != 2+3 {
		t.Fatalf("disks = %+v truncated = %+v", inv.Disks, inv.Truncated)
	}
	want := []store.Filesystem{
		{Mount: "/", FS: "ext4", Device: "/dev/nvme0n1p2", SizeBytes: 100, FreeBytes: 40, Disks: []string{"nvme0n1"}},
		{Mount: "/srv", FS: "xfs", Device: "/dev/md0", SizeBytes: 200, FreeBytes: 10, Disks: []string{"sda", "sdb"}},
		{Mount: "/run", FS: "tmpfs", Device: "tmpfs", SizeBytes: 5, FreeBytes: 5},
		{Mount: "/big", FS: "xfs", Device: "/dev/huge", Disks: []string{"sda"}},
	}
	if !reflect.DeepEqual(inv.Filesystems, want) {
		t.Fatalf("filesystems =\n%+v\nwant\n%+v", inv.Filesystems, want)
	}
	for _, d := range inv.Disks {
		if d.Partitions != nil {
			t.Fatal("new agents never report legacy partition groups")
		}
	}
}

func TestApplyDisksDisabledOrFailing(t *testing.T) {
	mounts := []fsMount{{Device: "/dev/sda1", Mount: "/", FS: "ext4", Size: 1, Free: 1}}
	// collect_disks: false -> no disks, availability unsupported, filesystems kept.
	var inv store.Inventory
	applyDisks(&inv, agentfacts.BlockDevices{Availability: store.AvailUnsupported}, nil, mounts)
	if inv.Disks != nil || inv.Availability.Disks != store.AvailUnsupported || len(inv.Filesystems) != 1 || inv.Filesystems[0].Disks != nil {
		t.Fatalf("disabled = %+v", inv)
	}
	// Read errors -> partial, filesystems still reported.
	inv = store.Inventory{}
	applyDisks(&inv, agentfacts.BlockDevices{Availability: store.AvailPartial}, fakeResolver{}, mounts)
	if inv.Availability.Disks != store.AvailPartial || len(inv.Filesystems) != 1 {
		t.Fatalf("partial = %+v", inv)
	}
}

func TestApplyDisksBounded(t *testing.T) {
	var mounts []fsMount
	for i := 0; i < store.MaxFilesystems+5; i++ {
		mounts = append(mounts, fsMount{Mount: "/m", FS: "tmpfs"})
	}
	var inv store.Inventory
	applyDisks(&inv, agentfacts.BlockDevices{Availability: store.AvailOK}, nil, mounts)
	if len(inv.Filesystems) != store.MaxFilesystems || inv.Truncated.Filesystems != 5 {
		t.Fatalf("filesystems = %d truncated = %d", len(inv.Filesystems), inv.Truncated.Filesystems)
	}
}

func TestOptionsCollectDisks(t *testing.T) {
	if !DefaultOptions().CollectDisks {
		t.Fatal("disks are collected by default")
	}
}
