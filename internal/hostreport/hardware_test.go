package hostreport

import (
	"fmt"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// legacyDigest is the report digest of sampleSnap() as computed by inventory
// 4.3.x (before feature 023): legacy snapshots must keep it, otherwise every
// host would be re-applied by IPAM for no change.
const legacyDigest = "cf349a685230f869a569438dc6dcea02894f54ba665402dde2833473a84b9301"

func hardwareSnap() store.Snapshot {
	s := sampleSnap()
	p := &s.Payload
	p.HardwareSchema = store.HardwareSchemaCurrent
	p.BIOS = store.BIOSInfo{Vendor: "American Megatrends International, LLC.", Version: "2.5", ReleaseDate: "11/26/2025"}
	p.System = store.SystemInfo{Manufacturer: "Supermicro", ProductName: "Super Server", SerialNumber: "SYS-SN-0001", UUID: "u-1", SKUNumber: "sku", Family: "fam"}
	p.Baseboard = store.BaseboardInfo{Manufacturer: "Supermicro", Product: "X12DPi-NT6", SerialNumber: "BB-SN"}
	p.Chassis = store.ChassisInfo{Manufacturer: "Supermicro", Type: "Rack Mount Chassis", SerialNumber: "CH-SN", AssetTag: "A1", BootupState: "Safe"}
	p.Processors = []store.Processor{
		{SocketDesignation: "CPU1", Manufacturer: "Intel(R) Corporation", Version: "Intel(R) Xeon(R) Silver 4310 CPU @ 2.10GHz", CoreCount: 12, ThreadCount: 24, Family: "Xeon", Type: "Central Processor", Upgrade: "Socket LGA4189", SocketPopulated: true},
		{SocketDesignation: "CPU2", Manufacturer: "Intel(R) Corporation", Version: "Intel(R) Xeon(R) Silver 4310 CPU @ 2.10GHz", CoreCount: 12, ThreadCount: 24, Family: "Xeon", Type: "Central Processor", Upgrade: "Socket LGA4189", SocketPopulated: true},
	}
	arr := store.MemoryArray{Location: "System board or motherboard", Use: "System memory", ErrorCorrection: "Single-bit ECC", MaximumCapacity: 12 << 40, NumberOfDevices: 16, Handle: 0x1000}
	p.Memory = store.MemoryInfo{TotalPhysicalBytes: 16 << 30, Array: arr, Arrays: []store.MemoryArray{arr}, SlotsTotal: 2, SlotsPopulated: 1,
		Modules: []store.MemoryModule{
			{DeviceLocator: "P1-DIMMA1", BankLocator: "P0_Node0_Channel0_Dimm0", CapacityBytes: 16 << 30, FormFactor: "DIMM", MemoryType: "DDR4",
				SpeedMTs: 3200, ConfiguredSpeedMTs: 2666, Manufacturer: "Samsung", PartNumber: "M393A2K43EB3-CWE", SerialNumber: "M-1",
				Populated: true, TypeDetail: []string{"Synchronous", "Registered (Buffered)"}, ArrayHandle: 0x1000, RankCount: 2},
			{DeviceLocator: "P1-DIMMB1", BankLocator: "P0_Node0_Channel1_Dimm0", ArrayHandle: 0x1000},
		}}
	p.Disks = []store.Disk{
		{Name: "nvme0n1", Model: "SAMSUNG MZQL23T8HCLS", Serial: "S64-1", SizeBytes: 3840755982336, MediaType: store.MediaNVMeSSD, Interface: store.IfNVMe, Vendor: "Samsung"},
		{Name: "sda", Model: "ST4000NM000A", Serial: "ZC1", SizeBytes: 4000787030016, MediaType: store.MediaHDD, Interface: store.IfSATA,
			Partitions: []store.Partition{{Mount: "/legacy", FS: "ext4"}}},
	}
	p.Filesystems = []store.Filesystem{{Mount: "/", FS: "ext4", Device: "/dev/mapper/vg-root", SizeBytes: 100, FreeBytes: 40, Disks: []string{"nvme0n1", "sda"}}}
	p.Availability = store.HardwareAvailability{SMBIOS: store.AvailOK, Disks: store.AvailOK}
	p.Truncated.Disks, p.Truncated.MemorySlots = 1, 2
	return s
}

func TestHardwareOnlyForCurrentSchema(t *testing.T) {
	for _, schema := range []uint32{0, 1} {
		s := hardwareSnap()
		s.Payload.HardwareSchema = schema
		if r := Project(sampleHost(), s); r.GetHardware() != nil {
			t.Fatalf("schema %d must not carry hardware (legacy decoding): %v", schema, r.GetHardware())
		}
	}
	if Project(sampleHost(), hardwareSnap()).GetHardware() == nil {
		t.Fatal("schema 2 must carry hardware")
	}
}

func TestHardwareMapping(t *testing.T) {
	r := Project(sampleHost(), hardwareSnap())
	hw := r.GetHardware()
	if hw.GetSchema() != 2 || hw.GetBios().GetVersion() != "2.5" || hw.GetBios().GetReleaseDate() != "11/26/2025" ||
		hw.GetSystem().GetProductName() != "Super Server" || hw.GetSystem().GetSerialNumber() != "SYS-SN-0001" ||
		hw.GetBaseboard().GetProduct() != "X12DPi-NT6" || hw.GetChassis().GetType() != "Rack Mount Chassis" ||
		hw.GetChassis().GetBootupState() != "Safe" {
		t.Fatalf("identity parts = %v", hw)
	}
	if len(hw.GetProcessors()) != 2 || hw.GetProcessors()[0].GetFamily() != "Xeon" || hw.GetProcessors()[1].GetUpgrade() != "Socket LGA4189" ||
		hw.GetProcessors()[0].GetThreadCount() != 24 || hw.GetProcessors()[0].GetType() != "Central Processor" {
		t.Fatalf("processors = %v", hw.GetProcessors())
	}
	m := hw.GetMemory()
	if m.GetTotalPhysicalBytes() != 16<<30 || m.GetSlotsTotal() != 2 || m.GetSlotsPopulated() != 1 || len(m.GetArrays()) != 1 ||
		m.GetArray().GetErrorCorrection() != "Single-bit ECC" || m.GetArray().GetHandle() != 0x1000 || len(m.GetModules()) != 2 {
		t.Fatalf("memory = %v", m)
	}
	if mod := m.GetModules()[0]; !mod.GetPopulated() || mod.GetMemoryType() != "DDR4" || mod.GetFormFactor() != "DIMM" ||
		len(mod.GetTypeDetail()) != 2 || mod.GetTypeDetail()[1] != "Registered (Buffered)" || mod.GetConfiguredSpeedMtS() != 2666 ||
		mod.GetRankCount() != 2 || mod.GetArrayHandle() != 0x1000 {
		t.Fatalf("module = %v", mod)
	}
	if m.GetModules()[1].GetPopulated() {
		t.Fatal("empty slot must be projected as not populated")
	}
	if len(hw.GetDisks()) != 2 || hw.GetDisks()[0].GetMediaType() != store.MediaNVMeSSD || hw.GetDisks()[0].GetVendor() != "Samsung" ||
		hw.GetDisks()[1].GetName() != "sda" {
		t.Fatalf("disks = %v", hw.GetDisks())
	}
	for _, d := range hw.GetDisks() {
		if len(d.GetPartitions()) != 0 {
			t.Fatalf("legacy partitions must never be projected: %v", d)
		}
	}
	if fs := hw.GetFilesystems(); len(fs) != 1 || fs[0].GetDevice() != "/dev/mapper/vg-root" || len(fs[0].GetDisks()) != 2 || fs[0].GetFreeBytes() != 40 {
		t.Fatalf("filesystems = %v", fs)
	}
	if hw.GetAvailability().GetSmbios() != store.AvailOK || hw.GetAvailability().GetDisks() != store.AvailOK {
		t.Fatalf("availability = %v", hw.GetAvailability())
	}
	if r.GetTruncated().GetDisks() != 1 || r.GetTruncated().GetMemorySlots() != 2 {
		t.Fatalf("truncated = %v", r.GetTruncated())
	}
	// The stored snapshot keeps its legacy partitions (projection copies).
	if s := hardwareSnap(); len(s.Payload.Disks[1].Partitions) != 1 {
		t.Fatal("projection must not modify the snapshot")
	}
}

func TestHardwareBounded(t *testing.T) {
	s := hardwareSnap()
	p := &s.Payload
	p.Processors, p.Memory.Modules, p.Memory.Arrays, p.Disks, p.Filesystems = nil, nil, nil, nil, nil
	for i := 0; i < store.MaxProcessors+3; i++ {
		p.Processors = append(p.Processors, store.Processor{SocketDesignation: fmt.Sprint("CPU", i)})
	}
	for i := 0; i < store.MaxMemorySlots+3; i++ {
		p.Memory.Modules = append(p.Memory.Modules, store.MemoryModule{DeviceLocator: fmt.Sprint("D", i)})
	}
	for i := 0; i < store.MaxMemoryArrays+3; i++ {
		p.Memory.Arrays = append(p.Memory.Arrays, store.MemoryArray{Handle: uint32(i)})
	}
	for i := 0; i < store.MaxDisks+3; i++ {
		p.Disks = append(p.Disks, store.Disk{Name: fmt.Sprint("sd", i)})
	}
	for i := 0; i < store.MaxFilesystems+3; i++ {
		fs := store.Filesystem{Mount: fmt.Sprint("/m", i)}
		if i == 0 {
			for j := 0; j < store.MaxFSDisks+3; j++ {
				fs.Disks = append(fs.Disks, fmt.Sprint("sd", j))
			}
		}
		p.Filesystems = append(p.Filesystems, fs)
	}
	hw := Project(sampleHost(), s).GetHardware()
	if len(hw.GetProcessors()) != store.MaxProcessors || len(hw.GetMemory().GetModules()) != store.MaxMemorySlots ||
		len(hw.GetMemory().GetArrays()) != store.MaxMemoryArrays || len(hw.GetDisks()) != store.MaxDisks ||
		len(hw.GetFilesystems()) != store.MaxFilesystems || len(hw.GetFilesystems()[0].GetDisks()) != store.MaxFSDisks {
		t.Fatalf("projection must stay within the collection bounds")
	}
}

func TestLegacyDigestUnchanged(t *testing.T) {
	if got := Project(sampleHost(), sampleSnap()).GetReportDigest(); got != legacyDigest {
		t.Fatalf("legacy digest = %s, want the 4.3.x digest %s", got, legacyDigest)
	}
	// A legacy snapshot carrying (wrongly decoded) hardware keeps it too.
	s := hardwareSnap()
	s.Payload.HardwareSchema = 1
	s.Payload.Truncated.Disks, s.Payload.Truncated.MemorySlots = 0, 0
	if got := Project(sampleHost(), s).GetReportDigest(); got != legacyDigest {
		t.Fatalf("legacy hardware changed the digest: %s", got)
	}
}

func TestHardwareDigest(t *testing.T) {
	base := Project(sampleHost(), hardwareSnap()).GetReportDigest()
	if base == legacyDigest {
		t.Fatal("hardware must be covered by the digest")
	}
	if Project(sampleHost(), hardwareSnap()).GetReportDigest() != base {
		t.Fatal("hardware digest not deterministic")
	}
	mut := []func(*store.Inventory){
		func(p *store.Inventory) { p.BIOS.Version = "2.6" },
		func(p *store.Inventory) { p.BIOS.ReleaseDate = "01/01/2026" },
		func(p *store.Inventory) { p.System.SerialNumber = "SYS-SN-0002" },
		func(p *store.Inventory) { p.Baseboard.SerialNumber = "BB-2" },
		func(p *store.Inventory) { p.Chassis.Type = "Tower" },
		func(p *store.Inventory) { p.Processors[1].CoreCount = 16 },
		func(p *store.Inventory) { p.Processors = p.Processors[:1] },
		func(p *store.Inventory) { p.Memory.TotalPhysicalBytes = 32 << 30 },
		func(p *store.Inventory) { p.Memory.Modules[1].Populated = true },
		func(p *store.Inventory) { p.Memory.Modules[0].TypeDetail = nil },
		func(p *store.Inventory) { p.Memory.Arrays[0].ErrorCorrection = "None" },
		func(p *store.Inventory) { p.Memory.SlotsPopulated = 2 },
		func(p *store.Inventory) { p.Disks[0].Serial = "S64-2" },
		func(p *store.Inventory) { p.Disks = p.Disks[:1] },
		func(p *store.Inventory) { p.Disks[1].Removable = true },
		func(p *store.Inventory) { p.Filesystems[0].Disks = []string{"sda"} },
		func(p *store.Inventory) { p.Availability.Disks = store.AvailPartial },
	}
	for i, m := range mut {
		s := hardwareSnap()
		m(&s.Payload)
		if Project(sampleHost(), s).GetReportDigest() == base {
			t.Errorf("hardware mutation %d did not change the digest", i)
		}
	}
	// Free bytes move on every report and are not a change of the host (like
	// update_state.checked_at): the digest ignores them.
	s := hardwareSnap()
	s.Payload.Filesystems[0].FreeBytes = 39
	if Project(sampleHost(), s).GetReportDigest() != base {
		t.Error("filesystem free bytes must not change the digest")
	}
	if Project(sampleHost(), s).GetHardware().GetFilesystems()[0].GetFreeBytes() != 39 {
		t.Error("free bytes must still be projected")
	}
}
