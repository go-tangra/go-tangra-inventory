package inventoryclient_test

import (
	"context"
	"testing"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/sdk/v4/pkg/inventoryclient"
)

func hardwareProfile() *invv1.HardwareProfile {
	arr := &invv1.MemoryArray{Location: "System board or motherboard", Use: "System memory", ErrorCorrection: "Single-bit ECC",
		MaximumCapacity: 12 << 40, NumberOfDevices: 16, Handle: 0x1000}
	return &invv1.HardwareProfile{
		Schema:    2,
		Bios:      &invv1.BIOSInfo{Vendor: "American Megatrends International, LLC.", Version: "2.5", ReleaseDate: "11/26/2025"},
		System:    &invv1.SystemInfo{Manufacturer: "Supermicro", ProductName: "Super Server", SerialNumber: "SYS1", Uuid: "u", SkuNumber: "sku", Family: "fam"},
		Baseboard: &invv1.BaseboardInfo{Manufacturer: "Supermicro", Product: "X12DPi-NT6", SerialNumber: "BB1"},
		Chassis:   &invv1.ChassisInfo{Manufacturer: "Supermicro", Type: "Rack Mount Chassis", SerialNumber: "CH1", AssetTag: "A1", BootupState: "Safe"},
		Processors: []*invv1.Processor{{SocketDesignation: "CPU1", Manufacturer: "Intel(R) Corporation", Version: "Intel(R) Xeon(R) Silver 4310 CPU @ 2.10GHz",
			MaxSpeedMhz: 4000, CurrentSpeedMhz: 2100, CoreCount: 12, CoreEnabled: 12, ThreadCount: 24, SocketPopulated: true,
			Family: "Xeon", Type: "Central Processor", Upgrade: "Socket LGA4189"}},
		Memory: &invv1.MemoryInfo{TotalPhysicalBytes: 16 << 30, Array: arr, Arrays: []*invv1.MemoryArray{arr}, SlotsTotal: 2, SlotsPopulated: 1,
			Modules: []*invv1.MemoryModule{
				{DeviceLocator: "P1-DIMMA1", BankLocator: "P0_Node0_Channel0_Dimm0", CapacityBytes: 16 << 30, FormFactor: "DIMM", MemoryType: "DDR4",
					SpeedMtS: 3200, ConfiguredSpeedMtS: 2666, Manufacturer: "Samsung", SerialNumber: "M1", PartNumber: "M393A2K43EB3-CWE",
					Populated: true, TypeDetail: []string{"Synchronous", "Registered (Buffered)"}, ArrayHandle: 0x1000, AssetTag: "at", RankCount: 2},
				{DeviceLocator: "P1-DIMMB1", ArrayHandle: 0x1000},
			}},
		Disks: []*invv1.Disk{{Name: "nvme0n1", Model: "SAMSUNG MZQL23T8HCLS", Serial: "S64", SizeBytes: 3840755982336,
			MediaType: "nvme_ssd", Interface: "nvme", Vendor: "Samsung"}, {Name: "sdb", MediaType: "unknown", Interface: "usb", Removable: true}},
		Filesystems:  []*invv1.Filesystem{{Mount: "/", Fs: "ext4", Device: "/dev/nvme0n1p2", SizeBytes: 100, FreeBytes: 40, Disks: []string{"nvme0n1"}}},
		Availability: &invv1.HardwareAvailability{Smbios: "ok", Disks: "partial"},
	}
}

// hardwareReports serves one report with hardware and one legacy report.
type hardwareReports struct {
	invv1.UnimplementedHostReportServiceServer
}

func (hardwareReports) ListHostReports(context.Context, *invv1.ListHostReportsRequest) (*invv1.ListHostReportsResponse, error) {
	withHW := fullReport("h-1")
	withHW.Hardware = hardwareProfile()
	withHW.Truncated.Disks, withHW.Truncated.MemorySlots, withHW.Truncated.MemoryArrays = 6, 7, 8
	withHW.Truncated.Processors, withHW.Truncated.Filesystems = 9, 10
	return &invv1.ListHostReportsResponse{Reports: []*invv1.HostReport{withHW, fullReport("h-legacy")}}, nil
}

