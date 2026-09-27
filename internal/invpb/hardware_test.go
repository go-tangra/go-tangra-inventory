package invpb

import (
	"reflect"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func sampleHardware() store.Inventory {
	arr := store.MemoryArray{Location: "System board or motherboard", Use: "System memory", ErrorCorrection: "Single-bit ECC",
		MaximumCapacity: 12 << 40, NumberOfDevices: 16, Handle: 0x1000}
	return store.Inventory{
		BIOS:      store.BIOSInfo{Vendor: "AMI", Version: "2.5", ReleaseDate: "11/26/2025"},
		System:    store.SystemInfo{Manufacturer: "Supermicro", ProductName: "Super Server", Version: "0123", SerialNumber: "S1", UUID: "u", WakeUpType: "Power Switch", SKUNumber: "sku", Family: "fam"},
		Baseboard: store.BaseboardInfo{Manufacturer: "Supermicro", Product: "X12DPi", Version: "1.0", SerialNumber: "B1", AssetTag: "a", LocationInChassis: "l", BoardType: "Motherboard"},
		Chassis:   store.ChassisInfo{Manufacturer: "Supermicro", Version: "v", SerialNumber: "C1", AssetTag: "t", SKUNumber: "s", Type: "Rack Mount Chassis", BootupState: "Safe"},
		Processors: []store.Processor{{SocketDesignation: "CPU1", Manufacturer: "Intel", Version: "Xeon Silver 4310", MaxSpeedMHz: 4000,
			CurrentSpeedMHz: 2100, CoreCount: 12, CoreEnabled: 12, ThreadCount: 24, PartNumber: "p", SerialNumber: "s", SocketPopulated: true,
			Family: "Xeon", Type: "Central Processor", Upgrade: "Socket LGA4189"}},
		Memory: store.MemoryInfo{TotalPhysicalBytes: 16 << 30, Array: arr, Arrays: []store.MemoryArray{arr}, SlotsTotal: 2, SlotsPopulated: 1,
			Modules: []store.MemoryModule{
				{DeviceLocator: "P1-DIMMA1", BankLocator: "P0_Node0_Channel0_Dimm0", CapacityBytes: 16 << 30, FormFactor: "DIMM", MemoryType: "DDR4",
					SpeedMTs: 3200, ConfiguredSpeedMTs: 2666, Manufacturer: "Samsung", SerialNumber: "M1", PartNumber: "M393A2K43EB3-CWE",
					Populated: true, TypeDetail: []string{"Synchronous", "Registered (Buffered)"}, ArrayHandle: 0x1000, AssetTag: "at", RankCount: 2},
				{DeviceLocator: "P1-DIMMB1", ArrayHandle: 0x1000},
			}},
		Disks: []store.Disk{
			{Name: "nvme0n1", Model: "PM9A3", Serial: "S64", SizeBytes: 3840755982336, MediaType: store.MediaNVMeSSD, Interface: store.IfNVMe, Vendor: "Samsung", Removable: false},
			{Name: "sdb", Model: "Cruzer", SizeBytes: 16 << 30, MediaType: store.MediaUnknown, Interface: store.IfUSB, Removable: true},
			{Partitions: []store.Partition{{Mount: "/", FS: "ext4", SizeBytes: 10, FreeBytes: 5}}},
		},
		Filesystems:  []store.Filesystem{{Mount: "/", FS: "ext4", Device: "/dev/nvme0n1p2", SizeBytes: 10, FreeBytes: 5, Disks: []string{"nvme0n1"}}},
		Availability: store.HardwareAvailability{SMBIOS: store.AvailOK, Disks: store.AvailPartial},
		Truncated:    store.CollectionLimits{Interfaces: 1, Addresses: 2, Guests: 3, Packages: 4, BmcPorts: 5, Disks: 6, MemorySlots: 7, MemoryArrays: 8, Processors: 9, Filesystems: 10},
	}
}

func TestHardwareRoundTrip(t *testing.T) {
	in := sampleHardware()
	if got := BIOSFromPB(BIOSToPB(in.BIOS)); got != in.BIOS {
		t.Errorf("bios = %+v", got)
	}
	if got := SystemFromPB(SystemToPB(in.System)); got != in.System {
		t.Errorf("system = %+v", got)
	}
	if got := BaseboardFromPB(BaseboardToPB(in.Baseboard)); got != in.Baseboard {
		t.Errorf("baseboard = %+v", got)
	}
	if got := ChassisFromPB(ChassisToPB(in.Chassis)); got != in.Chassis {
		t.Errorf("chassis = %+v", got)
	}
	if got := ProcessorsFromPB(ProcessorsToPB(in.Processors)); !reflect.DeepEqual(got, in.Processors) {
		t.Errorf("processors = %+v", got)
	}
	if got := MemoryFromPB(MemoryToPB(in.Memory)); !reflect.DeepEqual(got, in.Memory) {
		t.Errorf("memory = %+v\nwant %+v", got, in.Memory)
	}
	if got := DisksFromPB(DisksToPB(in.Disks)); !reflect.DeepEqual(got, in.Disks) {
		t.Errorf("disks = %+v", got)
	}
	if got := FilesystemsFromPB(FilesystemsToPB(in.Filesystems)); !reflect.DeepEqual(got, in.Filesystems) {
		t.Errorf("filesystems = %+v", got)
	}
	if got := AvailabilityFromPB(AvailabilityToPB(in.Availability)); got != in.Availability {
		t.Errorf("availability = %+v", got)
	}
	if got := LimitsFromPB(LimitsToPB(in.Truncated)); got != in.Truncated {
		t.Errorf("limits = %+v", got)
	}
}

func TestHardwareEmptyValues(t *testing.T) {
	if ProcessorsToPB(nil) != nil || ProcessorsFromPB(nil) != nil || DisksToPB(nil) != nil || DisksFromPB(nil) != nil ||
		FilesystemsToPB(nil) != nil || FilesystemsFromPB(nil) != nil {
		t.Fatal("empty lists map to nil")
	}
	if AvailabilityToPB(store.HardwareAvailability{}) != nil {
		t.Fatal("zero availability maps to nil")
	}
	if LimitsToPB(store.CollectionLimits{}) != nil {
		t.Fatal("zero limits map to nil")
	}
	// Singletons always map to a message (the payload shape agents send).
	if BIOSToPB(store.BIOSInfo{}) == nil || SystemToPB(store.SystemInfo{}) == nil || BaseboardToPB(store.BaseboardInfo{}) == nil ||
		ChassisToPB(store.ChassisInfo{}) == nil || MemoryToPB(store.MemoryInfo{}) == nil {
		t.Fatal("singletons map to a message")
	}
	if !reflect.DeepEqual(MemoryFromPB(nil), store.MemoryInfo{}) || ChassisFromPB(nil) != (store.ChassisInfo{}) {
		t.Fatal("nil messages map to zero values")
	}
	// A legacy memory message (no arrays) keeps Arrays nil and modules as reported.
	legacy := MemoryToPB(store.MemoryInfo{Modules: []store.MemoryModule{{DeviceLocator: "A1", CapacityBytes: 1}}})
	if got := MemoryFromPB(legacy); got.Arrays != nil || len(got.Modules) != 1 || got.Modules[0].Populated || got.Modules[0].TypeDetail != nil {
		t.Fatalf("legacy memory = %+v", got)
	}
}
