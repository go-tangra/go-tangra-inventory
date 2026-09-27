package agentfacts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Synthetic sysfs block trees (feature 023, T005/T006) in the layout
// scripts/capture-sysblock.sh writes: sys/block/<dev>/{size,removable,dev,
// devpath,queue/rotational,device/{model,vendor,serial,vpd_pg80},serial,
// dm/name,slaves/*,<partition>/partition} and run/udev/data/b<maj:min>.
// "devpath" holds the resolved /sys/devices path the kernel symlink points
// to. node-1 reproduces a node-1 class server (NVMe + SATA SSD + SATA HDD,
// LVM on NVMe, md RAID1 on the SATA disks, a USB stick and the usual
// virtual block devices); serials are synthetic.

type sysTree map[string]string

func (t sysTree) disk(name, devpath, size, rotational, removable, dev string) sysTree {
	b := "sys/block/" + name + "/"
	t[b+"devpath"] = devpath
	t[b+"size"] = size + "\n"
	if rotational != "" {
		t[b+"queue/rotational"] = rotational + "\n"
	}
	t[b+"removable"] = removable + "\n"
	t[b+"dev"] = dev + "\n"
	return t
}

func (t sysTree) set(path, content string) sysTree {
	t[path] = content
	return t
}

func (t sysTree) part(disk, part, dev string) sysTree {
	t["sys/block/"+disk+"/"+part+"/partition"] = "1\n"
	t["sys/block/"+disk+"/"+part+"/dev"] = dev + "\n"
	return t
}

func vpd80(serial string) string {
	return string([]byte{0x00, 0x80, 0x00, byte(len(serial))}) + serial
}

const pci = "/sys/devices/pci0000:00"

var sysTrees = map[string]func() sysTree{
	"node-1": func() sysTree {
		t := sysTree{}
		t.disk("nvme0n1", pci+"/0000:00:01.1/0000:01:00.0/nvme/nvme0/nvme0n1", "7501476528", "0", "0", "259:0").
			set("sys/block/nvme0n1/device/model", "SAMSUNG MZQL23T8HCLS-00A07              \n").
			set("sys/block/nvme0n1/device/serial", "S64HNE0T000001      \n").
			part("nvme0n1", "nvme0n1p1", "259:1").part("nvme0n1", "nvme0n1p2", "259:2").part("nvme0n1", "nvme0n1p3", "259:3")
		t.disk("sda", pci+"/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda", "7814037168", "0", "0", "8:0").
			set("sys/block/sda/device/model", "Samsung SSD 870 \n").set("sys/block/sda/device/vendor", "ATA     \n").
			set("sys/block/sda/device/vpd_pg80", vpd80("S5STNF0W000002 ")).part("sda", "sda1", "8:1")
		t.disk("sdb", pci+"/0000:00:17.0/ata2/host1/target1:0:0/1:0:0:0/block/sdb", "7814037168", "1", "0", "8:16").
			set("sys/block/sdb/device/model", "ST4000NM000A-2HZ\n").set("sys/block/sdb/device/vendor", "ATA     \n").
			set("run/udev/data/b8:16", "S:disk/by-id/ata-ST4000NM000A\nE:ID_SERIAL=ST4000NM000A-2HZ100_ZC100003\nE:ID_SERIAL_SHORT=ZC100003\n").
			part("sdb", "sdb1", "8:17")
		t.disk("sdc", pci+"/0000:00:14.0/usb2/2-3/2-3:1.0/host6/target6:0:0/6:0:0:0/block/sdc", "60062500", "1", "1", "8:32").
			set("sys/block/sdc/device/model", "Ultra Fit       \n").set("sys/block/sdc/device/vendor", "SanDisk \n").part("sdc", "sdc1", "8:33")
		t.disk("dm-0", "/sys/devices/virtual/block/dm-0", "4194304000", "0", "0", "253:0").
			set("sys/block/dm-0/dm/name", "vg0-root\n").set("sys/block/dm-0/slaves/nvme0n1p3", "")
		t.disk("dm-1", "/sys/devices/virtual/block/dm-1", "2097152000", "0", "0", "253:1").
			set("sys/block/dm-1/dm/name", "vg1-data\n").set("sys/block/dm-1/slaves/md0", "")
		t.disk("md0", "/sys/devices/virtual/block/md0", "7813774336", "1", "0", "9:0").
			set("sys/block/md0/slaves/sda1", "").set("sys/block/md0/slaves/sdb1", "")
		t.disk("loop0", "/sys/devices/virtual/block/loop0", "131072", "1", "0", "7:0")
		t.disk("zram0", "/sys/devices/virtual/block/zram0", "8388608", "0", "0", "252:0")
		t.disk("ram0", "/sys/devices/virtual/block/ram0", "131072", "1", "0", "1:0")
		t.disk("sr0", pci+"/0000:00:17.0/ata3/host2/target2:0:0/2:0:0:0/block/sr0", "2097151", "1", "1", "11:0")
		t.disk("nbd0", "/sys/devices/virtual/block/nbd0", "0", "0", "0", "43:0")
		return t
	},
	"lab": func() sysTree {
		t := sysTree{}
		t.disk("sdd", pci+"/0000:00:03.0/0000:02:00.0/host0/port-0:0/end_device-0:0/target0:0:0/0:0:0:0/block/sdd", "35156656128", "1", "0", "8:48").
			set("sys/block/sdd/device/model", "MG08SCA18TE     \n").set("sys/block/sdd/device/vendor", "TOSHIBA \n").
			set("sys/block/sdd/device/vpd_pg80", vpd80("X0K0A00EF000"))
		t.disk("sdh", "/sys/devices/LNXSYSTM:00/LNXSYBUS:00/ACPI0004:00/VMBUS:00/f8b3781b-1e82-4818-a1c3-63d806ec15bb/host2/target2:0:0/2:0:0:0/block/sdh", "268435456", "1", "0", "8:112").
			set("sys/block/sdh/device/model", "Virtual Disk    \n").set("sys/block/sdh/device/vendor", "Msft    \n")
		t.disk("mmcblk0", "/sys/devices/platform/fe310000.mmc/mmc_host/mmc0/mmc0:0001/block/mmcblk0", "61071360", "0", "0", "179:0").
			set("sys/block/mmcblk0/device/serial", "0x1a2b3c4d\n")
		t.disk("mmcblk0boot0", "/sys/devices/platform/fe310000.mmc/mmc_host/mmc0/mmc0:0001/block/mmcblk0/mmcblk0boot0", "8192", "0", "0", "179:8")
		t.disk("fd0", "/sys/devices/platform/floppy.0/block/fd0", "8", "1", "1", "2:0")
		t.disk("sdz", pci+"/0000:00:17.0/ata6/host5/target5:0:0/5:0:0:0/block/sdz", "0", "1", "1", "8:400")
		t.disk("drbd0", "/sys/devices/virtual/block/drbd0", "1048576", "1", "0", "147:0")
		t.disk("rbd0", "/sys/devices/rbd/0/block/rbd0", "1048576", "1", "0", "251:0")
		t.disk("scsi0", pci+"/0000:00:10.0/host3/target3:0:0/3:0:0:0/block/scsi0", "2048", "", "0", "8:500")
		return t
	},
	"vm": func() sysTree {
		t := sysTree{}
		t.disk("vda", pci+"/0000:00:04.0/virtio1/block/vda", "104857600", "1", "0", "252:0").
			set("sys/block/vda/serial", "vm-disk-0001\n").part("vda", "vda1", "252:1")
		t.disk("vdb", pci+"/0000:00:05.0/virtio2/block/vdb", "209715200", "0", "0", "252:16")
		t.disk("xvda", "/sys/devices/vbd-51712/block/xvda", "41943040", "0", "0", "202:0")
		return t
	},
}

