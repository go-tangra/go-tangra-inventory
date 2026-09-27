package ingest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func TestValidateHardware_BoundsTruncateAndCount(t *testing.T) {
	inv := store.Inventory{HardwareSchema: store.HardwareSchemaCurrent, Truncated: store.CollectionLimits{Disks: 1}}
	for i := 0; i < store.MaxDisks+1; i++ {
		inv.Disks = append(inv.Disks, store.Disk{Name: fmt.Sprintf("sd%d", i), MediaType: store.MediaHDD, Interface: store.IfSATA})
	}
	for i := 0; i < store.MaxMemorySlots+1; i++ {
		inv.Memory.Modules = append(inv.Memory.Modules, store.MemoryModule{DeviceLocator: fmt.Sprintf("DIMM%d", i), Populated: true})
	}
	for i := 0; i < store.MaxMemoryArrays+1; i++ {
		inv.Memory.Arrays = append(inv.Memory.Arrays, store.MemoryArray{Handle: uint32(i)})
	}
	for i := 0; i < store.MaxProcessors+1; i++ {
		inv.Processors = append(inv.Processors, store.Processor{SocketDesignation: fmt.Sprintf("CPU%d", i)})
	}
	for i := 0; i < store.MaxFilesystems+1; i++ {
		inv.Filesystems = append(inv.Filesystems, store.Filesystem{Mount: fmt.Sprintf("/m%d", i)})
	}
	for i := 0; i < store.MaxFSDisks+1; i++ {
		inv.Filesystems[0].Disks = append(inv.Filesystems[0].Disks, fmt.Sprintf("sd%d", i))
	}

	validateExtended(&inv)

	if len(inv.Disks) != store.MaxDisks || inv.Disks[0].Name != "sd0" || inv.Disks[store.MaxDisks-1].Name != "sd255" {
		t.Fatalf("disks = %d (first N must be kept)", len(inv.Disks))
	}
	if len(inv.Memory.Modules) != store.MaxMemorySlots || len(inv.Memory.Arrays) != store.MaxMemoryArrays {
		t.Fatalf("modules = %d arrays = %d", len(inv.Memory.Modules), len(inv.Memory.Arrays))
	}
	if len(inv.Processors) != store.MaxProcessors || len(inv.Filesystems) != store.MaxFilesystems {
		t.Fatalf("processors = %d filesystems = %d", len(inv.Processors), len(inv.Filesystems))
	}
	if len(inv.Filesystems[0].Disks) != store.MaxFSDisks {
		t.Fatalf("filesystem disk refs = %d", len(inv.Filesystems[0].Disks))
	}
	want := store.CollectionLimits{Disks: 1 + 1 + 1, MemorySlots: 1, MemoryArrays: 1, Processors: 1, Filesystems: 1}
	if inv.Truncated != want {
		t.Fatalf("truncated = %+v, want %+v (disk refs count as disks)", inv.Truncated, want)
	}
}

