package agentfacts

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// FuzzWindowsDisks parses arbitrary PowerShell output: never a panic, every
// value within the closed sets and string bounds.
func FuzzWindowsDisks(f *testing.F) {
	f.Add([]byte(`[{"DeviceId":"0","FriendlyName":"X","SerialNumber":"S","Size":1,"MediaType":4,"BusType":17}]`), []byte(`[{"DiskNumber":0,"DriveLetter":"C"}]`))
	f.Add([]byte(`{"Index":0,"Model":"M","Size":"12","InterfaceType":"USB"}`), []byte(`{"DiskNumber":1,"DriveLetter":67}`))
	f.Add([]byte(`null`), []byte(`[]`))
	f.Fuzz(func(t *testing.T, disksJSON, lettersJSON []byte) {
		for _, parse := range []func([]byte) ([]store.Disk, error){ParseWindowsPhysicalDisks, ParseWin32DiskDrives} {
			disks, err := parse(disksJSON)
			if err != nil {
				continue
			}
			for _, d := range disks {
				assertDisk(t, d)
			}
		}
		m, err := ParseWindowsDriveLetters(lettersJSON)
		if err == nil {
			for k, v := range m {
				if len(k) != 2 || k[1] != ':' || !utf8.ValidString(v) {
					t.Fatalf("letter map %q -> %q", k, v)
				}
			}
		}
	})
}

// FuzzSysBlock reads a tree whose attribute files hold arbitrary content.
func FuzzSysBlock(f *testing.F) {
	f.Add("sda", "/sys/devices/pci0000:00/ata1/block/sda", "7814037168", "0", "Samsung SSD", string([]byte{0, 0x80, 0, 4})+"S123", "E:ID_SERIAL_SHORT=U1\n", "8:0")
	f.Add("nvme0n1", "/sys/devices/nvme/nvme0/nvme0n1", "x", "", "", "", "", "")
	f.Fuzz(func(t *testing.T, name, devpath, size, rot, model, vpd, udev, dev string) {
		if name == "" || len(name) > 64 {
			return
		}
		m := fstest.MapFS{}
		base := "sys/block/" + name + "/"
		if !fsValid(base) {
			return
		}
		for k, v := range map[string]string{"devpath": devpath, "size": size, "queue/rotational": rot, "device/model": model,
			"device/vpd_pg80": vpd, "dev": dev, "removable": rot, "slaves/" + name: ""} {
			if fsValid(base + k) {
				m[base+k] = &fstest.MapFile{Data: []byte(v)}
			}
		}
		m["run/udev/data/b"+dev] = &fstest.MapFile{Data: []byte(udev)}
		src := TreeSource{FS: m}
		res := ReadBlockDevices(context.Background(), src)
		if len(res.Disks) > store.MaxDisks {
			t.Fatalf("disks = %d", len(res.Disks))
		}
		for _, d := range res.Disks {
			assertDisk(t, d)
		}
		got, _ := NewDiskResolver(src, []string{name}).Disks("/dev/" + name)
		if len(got) > store.MaxFSDisks {
			t.Fatalf("resolved = %d", len(got))
		}
	})
}

func fsValid(p string) bool { return fs.ValidPath(p) }

func assertDisk(t *testing.T, d store.Disk) {
	t.Helper()
	switch d.MediaType {
	case store.MediaSSD, store.MediaHDD, store.MediaNVMeSSD, store.MediaUnknown:
	default:
		t.Fatalf("media %q", d.MediaType)
	}
	switch d.Interface {
	case store.IfNVMe, store.IfSATA, store.IfSAS, store.IfSCSI, store.IfUSB, store.IfVirtio, store.IfHyperV, store.IfXen, store.IfMMC, store.IfOther:
	default:
		t.Fatalf("interface %q", d.Interface)
	}
	for _, s := range []string{d.Name, d.Model, d.Serial, d.Vendor} {
		if len(s) > store.MaxHWString || !utf8.ValidString(s) {
			t.Fatalf("string %q", s)
		}
	}
}
