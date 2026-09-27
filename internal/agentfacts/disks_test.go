package agentfacts

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

var updateSysblock = flag.Bool("update-sysblock", false, "rewrite testdata/sysblock trees")

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestBlockDevicesNode1(t *testing.T) {
	ds := facts(t, "node-1")
	if len(ds) != 4 {
		t.Fatalf("physical disks = %v (want nvme0n1, sda, sdb, sdc)", ds)
	}
	want := map[string]diskFacts{
		"nvme0n1": {"nvme0n1", "SAMSUNG MZQL23T8HCLS-00A07", "", "S64HNE0T000001", store.MediaNVMeSSD, store.IfNVMe, 7501476528 * 512, false},
		"sda":     {"sda", "Samsung SSD 870", "", "S5STNF0W000002", store.MediaSSD, store.IfSATA, 7814037168 * 512, false},
		"sdb":     {"sdb", "ST4000NM000A-2HZ", "", "ZC100003", store.MediaHDD, store.IfSATA, 7814037168 * 512, false},
		"sdc":     {"sdc", "Ultra Fit", "SanDisk", "", store.MediaUnknown, store.IfUSB, 60062500 * 512, true},
	}
	for name, w := range want {
		if got := mustDisk(t, ds, name); got != w {
			t.Errorf("%s = %+v\nwant %+v", name, got, w)
		}
	}
	res := ReadBlockDevices(testCtx(t), treeSource(t, "node-1"))
	if res.Availability != store.AvailOK || res.Truncated != 0 {
		t.Fatalf("availability = %q truncated = %d", res.Availability, res.Truncated)
	}
	// Order: kernel names sorted.
	if res.Disks[0].Name != "nvme0n1" || res.Disks[3].Name != "sdc" {
		t.Fatalf("order = %v", ds)
	}
}

func TestBlockDevicesLab(t *testing.T) {
	ds := facts(t, "lab")
	names := map[string]bool{}
	for _, d := range ds {
		names[d.Name] = true
	}
	for _, excluded := range []string{"mmcblk0boot0", "fd0", "sdz", "drbd0", "rbd0"} {
		if names[excluded] {
			t.Errorf("%s must not be reported as a physical disk", excluded)
		}
	}
	if sas := mustDisk(t, ds, "sdd"); sas.Iface != store.IfSAS || sas.Media != store.MediaHDD || sas.Serial != "X0K0A00EF000" || sas.Vendor != "TOSHIBA" {
		t.Errorf("sas = %+v", sas)
	}
	if hv := mustDisk(t, ds, "sdh"); hv.Iface != store.IfHyperV || hv.Media != store.MediaUnknown || hv.Vendor != "Msft" {
		t.Errorf("hyper-v = %+v", hv)
	}
	if mmc := mustDisk(t, ds, "mmcblk0"); mmc.Iface != store.IfMMC || mmc.Media != store.MediaSSD || mmc.Serial != "0x1a2b3c4d" {
		t.Errorf("mmc = %+v", mmc)
	}
	if scsi := mustDisk(t, ds, "scsi0"); scsi.Iface != store.IfSCSI || scsi.Media != store.MediaUnknown {
		t.Errorf("scsi without rotational = %+v", scsi)
	}
}

func TestBlockDevicesVM(t *testing.T) {
	ds := facts(t, "vm")
	if vda := mustDisk(t, ds, "vda"); vda.Iface != store.IfVirtio || vda.Media != store.MediaUnknown || vda.Serial != "vm-disk-0001" {
		t.Errorf("vda = %+v", vda)
	}
	if vdb := mustDisk(t, ds, "vdb"); vdb.Iface != store.IfVirtio || vdb.Media != store.MediaSSD {
		t.Errorf("vdb (rotational 0) = %+v", vdb)
	}
	if xvda := mustDisk(t, ds, "xvda"); xvda.Iface != store.IfXen || xvda.Media != store.MediaSSD {
		t.Errorf("xvda = %+v", xvda)
	}
}

func TestInterfaceFromPath(t *testing.T) {
	cases := map[string]string{
		pci + "/0000:01:00.0/nvme/nvme0/nvme0n1":                       store.IfNVMe,
		pci + "/0000:00:14.0/usb2/2-3/2-3:1.0/host6/target6:0:0/block": store.IfUSB,
		pci + "/0000:00:04.0/virtio1/block/vda":                        store.IfVirtio,
		"/sys/devices/VMBUS:00/x/host2/target2:0:0/block/sdh":          store.IfHyperV,
		"/sys/devices/platform/storvsc/host2/block/sdh":                store.IfHyperV,
		"/sys/devices/vbd-51712/block/xvda":                            store.IfXen,
		"/sys/devices/platform/xen/vbd/block/xvdb":                     store.IfXen,
		"/sys/devices/platform/fe310000.mmc/mmc_host/mmc0/block":       store.IfMMC,
		pci + "/host0/port-0:0/end_device-0:0/target0:0:0/block/sdd":   store.IfSAS,
		pci + "/host0/sas_host0/block/sde":                             store.IfSAS,
		pci + "/0000:00:17.0/ata1/host0/target0:0:0/block/sda":         store.IfSATA,
		pci + "/host3/target3:0:0/3:0:0:0/block/sdx":                   store.IfSCSI,
		"":                                                             store.IfOther,
	}
	for path, want := range cases {
		if got := interfaceFromPath(path); got != want {
			t.Errorf("%q = %q, want %q", path, got, want)
		}
	}
}