func TestHostReportHardware(t *testing.T) {
	c := dialReports(t, hardwareReports{}, nil)
	reports, _, err := c.ListHostReports(context.Background(), "t-1", inventoryclient.ReportFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %d", len(reports))
	}
	if reports[1].Hardware != nil {
		t.Fatalf("legacy report must have no hardware: %+v", reports[1].Hardware)
	}
	hw := reports[0].Hardware
	if hw == nil {
		t.Fatal("hardware missing")
	}
	if hw.Schema != 2 || hw.BIOS.Version != "2.5" || hw.BIOS.ReleaseDate != "11/26/2025" || hw.System.ProductName != "Super Server" ||
		hw.System.SerialNumber != "SYS1" || hw.Board.Product != "X12DPi-NT6" || hw.Chassis.Type != "Rack Mount Chassis" || hw.Chassis.BootupState != "Safe" {
		t.Fatalf("identity parts = %+v", hw)
	}
	if len(hw.Processors) != 1 || hw.Processors[0].Family != "Xeon" || hw.Processors[0].Type != "Central Processor" ||
		hw.Processors[0].Upgrade != "Socket LGA4189" || hw.Processors[0].ThreadCount != 24 {
		t.Fatalf("processors = %+v", hw.Processors)
	}
	m := hw.Memory
	if m.TotalPhysicalBytes != 16<<30 || m.SlotsTotal != 2 || m.SlotsPopulated != 1 || len(m.Arrays) != 1 || m.Arrays[0].Handle != 0x1000 ||
		m.Array.ErrorCorrection != "Single-bit ECC" || len(m.Modules) != 2 {
		t.Fatalf("memory = %+v", m)
	}
	if mod := m.Modules[0]; !mod.Populated || mod.MemoryType != "DDR4" || len(mod.TypeDetail) != 2 || mod.ArrayHandle != 0x1000 ||
		mod.AssetTag != "at" || mod.RankCount != 2 || mod.ConfiguredSpeedMTs != 2666 {
		t.Fatalf("module = %+v", mod)
	}
	if m.Modules[1].Populated {
		t.Fatal("empty slot must stay empty")
	}
	if len(hw.Disks) != 2 || hw.Disks[0].Name != "nvme0n1" || hw.Disks[0].Vendor != "Samsung" || hw.Disks[0].MediaType != "nvme_ssd" ||
		!hw.Disks[1].Removable {
		t.Fatalf("disks = %+v", hw.Disks)
	}
	if len(hw.Filesystems) != 1 || hw.Filesystems[0].Device != "/dev/nvme0n1p2" || hw.Filesystems[0].Disks[0] != "nvme0n1" ||
		hw.Filesystems[0].FreeBytes != 40 || hw.Filesystems[0].FS != "ext4" {
		t.Fatalf("filesystems = %+v", hw.Filesystems)
	}
	if hw.Availability.SMBIOS != "ok" || hw.Availability.Disks != "partial" {
		t.Fatalf("availability = %+v", hw.Availability)
	}
	tr := reports[0].Truncated
	if tr.Disks != 6 || tr.MemorySlots != 7 || tr.MemoryArrays != 8 || tr.Processors != 9 || tr.Filesystems != 10 {
		t.Fatalf("truncated = %+v", tr)
	}
}

// hardwareSnapshots serves a snapshot payload carrying the 023 fields.
type hardwareSnapshots struct {
	invv1.UnimplementedInventorySnapshotServiceServer
}

func (hardwareSnapshots) GetLatestByHost(context.Context, *invv1.GetLatestByHostRequest) (*invv1.Snapshot, error) {
	hw := hardwareProfile()
	return &invv1.Snapshot{Summary: &invv1.SnapshotSummary{Id: "s-1"}, Payload: &invv1.Inventory{
		HardwareSchema: 2, Chassis: hw.GetChassis(), Processors: hw.GetProcessors(), Memory: hw.GetMemory(), Disks: hw.GetDisks(),
		Filesystems: hw.GetFilesystems(), HardwareAvailability: hw.GetAvailability(),
		Truncated: &invv1.CollectionLimits{Disks: 1, Filesystems: 2},
	}}, nil
}

func TestToInventoryHardwareFields(t *testing.T) {
	c := dialReports(t, &fakeReports{}, hardwareSnapshots{})
	s, err := c.GetLatestSnapshot(context.Background(), "t-1", "h-1")
	if err != nil {
		t.Fatal(err)
	}
	inv := s.Payload
	if inv.HardwareSchema != 2 || inv.Chassis.BootupState != "Safe" || inv.Processors[0].Upgrade != "Socket LGA4189" ||
		inv.Memory.SlotsTotal != 2 || !inv.Memory.Modules[0].Populated || len(inv.Memory.Arrays) != 1 ||
		inv.Disks[0].Name != "nvme0n1" || !inv.Disks[1].Removable || len(inv.Filesystems) != 1 || inv.Filesystems[0].Disks[0] != "nvme0n1" ||
		inv.Availability.Disks != "partial" || inv.Truncated.Disks != 1 || inv.Truncated.Filesystems != 2 {
		t.Fatalf("inventory = %+v", inv)
	}
}
