package agentfacts

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func TestResolveFilesystemDisks(t *testing.T) {
	src := treeSource(t, "node-1")
	r := NewDiskResolver(src, []string{"nvme0n1", "sda", "sdb", "sdc"})
	cases := map[string][]string{
		"/dev/nvme0n1p2":       {"nvme0n1"},    // partition -> parent disk
		"/dev/nvme0n1":         {"nvme0n1"},    // whole disk
		"/dev/mapper/vg0-root": {"nvme0n1"},    // LVM dm-0 -> slaves/nvme0n1p3 -> nvme0n1
		"/dev/dm-0":            {"nvme0n1"},    // same by kernel name
		"/dev/md0":             {"sda", "sdb"}, // RAID1 over two disks
		"/dev/mapper/vg1-data": {"sda", "sdb"}, // LVM on md
		"/dev/sdc1":            {"sdc"},        // USB stick partition
		"/dev/loop0":           nil,            // not a physical disk
		"/dev/mapper/unknown":  nil,            // unknown dm name
		"tmpfs":                nil,            // not a device
		"C:":                   nil,            // not a Linux device
	}
	for dev, want := range cases {
		got, dropped := r.Disks(dev)
		if !reflect.DeepEqual(got, want) || dropped != 0 {
			t.Errorf("%s -> %v (dropped %d), want %v", dev, got, dropped, want)
		}
	}
}

func TestResolveDepthAndCycles(t *testing.T) {
	tree := sysTree{}
	// dm-0 -> dm-1 -> ... -> dm-9 -> sda (depth 10 > 8)
	for i := 0; i < 10; i++ {
		tree.disk(fmt.Sprintf("dm-%d", i), "/sys/devices/virtual/block/x", "100", "0", "0", "253:0").
			set(fmt.Sprintf("sys/block/dm-%d/slaves/dm-%d", i, i+1), "")
	}
	tree.set("sys/block/dm-9/slaves/sda", "")
	tree.disk("sda", pci+"/ata1/block/sda", "100", "0", "0", "8:0")
	// md0 <-> md1 cycle.
	tree.disk("md0", "/sys/devices/virtual/block/md0", "100", "0", "0", "9:0").set("sys/block/md0/slaves/md1", "")
	tree.disk("md1", "/sys/devices/virtual/block/md1", "100", "0", "0", "9:1").set("sys/block/md1/slaves/md0", "").set("sys/block/md1/slaves/sda", "")
	r := NewDiskResolver(mapSource(tree), []string{"sda"})
	if got, _ := r.Disks("/dev/dm-0"); got != nil {
		t.Errorf("depth > 8 must stop: %v", got)
	}
	if got, _ := r.Disks("/dev/dm-2"); !reflect.DeepEqual(got, []string{"sda"}) {
		t.Errorf("depth 8 = %v", got)
	}
	if got, _ := r.Disks("/dev/md0"); !reflect.DeepEqual(got, []string{"sda"}) {
		t.Errorf("cycle = %v", got)
	}
}

func TestResolveBounded(t *testing.T) {
	tree := sysTree{}
	tree.disk("md0", "/sys/devices/virtual/block/md0", "100", "0", "0", "9:0")
	var names []string
	for i := 0; i < store.MaxFSDisks+1; i++ {
		n := fmt.Sprintf("sd%03d", i)
		names = append(names, n)
		tree.disk(n, pci+"/ata1/block/"+n, "100", "1", "0", "8:0").set("sys/block/md0/slaves/"+n, "")
	}
	got, dropped := NewDiskResolver(mapSource(tree), names).Disks("/dev/md0")
	if len(got) != store.MaxFSDisks || dropped != 1 {
		t.Fatalf("disks = %d dropped = %d", len(got), dropped)
	}
}