// TestWriteSysTrees writes the synthetic trees (run with -update-sysblock).
func TestWriteSysTrees(t *testing.T) {
	if !*updateSysblock {
		t.Skip("run with -update-sysblock to rewrite testdata/sysblock")
	}
	for name, build := range sysTrees {
		root := filepath.Join("testdata", "sysblock", name)
		for path, content := range build() {
			p := filepath.Join(root, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil { // #nosec G306 -- test fixture
				t.Fatal(err)
			}
		}
	}
}

// TestSysTreesMatchBuilder keeps the committed trees and the builder in step.
func TestSysTreesMatchBuilder(t *testing.T) {
	for name, build := range sysTrees {
		root := filepath.Join("testdata", "sysblock", name)
		want := build()
		got := map[string]string{}
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			b, rerr := os.ReadFile(p) // #nosec G304 -- test fixture
			rel, _ := filepath.Rel(root, p)
			got[filepath.ToSlash(rel)] = string(b)
			return rerr
		})
		if err != nil {
			t.Fatal(err)
		}
		var diffs []string
		for k, v := range want {
			if got[k] != v {
				diffs = append(diffs, k)
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				diffs = append(diffs, "extra "+k)
			}
		}
		sort.Strings(diffs)
		if len(diffs) > 0 {
			t.Errorf("%s differs from the builder (run with -update-sysblock): %s", name, strings.Join(diffs, ", "))
		}
	}
}

func treeSource(t *testing.T, name string) TreeSource {
	t.Helper()
	root := filepath.Join("testdata", "sysblock", name)
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("tree %s: %v", name, err)
	}
	return TreeSource{FS: os.DirFS(root)}
}

func mustDisk(t *testing.T, ds []diskFacts, name string) diskFacts {
	t.Helper()
	for _, d := range ds {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("disk %s not reported (have %v)", name, ds)
	return diskFacts{}
}

type diskFacts = struct {
	Name, Model, Vendor, Serial, Media, Iface string
	Size                                      uint64
	Removable                                 bool
}

func facts(t *testing.T, name string) []diskFacts {
	t.Helper()
	res := ReadBlockDevices(testCtx(t), treeSource(t, name))
	var out []diskFacts
	for _, d := range res.Disks {
		out = append(out, diskFacts{d.Name, d.Model, d.Vendor, d.Serial, d.MediaType, d.Interface, d.SizeBytes, d.Removable})
	}
	return out
}

func (f sysTree) String() string { return fmt.Sprintf("%d files", len(f)) }
