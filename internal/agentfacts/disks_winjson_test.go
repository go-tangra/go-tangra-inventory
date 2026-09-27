package agentfacts

import (
	"reflect"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func TestParseWindowsPhysicalDisks(t *testing.T) {
	// Get-PhysicalDisk | Select-Object ... | ConvertTo-Json -Compress: an array
	// with MediaType/BusType as numbers (Windows PowerShell 5.1).
	arr := `[{"DeviceId":"0","FriendlyName":"SAMSUNG MZVL2512HCJQ-00B00","SerialNumber":"0025_3887_1A00_0001.","Size":512110190592,"MediaType":4,"BusType":17},
	{"DeviceId":"1","FriendlyName":"ST2000DM008-2FR102","SerialNumber":"  ZFL00001","Size":2000398934016,"MediaType":3,"BusType":11},
	{"DeviceId":"2","FriendlyName":"SanDisk Cruzer","SerialNumber":null,"Size":16008609792,"MediaType":0,"BusType":7},
	{"DeviceId":"3","FriendlyName":"Msft Virtual Disk","SerialNumber":"","Size":137438953472,"MediaType":"Unspecified","BusType":"SCSI"},
	{"DeviceId":"4","FriendlyName":"SAS HDD","Size":1,"MediaType":"HDD","BusType":"SAS"},
	{"DeviceId":"5","FriendlyName":"SD","Size":1,"MediaType":"SSD","BusType":12},
	{"DeviceId":"6","FriendlyName":"Weird","Size":-5,"MediaType":99,"BusType":99}]`
	disks, err := ParseWindowsPhysicalDisks([]byte(arr))
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Disk{
		{Name: "PhysicalDrive0", Model: "SAMSUNG MZVL2512HCJQ-00B00", Serial: "0025_3887_1A00_0001.", SizeBytes: 512110190592, MediaType: store.MediaNVMeSSD, Interface: store.IfNVMe},
		{Name: "PhysicalDrive1", Model: "ST2000DM008-2FR102", Serial: "ZFL00001", SizeBytes: 2000398934016, MediaType: store.MediaHDD, Interface: store.IfSATA},
		{Name: "PhysicalDrive2", Model: "SanDisk Cruzer", SizeBytes: 16008609792, MediaType: store.MediaUnknown, Interface: store.IfUSB, Removable: true},
		{Name: "PhysicalDrive3", Model: "Msft Virtual Disk", SizeBytes: 137438953472, MediaType: store.MediaUnknown, Interface: store.IfSCSI},
		{Name: "PhysicalDrive4", Model: "SAS HDD", SizeBytes: 1, MediaType: store.MediaHDD, Interface: store.IfSAS},
		{Name: "PhysicalDrive5", Model: "SD", SizeBytes: 1, MediaType: store.MediaSSD, Interface: store.IfMMC, Removable: true},
		{Name: "PhysicalDrive6", Model: "Weird", MediaType: store.MediaUnknown, Interface: store.IfOther},
	}
	if !reflect.DeepEqual(disks, want) {
		t.Fatalf("disks =\n%+v\nwant\n%+v", disks, want)
	}
	// A single disk is serialised as an object, not an array.
	one, err := ParseWindowsPhysicalDisks([]byte(`{"DeviceId":"0","FriendlyName":"Disk","Size":10,"MediaType":"SSD","BusType":"NVMe"}`))
	if err != nil || len(one) != 1 || one[0].MediaType != store.MediaNVMeSSD || one[0].Interface != store.IfNVMe {
		t.Fatalf("single = %+v %v", one, err)
	}
	for _, bad := range []string{``, `nope`, `[1,2]`, `{"DeviceId":{}}`} {
		if _, err := ParseWindowsPhysicalDisks([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if d, err := ParseWindowsPhysicalDisks([]byte(`null`)); err != nil || d != nil {
		t.Errorf("null = %+v %v", d, err)
	}
}

func TestParseWin32DiskDrive(t *testing.T) {
	js := `[{"Index":0,"Model":"WDC WD10EZEX","SerialNumber":"  WD-WCC0001","Size":"1000202273280","InterfaceType":"IDE","MediaType":"Fixed hard disk media"},
	{"Index":1,"Model":"USB Stick","SerialNumber":null,"Size":8004304896,"InterfaceType":"USB","MediaType":"Removable Media"},
	{"Index":2,"Model":"SCSI Disk","Size":null,"InterfaceType":"SCSI"},{"Index":3,"Model":"Card","InterfaceType":"1394"}]`
	disks, err := ParseWin32DiskDrives([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Disk{
		{Name: "PhysicalDrive0", Model: "WDC WD10EZEX", Serial: "WD-WCC0001", SizeBytes: 1000202273280, MediaType: store.MediaUnknown, Interface: store.IfSATA},
		{Name: "PhysicalDrive1", Model: "USB Stick", SizeBytes: 8004304896, MediaType: store.MediaUnknown, Interface: store.IfUSB, Removable: true},
		{Name: "PhysicalDrive2", Model: "SCSI Disk", MediaType: store.MediaUnknown, Interface: store.IfSCSI},
		{Name: "PhysicalDrive3", Model: "Card", MediaType: store.MediaUnknown, Interface: store.IfOther},
	}
	if !reflect.DeepEqual(disks, want) {
		t.Fatalf("disks =\n%+v\nwant\n%+v", disks, want)
	}
	if _, err := ParseWin32DiskDrives([]byte(`{`)); err == nil {
		t.Fatal("accepted broken JSON")
	}
}

func TestParseWindowsDriveLetters(t *testing.T) {
	js := `[{"DiskNumber":0,"DriveLetter":"C"},{"DiskNumber":0,"DriveLetter":""},{"DiskNumber":1,"DriveLetter":68},
	{"DiskNumber":1,"DriveLetter":null},{"DiskNumber":2,"DriveLetter":"\u0000"},{"DiskNumber":3,"DriveLetter":"e"},{"DiskNumber":4,"DriveLetter":"12"}]`
	m, err := ParseWindowsDriveLetters([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, map[string]string{"C:": "PhysicalDrive0", "D:": "PhysicalDrive1", "E:": "PhysicalDrive3"}) {
		t.Fatalf("letters = %v", m)
	}
	if one, err := ParseWindowsDriveLetters([]byte(`{"DiskNumber":0,"DriveLetter":"C"}`)); err != nil || one["C:"] != "PhysicalDrive0" {
		t.Fatalf("single = %v %v", one, err)
	}
	if _, err := ParseWindowsDriveLetters([]byte(`[{"DiskNumber":"x"}]`)); err == nil {
		t.Fatal("accepted bad disk number")
	}
	// Filesystems ("C:" devices) resolve through the letter map.
	r := WindowsDriveResolver(m)
	if got, _ := r.Disks("C:"); !reflect.DeepEqual(got, []string{"PhysicalDrive0"}) {
		t.Fatalf("C: -> %v", got)
	}
	if got, _ := r.Disks(`c:\`); !reflect.DeepEqual(got, []string{"PhysicalDrive0"}) {
		t.Fatalf(`c:\ -> %v`, got)
	}
	if got, _ := r.Disks("Z:"); got != nil {
		t.Fatalf("Z: -> %v", got)
	}
}

func TestWindowsDisksBounded(t *testing.T) {
	js := "["
	for i := 0; i <= store.MaxDisks; i++ {
		if i > 0 {
			js += ","
		}
		js += `{"DeviceId":"` + string(rune('0'+i%10)) + `","Size":1}`
	}
	js += "]"
	disks, err := ParseWindowsPhysicalDisks([]byte(js))
	if err != nil || len(disks) != store.MaxDisks+1 {
		t.Fatalf("parser keeps every entry (%d, %v); the collector bounds the list", len(disks), err)
	}
}