func TestSerialFallbacks(t *testing.T) {
	base := sysTree{}.disk("sda", pci+"/ata1/host0/block/sda", "100", "0", "0", "8:0")
	cases := []struct {
		name  string
		extra sysTree
		want  string
	}{
		{"device/serial", sysTree{"sys/block/sda/device/serial": " DS1 \n", "sys/block/sda/device/vpd_pg80": vpd80("VPD1")}, "DS1"},
		{"block serial", sysTree{"sys/block/sda/serial": "BS1\n"}, "BS1"},
		{"vpd_pg80", sysTree{"sys/block/sda/device/vpd_pg80": vpd80("VPD1  ")}, "VPD1"},
		{"short vpd_pg80", sysTree{"sys/block/sda/device/vpd_pg80": "\x00\x80\x00\x20ABC", "run/udev/data/b8:0": "E:ID_SERIAL_SHORT=U1\n"}, "ABC"},
		{"not page 80", sysTree{"sys/block/sda/device/vpd_pg80": "\x00\x83\x00\x04WWID", "run/udev/data/b8:0": "E:ID_SERIAL_SHORT=U1\n"}, "U1"},
		{"udev", sysTree{"run/udev/data/b8:0": "E:ID_MODEL=x\nE:ID_SERIAL_SHORT= U1 \n"}, "U1"},
		{"none", sysTree{}, ""},
	}
	for _, c := range cases {
		tree := sysTree{}
		for k, v := range base {
			tree[k] = v
		}
		for k, v := range c.extra {
			tree[k] = v
		}
		res := ReadBlockDevices(testCtx(t), mapSource(tree))
		if len(res.Disks) != 1 || res.Disks[0].Serial != c.want {
			t.Errorf("%s: serial = %+v, want %q", c.name, res.Disks, c.want)
		}
	}
}

func mapSource(t sysTree) TreeSource {
	m := fstest.MapFS{}
	for k, v := range t {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return TreeSource{FS: m}
}

func TestAttributeReadsCapped(t *testing.T) {
	tree := sysTree{}.disk("sda", pci+"/ata1/host0/block/sda", "100", "0", "0", "8:0").
		set("sys/block/sda/device/model", strings.Repeat("M", 10000))
	res := ReadBlockDevices(testCtx(t), mapSource(tree))
	if len(res.Disks) != 1 || len(res.Disks[0].Model) > store.MaxHWString {
		t.Fatalf("model length = %d", len(res.Disks[0].Model))
	}
	if b, err := readCapped(mapSource(tree).FS, "sys/block/sda/device/model"); err != nil || len(b) != maxAttrBytes {
		t.Fatalf("read = %d %v", len(b), err)
	}
}

func TestBlockDevicesBounded(t *testing.T) {
	tree := sysTree{}
	for i := 0; i < store.MaxDisks+1; i++ {
		tree.disk(fmt.Sprintf("sd%04d", i), pci+"/ata1/host0/block/x", "100", "1", "0", "8:0")
	}
	res := ReadBlockDevices(testCtx(t), mapSource(tree))
	if len(res.Disks) != store.MaxDisks || res.Truncated != 1 {
		t.Fatalf("disks = %d truncated = %d", len(res.Disks), res.Truncated)
	}
}

// errFS fails every read below sys/block/<dev>.
type errFS struct{ fs.FS }

func (e errFS) Open(name string) (fs.File, error) {
	if strings.Count(name, "/") > 2 {
		return nil, errors.New("io error")
	}
	return e.FS.Open(name)
}

func TestBlockDevicesErrorsArePartial(t *testing.T) {
	tree := sysTree{}.disk("sda", pci+"/ata1/host0/block/sda", "100", "0", "0", "8:0")
	res := ReadBlockDevices(testCtx(t), TreeSource{FS: errFS{mapSource(tree).FS}})
	if len(res.Disks) != 0 || res.Availability != store.AvailPartial {
		t.Fatalf("unreadable attributes = %+v", res)
	}
	res = ReadBlockDevices(testCtx(t), TreeSource{FS: fstest.MapFS{}})
	if len(res.Disks) != 0 || res.Availability != store.AvailUnavailable {
		t.Fatalf("no sys/block = %+v", res)
	}
	// A cancelled context stops early and reports partial.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := ReadBlockDevices(ctx, mapSource(tree)); res.Availability != store.AvailPartial || len(res.Disks) != 0 {
		t.Fatalf("cancelled = %+v", res)
	}
	// DevPath via the resolver function.
	src := mapSource(tree)
	src.DevPath = func(string) (string, error) { return "", errors.New("no symlink") }
	if res := ReadBlockDevices(testCtx(t), src); len(res.Disks) != 1 || res.Disks[0].Interface != store.IfOther {
		t.Fatalf("unresolvable device path = %+v", res.Disks)
	}
}

func TestDiskSmallEdges(t *testing.T) {
	tree := sysTree{}.disk("sda", "ignored", "100", "1", "0", "8:0").set("sys/block/sda/device/model", "Model\x01X\xff")
	src := mapSource(tree)
	src.DevPath = func(dev string) (string, error) { return pci + "/ata1/host0/block/" + dev, nil }
	res := ReadBlockDevices(testCtx(t), src)
	if len(res.Disks) != 1 || res.Disks[0].Interface != store.IfSATA || res.Disks[0].Model != "ModelX" || res.Disks[0].MediaType != store.MediaHDD {
		t.Fatalf("resolver path / cleaned model = %+v", res.Disks)
	}
	if _, err := ParseWindowsDriveLetters([]byte(`[{"DiskNumber":-1,"DriveLetter":"C"}]`)); err == nil {
		t.Fatal("negative disk number accepted")
	}
	if _, err := ParseWindowsPhysicalDisks([]byte(`[{"Size":{}}]`)); err == nil {
		t.Fatal("object size accepted")
	}
}