func TestValidateHardware_StringsCleaned(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	inv := store.Inventory{
		HardwareSchema: 7,
		BIOS:           store.BIOSInfo{Vendor: "American\x00 Megatrends\n", Version: long},
		System:         store.SystemInfo{Manufacturer: "Super\x7fmicro", SerialNumber: "S\x01N"},
		Baseboard:      store.BaseboardInfo{Product: "X12\u0085DPi"},
		Chassis:        store.ChassisInfo{Type: "Rack Mount Chassis", BootupState: "Safe\tx"},
		Processors:     []store.Processor{{Version: "Xeon\x1b[31m", Family: long, Upgrade: "Socket LGA4189"}},
		Memory: store.MemoryInfo{
			Array:   store.MemoryArray{Location: "System board or motherboard\x00"},
			Arrays:  []store.MemoryArray{{Use: "System memory\r"}},
			Modules: []store.MemoryModule{{PartNumber: "M393A2K43EB3-CWE\x00\x00", TypeDetail: []string{"Synchronous\n", "", long}}},
		},
		Disks: []store.Disk{
			{Name: "sda", Model: "Samsung\x00SSD", MediaType: "flash", Interface: "thunderbolt"},
			{Name: "nvme0n1", MediaType: store.MediaNVMeSSD, Interface: store.IfNVMe},
			{Name: "legacy"}, // agents < 023: media/interface empty stay empty
		},
		Filesystems:  []store.Filesystem{{Mount: "/\x00", FS: "ext4", Device: "/dev/mapper/vg-root", Disks: []string{"sda\n", ""}}},
		Availability: store.HardwareAvailability{SMBIOS: "great", Disks: store.AvailPartial},
	}
	bad := string([]byte{'a', 0xff, 'b'})
	inv.System.Family = bad

	validateExtended(&inv)

	if inv.HardwareSchema != store.HardwareSchemaCurrent {
		t.Fatalf("hardware_schema = %d, want clamped to %d", inv.HardwareSchema, store.HardwareSchemaCurrent)
	}
	checks := map[string][2]string{
		"bios.vendor":      {inv.BIOS.Vendor, "American Megatrends"},
		"system.mfr":       {inv.System.Manufacturer, "Supermicro"},
		"system.serial":    {inv.System.SerialNumber, "SN"},
		"system.family":    {inv.System.Family, "ab"},
		"board.product":    {inv.Baseboard.Product, "X12DPi"},
		"chassis.bootup":   {inv.Chassis.BootupState, "Safex"},
		"cpu.version":      {inv.Processors[0].Version, "Xeon[31m"},
		"array.location":   {inv.Memory.Array.Location, "System board or motherboard"},
		"arrays.use":       {inv.Memory.Arrays[0].Use, "System memory"},
		"module.part":      {inv.Memory.Modules[0].PartNumber, "M393A2K43EB3-CWE"},
		"disk.model":       {inv.Disks[0].Model, "SamsungSSD"},
		"disk.media":       {inv.Disks[0].MediaType, store.MediaUnknown},
		"disk.iface":       {inv.Disks[0].Interface, store.IfOther},
		"nvme.media":       {inv.Disks[1].MediaType, store.MediaNVMeSSD},
		"nvme.iface":       {inv.Disks[1].Interface, store.IfNVMe},
		"legacy.media":     {inv.Disks[2].MediaType, ""},
		"legacy.iface":     {inv.Disks[2].Interface, ""},
		"fs.mount":         {inv.Filesystems[0].Mount, "/"},
		"avail.smbios":     {inv.Availability.SMBIOS, store.AvailUnknown},
		"avail.disks":      {inv.Availability.Disks, store.AvailPartial},
		"bios.version.len": {fmt.Sprint(len(inv.BIOS.Version) <= store.MaxHWString), "true"},
		"cpu.family.len":   {fmt.Sprint(len(inv.Processors[0].Family) <= store.MaxHWString), "true"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
	if got := inv.Memory.Modules[0].TypeDetail; len(got) != 2 || got[0] != "Synchronous" || len(got[1]) > store.MaxHWString {
		t.Errorf("type_detail = %q (empty entries dropped, others cleaned and clipped)", got)
	}
	if got := inv.Filesystems[0].Disks; len(got) != 1 || got[0] != "sda" {
		t.Errorf("filesystem disks = %q", got)
	}
}

func TestValidateHardware_TypeDetailBounded(t *testing.T) {
	inv := store.Inventory{Memory: store.MemoryInfo{Modules: []store.MemoryModule{{}}}}
	for i := 0; i < 40; i++ {
		inv.Memory.Modules[0].TypeDetail = append(inv.Memory.Modules[0].TypeDetail, fmt.Sprint("bit", i))
	}
	validateExtended(&inv)
	if n := len(inv.Memory.Modules[0].TypeDetail); n != maxTypeDetail {
		t.Fatalf("type_detail entries = %d, want %d", n, maxTypeDetail)
	}
}

func TestValidateHardware_LegacyUntouched(t *testing.T) {
	inv := store.Inventory{
		Memory: store.MemoryInfo{Modules: []store.MemoryModule{{DeviceLocator: "DIMM_A1", CapacityBytes: 8 << 30, MemoryType: "LPDDR3"}}},
		Disks:  []store.Disk{{Partitions: []store.Partition{{Mount: "/", FS: "ext4"}}}},
	}
	validateExtended(&inv)
	if inv.HardwareSchema != 0 || inv.Memory.Modules[0].Populated || inv.Memory.Modules[0].MemoryType != "LPDDR3" {
		t.Fatalf("legacy payload must be kept as reported: %+v", inv.Memory.Modules[0])
	}
	if len(inv.Disks) != 1 || len(inv.Disks[0].Partitions) != 1 || inv.Truncated != (store.CollectionLimits{}) {
		t.Fatalf("legacy disks changed: %+v %+v", inv.Disks, inv.Truncated)
	}
}

func TestSubmit_HardwareOversizedStillInvalidArgument(t *testing.T) {
	srv, ctx := fuzzServer(t, 64<<10)
	pb := &inventoryv1.Inventory{Identity: &inventoryv1.Identity{Hostname: "big"}, HardwareSchema: 2}
	for i := 0; i < 2000; i++ {
		pb.Disks = append(pb.Disks, &inventoryv1.Disk{Name: fmt.Sprintf("sd%d", i), Model: strings.Repeat("m", 100)})
	}
	_, err := srv.SubmitInventory(ctx, &inventoryv1.SubmitRequest{Inventory: pb})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument before truncation", err)
	}
}

func TestSubmit_HardwareRoundTrip(t *testing.T) {
	srv, ctx := fuzzServer(t, 8<<20)
	pb := &inventoryv1.Inventory{
		Identity:       &inventoryv1.Identity{Hostname: "hw1"},
		HardwareSchema: 2,
		Chassis:        &inventoryv1.ChassisInfo{Type: "Rack Mount Chassis", BootupState: "Safe"},
		Processors:     []*inventoryv1.Processor{{SocketDesignation: "CPU1", Family: "Xeon", Type: "Central Processor", Upgrade: "Socket LGA4189"}},
		Memory: &inventoryv1.MemoryInfo{
			SlotsTotal: 2, SlotsPopulated: 1,
			Arrays:  []*inventoryv1.MemoryArray{{Handle: 0x1000, Use: "System memory"}},
			Modules: []*inventoryv1.MemoryModule{{DeviceLocator: "P1-DIMMA1", Populated: true, TypeDetail: []string{"Registered (Buffered)"}, ArrayHandle: 0x1000, RankCount: 2}, {DeviceLocator: "P1-DIMMB1"}},
		},
		Disks:                []*inventoryv1.Disk{{Name: "nvme0n1", MediaType: "nvme_ssd", Interface: "nvme", Removable: false, Vendor: "Samsung"}},
		Filesystems:          []*inventoryv1.Filesystem{{Mount: "/", Fs: "ext4", Device: "/dev/nvme0n1p2", SizeBytes: 100, FreeBytes: 40, Disks: []string{"nvme0n1"}}},
		HardwareAvailability: &inventoryv1.HardwareAvailability{Smbios: "ok", Disks: "ok"},
	}
	if _, err := srv.SubmitInventory(ctx, &inventoryv1.SubmitRequest{Inventory: pb}); err != nil {
		t.Fatal(err)
	}
	snap := latestSnapshot(t, srv, ctx)
	inv := snap.Payload
	if inv.HardwareSchema != 2 || inv.Chassis.BootupState != "Safe" || inv.Processors[0].Upgrade != "Socket LGA4189" {
		t.Fatalf("hardware not stored: %+v", inv)
	}
	if inv.Memory.SlotsTotal != 2 || !inv.Memory.Modules[0].Populated || inv.Memory.Modules[1].Populated || inv.Memory.Arrays[0].Handle != 0x1000 {
		t.Fatalf("memory not stored: %+v", inv.Memory)
	}
	if inv.Disks[0].Vendor != "Samsung" || inv.Filesystems[0].Disks[0] != "nvme0n1" || inv.Availability.SMBIOS != store.AvailOK {
		t.Fatalf("disks/filesystems not stored: %+v %+v", inv.Disks, inv.Filesystems)
	}
}

// latestSnapshot returns the snapshot stored by the fuzz server's last submit.
func latestSnapshot(t *testing.T, srv *Server, ctx context.Context) store.Snapshot {
	t.Helper()
	agent, _ := AgentFromContext(ctx)
	a, err := srv.st.GetAgent(ctx, agent.TenantID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := srv.st.GetLatestForHost(ctx, agent.TenantID, a.HostID)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
