package diff_test

import (
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/diff"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// legacyHW is how an agent before 023 reported node-1 (shifted names, no
// empty slots, partition groups instead of disks).
func legacyHW() store.Inventory {
	return store.Inventory{
		OS:         store.OSInfo{Name: "Ubuntu"},
		BIOS:       store.BIOSInfo{Vendor: "AMI", Version: "2.5"},
		Baseboard:  store.BaseboardInfo{Product: "X12DPi", BoardType: "Processor/Memory Module"},
		Chassis:    store.ChassisInfo{Manufacturer: "Supermicro"},
		Processors: []store.Processor{{SocketDesignation: "CPU1", CoreCount: 12}},
		Memory: store.MemoryInfo{TotalPhysicalBytes: 16 << 30, Array: store.MemoryArray{Location: "ISA add-on card", Use: "Video memory"},
			Modules: []store.MemoryModule{{DeviceLocator: "P1-DIMMA1", SerialNumber: "M1", MemoryType: "LPDDR3", FormFactor: "TSOP", CapacityBytes: 16 << 30}}},
		Disks:    []store.Disk{{Partitions: []store.Partition{{Mount: "/", FS: "ext4"}}}},
		Programs: []store.Program{{Name: "curl", Version: "8.0"}},
	}
}

func currentHW() store.Inventory {
	inv := legacyHW()
	inv.HardwareSchema = store.HardwareSchemaCurrent
	inv.Baseboard.BoardType = "Motherboard (includes processor, memory, and I/O)"
	inv.Chassis.Type = "Rack Mount Chassis"
	inv.Processors = []store.Processor{{SocketDesignation: "CPU1", CoreCount: 12, Family: "Intel Xeon processor", Upgrade: "Socket LGA4189"}}
	inv.Memory = store.MemoryInfo{TotalPhysicalBytes: 16 << 30, SlotsTotal: 2, SlotsPopulated: 1,
		Array: store.MemoryArray{Location: "System board or motherboard", Use: "System memory"},
		Modules: []store.MemoryModule{
			{DeviceLocator: "P1-DIMMA1", BankLocator: "P0_Node0_Channel0_Dimm0", SerialNumber: "M1", MemoryType: "DDR4", FormFactor: "DIMM", CapacityBytes: 16 << 30, Populated: true},
			{DeviceLocator: "P1-DIMMB1", BankLocator: "P0_Node0_Channel1_Dimm0"},
		}}
	inv.Disks = []store.Disk{{Name: "nvme0n1", Serial: "S64", SizeBytes: 3840755982336, MediaType: store.MediaNVMeSSD, Interface: store.IfNVMe}}
	return inv
}

func TestDiff_SchemaTransitionSuppressesHardware(t *testing.T) {
	prev, next := legacyHW(), currentHW()
	next.Programs = append(next.Programs, store.Program{Name: "vim", Version: "9.0"})
	changes := diff.Diff(prev, next)
	for _, c := range changes {
		switch c.Category {
		case diff.CatBIOS, diff.CatSystem, diff.CatBaseboard, diff.CatChassis, diff.CatProcessor, diff.CatMemory, diff.CatDisk:
			t.Errorf("hardware change recorded on the 1 -> 2 transition: %+v", c)
		}
	}
	c, ok := find(changes, diff.CatHardwareSchema, diff.CatHardwareSchema)
	if !ok || c.ChangeType != store.ChangeModified || c.Before != "0" || c.After != "2" {
		t.Fatalf("hardware_schema change = %+v %v", c, ok)
	}
	if _, ok := find(changes, diff.CatSoftware, "vim\x009.0"); !ok {
		t.Fatal("non-hardware changes must still be recorded")
	}
	// Schema 1 (explicit) behaves the same.
	prev.HardwareSchema = 1
	if c, ok := find(diff.Diff(prev, next), diff.CatHardwareSchema, diff.CatHardwareSchema); !ok || c.Before != "1" {
		t.Fatalf("schema 1 transition = %+v", c)
	}
	// A downgrade to an old agent records the schema change and the
	// hardware changes (nothing to hide: the old values are wrong again).
	back := diff.Diff(next, legacyHW())
	if _, ok := find(back, diff.CatHardwareSchema, diff.CatHardwareSchema); !ok {
		t.Fatal("2 -> 0 schema change missing")
	}
	if _, ok := find(back, diff.CatChassis, diff.CatChassis); !ok {
		t.Fatal("hardware changes on a downgrade are recorded")
	}
}

func TestDiff_MemoryKeyedByLocator(t *testing.T) {
	prev, next := currentHW(), currentHW()
	// Replaced module in the same slot: one "modified" change of that slot.
	next.Memory.Modules[0].SerialNumber = "M2"
	changes := diff.Diff(prev, next)
	var mem []store.Change
	for _, c := range changes {
		if c.Category == diff.CatMemory {
			mem = append(mem, c)
		}
	}
	if len(mem) != 1 || mem[0].ChangeType != store.ChangeModified || mem[0].ComponentKey != "P1-DIMMA1\x00P0_Node0_Channel0_Dimm0" {
		t.Fatalf("memory changes = %+v", mem)
	}
	// Empty slot populated.
	next = currentHW()
	next.Memory.Modules[1] = store.MemoryModule{DeviceLocator: "P1-DIMMB1", BankLocator: "P0_Node0_Channel1_Dimm0", Populated: true, CapacityBytes: 16 << 30, MemoryType: "DDR4"}
	c, ok := find(diff.Diff(prev, next), diff.CatMemory, "P1-DIMMB1\x00P0_Node0_Channel1_Dimm0")
	if !ok || c.ChangeType != store.ChangeModified {
		t.Fatalf("empty -> populated slot = %+v %v", c, ok)
	}
	// Module removed (slot becomes empty) is a change of that slot too.
	next = currentHW()
	next.Memory.Modules[0] = store.MemoryModule{DeviceLocator: "P1-DIMMA1", BankLocator: "P0_Node0_Channel0_Dimm0"}
	if _, ok := find(diff.Diff(prev, next), diff.CatMemory, "P1-DIMMA1\x00P0_Node0_Channel0_Dimm0"); !ok {
		t.Fatal("populated -> empty slot not recorded")
	}
}

func TestDiff_ProcessorFamilyChange(t *testing.T) {
	prev, next := currentHW(), currentHW()
	next.Processors[0].Family = "Intel Core i9 processor"
	if c, ok := find(diff.Diff(prev, next), diff.CatProcessor, "CPU1"); !ok || c.ChangeType != store.ChangeModified {
		t.Fatalf("processor family change = %+v %v", c, ok)
	}
	if got := diff.Diff(currentHW(), currentHW()); len(got) != 0 {
		t.Fatalf("identical schema-2 inventories: %+v", got)
	}
}
