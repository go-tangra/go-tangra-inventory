package agentfacts

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

var v33 = SMBIOSVersion{Major: 3, Minor: 3}

// st builds a structure whose Formatted bytes are b placed at offset 04h,
// with the header length covering exactly Formatted.
func st(typ uint8, handle uint16, b []byte, strs ...string) SMBIOSStructure {
	return SMBIOSStructure{Type: typ, Length: uint8(4 + len(b)), Handle: handle, Formatted: b, Strings: strs}
}

// formatted returns a zeroed formatted area for a structure of total length n.
func formatted(n int) []byte { return make([]byte, n-4) }

func put8(b []byte, off int, v uint8)   { b[off-4] = v }
func put16(b []byte, off int, v uint16) { binary.LittleEndian.PutUint16(b[off-4:], v) }
func put32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off-4:], v) }
func put64(b []byte, off int, v uint64) { binary.LittleEndian.PutUint64(b[off-4:], v) }

func TestEnumTables(t *testing.T) {
	cases := []struct {
		name  string
		fn    func(uint16) string
		codes map[uint16]string
	}{
		{"memory type", MemoryTypeName, map[uint16]string{
			0x01: "Other", 0x02: "Unknown", 0x03: "DRAM", 0x04: "EDRAM", 0x05: "VRAM", 0x06: "SRAM", 0x07: "RAM", 0x08: "ROM",
			0x09: "FLASH", 0x0A: "EEPROM", 0x0B: "FEPROM", 0x0C: "EPROM", 0x0D: "CDRAM", 0x0E: "3DRAM", 0x0F: "SDRAM",
			0x10: "SGRAM", 0x11: "RDRAM", 0x12: "DDR", 0x13: "DDR2", 0x14: "DDR2 FB-DIMM",
			0x15: "Unknown (code 21)", 0x16: "Unknown (code 22)", 0x17: "Unknown (code 23)",
			0x18: "DDR3", 0x19: "FBD2", 0x1A: "DDR4", 0x1B: "LPDDR", 0x1C: "LPDDR2", 0x1D: "LPDDR3", 0x1E: "LPDDR4",
			0x1F: "Logical non-volatile device", 0x20: "HBM", 0x21: "HBM2", 0x22: "DDR5", 0x23: "LPDDR5", 0x24: "HBM3",
			0x00: "Unknown (code 0)", 0x25: "Unknown (code 37)", 0xFF: "Unknown (code 255)",
		}},
		{"form factor", FormFactorName, map[uint16]string{
			0x01: "Other", 0x02: "Unknown", 0x03: "SIMM", 0x04: "SIP", 0x05: "Chip", 0x06: "DIP", 0x07: "ZIP",
			0x08: "Proprietary Card", 0x09: "DIMM", 0x0A: "TSOP", 0x0B: "Row of chips", 0x0C: "RIMM", 0x0D: "SODIMM",
			0x0E: "SRIMM", 0x0F: "FB-DIMM", 0x10: "Die", 0x11: "CAMM", 0x00: "Unknown (code 0)", 0x12: "Unknown (code 18)",
		}},
		{"array location", ArrayLocationName, map[uint16]string{
			0x01: "Other", 0x02: "Unknown", 0x03: "System board or motherboard", 0x04: "ISA add-on card",
			0x05: "EISA add-on card", 0x06: "PCI add-on card", 0x07: "MCA add-on card", 0x08: "PCMCIA add-on card",
			0x09: "Proprietary add-on card", 0x0A: "NuBus", 0xA0: "PC-98/C20 add-on card", 0xA1: "PC-98/C24 add-on card",
			0xA2: "PC-98/E add-on card", 0xA3: "PC-98/Local bus add-on card", 0xA4: "CXL add-on card",
			0x0B: "Unknown (code 11)", 0x00: "Unknown (code 0)",
		}},
		{"array use", ArrayUseName, map[uint16]string{
			0x01: "Other", 0x02: "Unknown", 0x03: "System memory", 0x04: "Video memory", 0x05: "Flash memory",
			0x06: "Non-volatile RAM", 0x07: "Cache memory", 0x08: "Unknown (code 8)",
		}},
		{"ecc", ErrorCorrectionName, map[uint16]string{
			0x01: "Other", 0x02: "Unknown", 0x03: "None", 0x04: "Parity", 0x05: "Single-bit ECC", 0x06: "Multi-bit ECC",
			0x07: "CRC", 0x08: "Unknown (code 8)",
		}},
		{"chassis", ChassisTypeName, map[uint16]string{
			0x01: "Other", 0x02: "Unknown", 0x03: "Desktop", 0x04: "Low Profile Desktop", 0x05: "Pizza Box", 0x06: "Mini Tower",
			0x07: "Tower", 0x08: "Portable", 0x09: "Laptop", 0x0A: "Notebook", 0x0B: "Hand Held", 0x0C: "Docking Station",
			0x0D: "All in One", 0x0E: "Sub Notebook", 0x0F: "Space-saving", 0x10: "Lunch Box", 0x11: "Main Server Chassis",
			0x12: "Expansion Chassis", 0x13: "SubChassis", 0x14: "Bus Expansion Chassis", 0x15: "Peripheral Chassis",
			0x16: "RAID Chassis", 0x17: "Rack Mount Chassis", 0x18: "Sealed-case PC", 0x19: "Multi-system chassis",
			0x1A: "Compact PCI", 0x1B: "Advanced TCA", 0x1C: "Blade", 0x1D: "Blade Enclosure", 0x1E: "Tablet",
			0x1F: "Convertible", 0x20: "Detachable", 0x21: "IoT Gateway", 0x22: "Embedded PC", 0x23: "Mini PC",
			0x24: "Stick PC", 0x25: "Unknown (code 37)",
		}},
		{"bootup state", ChassisStateName, map[uint16]string{
			0x01: "Other", 0x02: "Unknown", 0x03: "Safe", 0x04: "Warning", 0x05: "Critical", 0x06: "Non-recoverable",
			0x07: "Unknown (code 7)",
		}},
		{"processor type", ProcessorTypeName, map[uint16]string{
			0x01: "Other", 0x02: "Unknown", 0x03: "Central Processor", 0x04: "Math Processor", 0x05: "DSP Processor",
			0x06: "Video Processor", 0x07: "Unknown (code 7)",
		}},
		{"processor family", ProcessorFamilyName, map[uint16]string{
			0x01: "Other", 0x02: "Unknown", 0x0B: "Intel Pentium processor", 0x28: "Intel Core Duo processor",
			0x2B: "Intel Atom processor", 0x6B: "AMD Zen Processor Family", 0x83: "AMD Athlon 64 Processor Family",
			0x84: "AMD Opteron Processor Family", 0xB3: "Intel Xeon processor", 0xC6: "Intel Core i7 processor",
			0xCD: "Intel Core i5 processor", 0xCE: "Intel Core i3 processor", 0xCF: "Intel Core i9 processor",
			0x100: "ARMv7", 0x101: "ARMv8", 0x102: "ARMv9", 0x118: "ARM", 0x200: "RISC-V RV32", 0x201: "RISC-V RV64",
			0xFE: "Unknown (code 254)", 0xFF: "Unknown (code 255)", 0x1FFF: "Unknown (code 8191)",
		}},
		{"processor upgrade", ProcessorUpgradeName, map[uint16]string{
			0x01: "Other", 0x02: "Unknown", 0x03: "Daughter Board", 0x04: "ZIF Socket", 0x06: "None", 0x15: "Socket LGA775",
			0x19: "Socket LGA1366", 0x26: "Socket LGA2011", 0x31: "Socket AM4", 0x36: "Socket LGA3647-1", 0x37: "Socket SP3",
			0x3D: "Socket LGA4189", 0x3E: "Socket LGA1200", 0x3F: "Socket LGA4677", 0x40: "Socket LGA1700",
			0x49: "Socket AM5", 0x4A: "Socket SP5", 0x00: "Unknown (code 0)", 0xFF: "Unknown (code 255)",
		}},
		{"board type", BoardTypeName, map[uint16]string{
			0x01: "Unknown", 0x02: "Other", 0x03: "Server Blade", 0x04: "Connectivity Switch", 0x05: "System Management Module",
			0x06: "Processor Module", 0x07: "I/O Module", 0x08: "Memory Module", 0x09: "Daughter board",
			0x0A: "Motherboard (includes processor, memory, and I/O)", 0x0B: "Processor/Memory Module",
			0x0C: "Processor/IO Module", 0x0D: "Interconnect board", 0x0E: "Unknown (code 14)",
		}},
		{"wake-up type", WakeUpTypeName, map[uint16]string{
			0x00: "Unknown (code 0)", 0x01: "Other", 0x02: "Unknown", 0x03: "APM Timer", 0x04: "Modem Ring", 0x05: "LAN Remote",
			0x06: "Power Switch", 0x07: "PCI PME#", 0x08: "AC Power Restored", 0x09: "Unknown (code 9)",
		}},
	}
	for _, c := range cases {
		for code, want := range c.codes {
			if got := c.fn(code); got != want {
				t.Errorf("%s 0x%02X = %q, want %q", c.name, code, got, want)
			}
		}
	}
}

// TestProcessorFamilyTableComplete: every defined processor family code maps
// to a name and no name is a neighbour's (the table is keyed, not indexed).
func TestProcessorFamilyTableComplete(t *testing.T) {
	seen := map[string]uint16{}
	for code, name := range processorFamilies {
		if name == "" || strings.HasPrefix(name, "Unknown (code") {
			t.Errorf("family 0x%X has no name", code)
		}
		if prev, dup := seen[name]; dup && name != "Reserved" {
			t.Errorf("family name %q used by 0x%X and 0x%X", name, prev, code)
		}
		seen[name] = code
	}
	if len(processorFamilies) < 200 {
		t.Errorf("processor family table has %d entries, want the DSP0134 3.7 table", len(processorFamilies))
	}
}

func TestTypeDetailBits(t *testing.T) {
	names := []string{"", "Other", "Unknown", "Fast-paged", "Static column", "Pseudo-static", "RAMBUS", "Synchronous",
		"CMOS", "EDO", "Window DRAM", "Cache DRAM", "Non-volatile", "Registered (Buffered)", "Unbuffered (Unregistered)", "LRDIMM"}
	for bit := 1; bit < 16; bit++ {
		if got := TypeDetailNames(uint16(1) << bit); !reflect.DeepEqual(got, []string{names[bit]}) {
			t.Errorf("bit %d = %q, want %q", bit, got, names[bit])
		}
	}
	if got := TypeDetailNames(1); got != nil {
		t.Errorf("reserved bit 0 = %q, want nothing", got)
	}
	// DDR4 RDIMM: Synchronous (bit 7) + Registered (bit 13) = 0x2080; the
	// high byte matters (go-smbios read only the low byte).
	if got := TypeDetailNames(0x2080); !reflect.DeepEqual(got, []string{"Synchronous", "Registered (Buffered)"}) {
		t.Errorf("0x2080 = %q", got)
	}
	if got := TypeDetailNames(0x4080); !reflect.DeepEqual(got, []string{"Synchronous", "Unbuffered (Unregistered)"}) {
		t.Errorf("0x4080 = %q", got)
	}
	if got := TypeDetailNames(0x8080); !reflect.DeepEqual(got, []string{"Synchronous", "LRDIMM"}) {
		t.Errorf("0x8080 = %q", got)
	}
	if got := TypeDetailNames(0); got != nil {
		t.Errorf("0 = %q", got)
	}
}

func memDevice(handle, array uint16, size uint16, speed, configured uint16) SMBIOSStructure {
	b := formatted(0x5C)
	put16(b, 0x04, array)
	put16(b, 0x0C, size)
	put8(b, 0x0E, 0x09) // DIMM
	put8(b, 0x10, 1)    // device locator = string 1
	put8(b, 0x11, 2)
	put8(b, 0x12, 0x1A) // DDR4
	put16(b, 0x13, 0x2080)
	put16(b, 0x15, speed)
	put8(b, 0x17, 3)
	put8(b, 0x18, 4)
	put8(b, 0x19, 5)
	put8(b, 0x1A, 6)
	put8(b, 0x1B, 0x02) // rank 2
	put16(b, 0x20, configured)
	return st(17, handle, b, "P1-DIMMA1", "P0_Node0_Channel0_Dimm0", "Samsung", "SN-1", "AT-1", "M393A2K43EB3-CWE  ")
}

func TestMemoryDeviceSizesAndSpeeds(t *testing.T) {
	arr := physArray(0x1000, 0x03, 0x03, 0x05, 16)
	cases := []struct {
		name        string
		dev         SMBIOSStructure
		populated   bool
		capBytes    uint64
		speed, conf uint32
	}{
		{"MB", memDevice(0x1100, 0x1000, 16384, 3200, 2666), true, 16 << 30, 3200, 2666},
		{"KB granularity", memDevice(0x1101, 0x1000, 0x8000|512, 0, 0), true, 512 << 10, 0, 0},
		{"empty slot", memDevice(0x1102, 0x1000, 0, 0, 0), false, 0, 0, 0},
		{"unknown size", memDevice(0x1103, 0x1000, 0xFFFF, 0, 0), true, 0, 0, 0},
	}
	ext := memDevice(0x1104, 0x1000, 0x7FFF, 0xFFFF, 0xFFFF)
	put32(ext.Formatted, 0x1C, 0x80000000|40960) // bit 31 reserved: masked
	put32(ext.Formatted, 0x54, 8800)
	put32(ext.Formatted, 0x58, 8000)
	cases = append(cases, struct {
		name        string
		dev         SMBIOSStructure
		populated   bool
		capBytes    uint64
		speed, conf uint32
	}{"extended size and speed", ext, true, 40960 << 20, 8800, 8000})

	for _, c := range cases {
		hw := DecodeSMBIOS(v33, []SMBIOSStructure{arr, c.dev})
		if len(hw.Memory.Modules) != 1 {
			t.Fatalf("%s: modules = %d", c.name, len(hw.Memory.Modules))
		}
		m := hw.Memory.Modules[0]
		if m.Populated != c.populated || m.CapacityBytes != c.capBytes || m.SpeedMTs != c.speed || m.ConfiguredSpeedMTs != c.conf {
			t.Errorf("%s: %+v", c.name, m)
		}
		if c.populated && (m.MemoryType != "DDR4" || m.FormFactor != "DIMM" || m.RankCount != 2 || m.ArrayHandle != 0x1000 ||
			m.PartNumber != "M393A2K43EB3-CWE" || m.AssetTag != "AT-1" || m.BankLocator != "P0_Node0_Channel0_Dimm0") {
			t.Errorf("%s: fields %+v", c.name, m)
		}
		if !c.populated && (m.MemoryType != "" || m.Manufacturer != "" || m.TypeDetail != nil || m.DeviceLocator != "P1-DIMMA1") {
			t.Errorf("%s: an empty slot keeps only its locators: %+v", c.name, m)
		}
	}
}

func physArray(handle uint16, loc, use, ecc uint8, devices uint16) SMBIOSStructure {
	b := formatted(0x17)
	put8(b, 0x04, loc)
	put8(b, 0x05, use)
	put8(b, 0x06, ecc)
	put32(b, 0x07, 0x80000000)
	put16(b, 0x0B, 0xFFFE)
	put16(b, 0x0D, devices)
	put64(b, 0x0F, 12<<40)
	return st(16, handle, b)
}

func TestMemoryArraysAndTotals(t *testing.T) {
	sys := physArray(0x1000, 0x03, 0x03, 0x05, 2)
	video := physArray(0x2000, 0x06, 0x04, 0x03, 1)
	small := physArray(0x3000, 0x03, 0x03, 0x03, 1)
	b := small.Formatted
	put32(b, 0x07, 64<<20)                       // 64 GiB in KiB, no extended capacity needed
	small = st(16, 0x3000, b[:0x0F-4])           // SMBIOS 2.1 length: extended capacity absent
	d1 := memDevice(0x1100, 0x1000, 16384, 0, 0) // system, 16 GiB
	d2 := memDevice(0x1101, 0x1000, 0, 0, 0)     // system, empty
	d3 := memDevice(0x2100, 0x2000, 1024, 0, 0)  // video memory: not in the total
	d4 := memDevice(0x3100, 0x3000, 8192, 0, 0)  // second system array
	hw := DecodeSMBIOS(v33, []SMBIOSStructure{video, sys, small, d1, d2, d3, d4})
	m := hw.Memory
	if len(m.Arrays) != 3 || m.Array.Handle != 0x1000 || m.Array.Use != "System memory" || m.Array.Location != "System board or motherboard" ||
		m.Array.ErrorCorrection != "Single-bit ECC" || m.Array.MaximumCapacity != 12<<40 || m.Array.NumberOfDevices != 2 {
		t.Fatalf("primary array = %+v", m.Array)
	}
	if m.Arrays[2].MaximumCapacity != 64<<30 {
		t.Fatalf("KiB maximum capacity = %d", m.Arrays[2].MaximumCapacity)
	}
	if m.TotalPhysicalBytes != (16+8)<<30 {
		t.Fatalf("total = %d, want only system memory arrays", m.TotalPhysicalBytes)
	}
	if m.SlotsTotal != 4 || m.SlotsPopulated != 3 {
		t.Fatalf("slots = %d/%d", m.SlotsPopulated, m.SlotsTotal)
	}
	// Without any array (some VMs) every populated module counts.
	hw = DecodeSMBIOS(v33, []SMBIOSStructure{d1, d3})
	if hw.Memory.TotalPhysicalBytes != 17<<30 || hw.Memory.Array != (store.MemoryArray{}) {
		t.Fatalf("no-array total = %d %+v", hw.Memory.TotalPhysicalBytes, hw.Memory.Array)
	}
	// Without a system-memory array the first array is the primary one.
	hw = DecodeSMBIOS(v33, []SMBIOSStructure{video, d3})
	if hw.Memory.Array.Handle != 0x2000 || hw.Memory.TotalPhysicalBytes != 1<<30 {
		t.Fatalf("no system array: %+v", hw.Memory)
	}
}

func TestFieldsBeyondLengthNotAvailable(t *testing.T) {
	// SMBIOS 2.1 memory device (length 15h): no speed, manufacturer, part
	// number, rank or configured speed.
	d := memDevice(0x1100, 0x1000, 4096, 1600, 1600)
	short := st(17, 0x1100, d.Formatted[:0x15-4], d.Strings...)
	m := DecodeSMBIOS(v33, []SMBIOSStructure{short}).Memory.Modules[0]
	if m.CapacityBytes != 4<<30 || m.MemoryType != "DDR4" || len(m.TypeDetail) != 2 {
		t.Fatalf("available fields = %+v", m)
	}
	if m.SpeedMTs != 0 || m.Manufacturer != "" || m.PartNumber != "" || m.RankCount != 0 || m.ConfiguredSpeedMTs != 0 {
		t.Fatalf("fields beyond the structure length must be not available: %+v", m)
	}
	// Header.Length shorter than the formatted bytes wins.
	lying := d
	lying.Length = 0x12
	m = DecodeSMBIOS(v33, []SMBIOSStructure{lying}).Memory.Modules[0]
	if m.MemoryType != "" || m.TypeDetail != nil || m.DeviceLocator != "P1-DIMMA1" {
		t.Fatalf("header length must bound the fields: %+v", m)
	}
}

func TestProcessorDecoding(t *testing.T) {
	b := formatted(0x30)
	put8(b, 0x04, 1)
	put8(b, 0x05, 0x03)
	put8(b, 0x06, 0xB3)
	put8(b, 0x07, 2)
	put8(b, 0x10, 3)
	put16(b, 0x14, 4000)
	put16(b, 0x16, 2100)
	put8(b, 0x18, 0x41) // populated, enabled
	put8(b, 0x19, 0x3D)
	put8(b, 0x20, 4)
	put8(b, 0x22, 5)
	put8(b, 0x23, 12)
	put8(b, 0x24, 12)
	put8(b, 0x25, 24)
	put16(b, 0x28, 0xB3)
	cpu := st(4, 0x0400, b, "CPU1", "Intel(R) Corporation", "Intel(R) Xeon(R) Silver 4310 CPU @ 2.10GHz", "PSN", "PN")
	p := DecodeSMBIOS(v33, []SMBIOSStructure{cpu}).Processors[0]
	want := store.Processor{SocketDesignation: "CPU1", Manufacturer: "Intel(R) Corporation", Version: "Intel(R) Xeon(R) Silver 4310 CPU @ 2.10GHz",
		MaxSpeedMHz: 4000, CurrentSpeedMHz: 2100, CoreCount: 12, CoreEnabled: 12, ThreadCount: 24, PartNumber: "PN", SerialNumber: "PSN",
		SocketPopulated: true, Family: "Intel Xeon processor", Type: "Central Processor", Upgrade: "Socket LGA4189"}
	if p != want {
		t.Fatalf("processor = %+v\nwant %+v", p, want)
	}

	// Family 2 (FEh) and core/thread count 2 (FFh) for large parts.
	put8(b, 0x06, 0xFE)
	put16(b, 0x28, 0x101)
	put8(b, 0x23, 0xFF)
	put8(b, 0x24, 0xFF)
	put8(b, 0x25, 0xFF)
	b = append(b, make([]byte, 8)...)
	put16(b, 0x2A, 128)
	put16(b, 0x2C, 120)
	put16(b, 0x2E, 256)
	p = DecodeSMBIOS(v33, []SMBIOSStructure{st(4, 0x0400, b, "CPU1")}).Processors[0]
	if p.Family != "ARMv8" || p.CoreCount != 128 || p.CoreEnabled != 120 || p.ThreadCount != 256 {
		t.Fatalf("family 2 / count 2 = %+v", p)
	}
	// FEh without a family 2 field (SMBIOS < 2.6) is reported as unknown.
	p = DecodeSMBIOS(v33, []SMBIOSStructure{st(4, 0x0400, b[:0x28-4], "CPU1")}).Processors[0]
	if p.Family != "Unknown (code 254)" || p.CoreCount != 255 {
		t.Fatalf("family FEh without family 2 = %+v", p)
	}
	// Unpopulated socket.
	put8(b, 0x18, 0x00)
	if DecodeSMBIOS(v33, []SMBIOSStructure{st(4, 0x0400, b, "CPU2")}).Processors[0].SocketPopulated {
		t.Fatal("status bit 6 clear = unpopulated")
	}
}

func TestSystemBoardChassisBIOS(t *testing.T) {
	bios := formatted(0x1A)
	put8(bios, 0x04, 1)
	put8(bios, 0x05, 2)
	put8(bios, 0x08, 3)
	sys := formatted(0x1B)
	put8(sys, 0x04, 1)
	put8(sys, 0x05, 2)
	put8(sys, 0x06, 3)
	put8(sys, 0x07, 4)
	copy(sys[0x08-4:], []byte{0x33, 0x22, 0x11, 0x00, 0x55, 0x44, 0x77, 0x66, 0x88, 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF})
	put8(sys, 0x18, 0x06)
	put8(sys, 0x19, 5)
	put8(sys, 0x1A, 6)
	board := formatted(0x0F)
	put8(board, 0x04, 1)
	put8(board, 0x05, 2)
	put8(board, 0x06, 3)
	put8(board, 0x07, 4)
	put8(board, 0x08, 5)
	put8(board, 0x0A, 6)
	put8(board, 0x0D, 0x0A)
	ch := formatted(0x15 + 2*3 + 1)
	put8(ch, 0x04, 1)
	put8(ch, 0x05, 0x80|0x17) // lock present + Rack Mount Chassis
	put8(ch, 0x06, 2)
	put8(ch, 0x07, 3)
	put8(ch, 0x08, 4)
	put8(ch, 0x09, 0x03)
	put8(ch, 0x13, 2) // two contained elements of 3 bytes
	put8(ch, 0x14, 3)
	put8(ch, 0x15+6, 5) // SKU after the contained elements
	hw := DecodeSMBIOS(v33, []SMBIOSStructure{
		st(0, 0, bios, "American Megatrends International, LLC.", "2.5", "11/26/2025"),
		st(1, 1, sys, "Supermicro", "Super Server", "0123456789", "S123", "To be filled by O.E.M.", "Default string"),
		st(2, 2, board, "Supermicro", "X12DPi-NT6", "1.02", "BB123", "Base Board Asset Tagging", "Part Component"),
		st(3, 3, ch, "Supermicro", "0123456789", "C123", "Chassis Asset Tag", "SKU-1"),
	})
	if hw.BIOS != (store.BIOSInfo{Vendor: "American Megatrends International, LLC.", Version: "2.5", ReleaseDate: "11/26/2025"}) {
		t.Errorf("bios = %+v", hw.BIOS)
	}
	wantSys := store.SystemInfo{Manufacturer: "Supermicro", ProductName: "Super Server", Version: "0123456789", SerialNumber: "S123",
		UUID: "00112233-4455-6677-8899-aabbccddeeff", WakeUpType: "Power Switch", SKUNumber: "", Family: "Default string"}
	if hw.System != wantSys {
		t.Errorf("system = %+v\nwant %+v", hw.System, wantSys)
	}
	if hw.Baseboard != (store.BaseboardInfo{Manufacturer: "Supermicro", Product: "X12DPi-NT6", Version: "1.02", SerialNumber: "BB123",
		AssetTag: "Base Board Asset Tagging", LocationInChassis: "Part Component", BoardType: "Motherboard (includes processor, memory, and I/O)"}) {
		t.Errorf("board = %+v", hw.Baseboard)
	}
	if hw.Chassis != (store.ChassisInfo{Manufacturer: "Supermicro", Version: "0123456789", SerialNumber: "C123", AssetTag: "Chassis Asset Tag",
		SKUNumber: "SKU-1", Type: "Rack Mount Chassis", BootupState: "Safe"}) {
		t.Errorf("chassis = %+v", hw.Chassis)
	}
	// SMBIOS 2.5 byte order of the UUID (no middle-endian swap).
	hw = DecodeSMBIOS(SMBIOSVersion{Major: 2, Minor: 5}, []SMBIOSStructure{st(1, 1, sys, "x")})
	if hw.System.UUID != "33221100-5544-7766-8899-aabbccddeeff" {
		t.Errorf("2.5 uuid = %s", hw.System.UUID)
	}
	// A 2.0 system structure has no UUID, wake-up type, SKU or family.
	hw = DecodeSMBIOS(SMBIOSVersion{Major: 2, Minor: 0}, []SMBIOSStructure{st(1, 1, sys[:0x08-4], "a", "b")})
	if hw.System.UUID != "" || hw.System.WakeUpType != "" || hw.System.ProductName != "b" {
		t.Errorf("2.0 system = %+v", hw.System)
	}
}

func TestStringsBoundedAndCleaned(t *testing.T) {
	bios := formatted(0x12)
	put8(bios, 0x04, 1)
	put8(bios, 0x05, 2)
	put8(bios, 0x08, 9) // index beyond the string set
	hw := DecodeSMBIOS(v33, []SMBIOSStructure{st(0, 0, bios, strings.Repeat("A", 300), string([]byte{0xff, 0xfe}))})
	if len(hw.BIOS.Vendor) != maxSMBIOSString || hw.BIOS.Version != "" || hw.BIOS.ReleaseDate != "" {
		t.Fatalf("bios = %q %q %q", hw.BIOS.Vendor, hw.BIOS.Version, hw.BIOS.ReleaseDate)
	}
}

func TestOtherStringStructures(t *testing.T) {
	cache := formatted(0x13)
	put8(cache, 0x04, 1)
	port := formatted(0x09)
	put8(port, 0x04, 1)
	put8(port, 0x06, 2)
	port2 := formatted(0x09)
	put8(port2, 0x06, 1)
	slot := formatted(0x11)
	put8(slot, 0x04, 1)
	oem := formatted(0x05)
	put8(oem, 0x04, 2)
	lang := formatted(0x16)
	put8(lang, 0x15, 2)
	hw := DecodeSMBIOS(v33, []SMBIOSStructure{
		st(7, 1, cache, "L1 Cache"), st(8, 2, port, "J1", "USB1"), st(8, 3, port2, "COM1"), st(8, 4, formatted(0x09)),
		st(9, 5, slot, "CPU1 SLOT1 PCI-E 4.0 X16"), st(9, 6, formatted(0x11)),
		st(11, 7, oem, "Intel Corporation", "Supermicro"), st(13, 8, lang, "fr|CA|iso8859-1", "en|US|iso8859-1"),
	})
	if !reflect.DeepEqual(hw.Cache, []store.CacheInfo{{SocketDesignation: "L1 Cache"}}) || !reflect.DeepEqual(hw.Ports, []string{"J1 / USB1", "COM1"}) ||
		!reflect.DeepEqual(hw.Slots, []string{"CPU1 SLOT1 PCI-E 4.0 X16"}) || !reflect.DeepEqual(hw.OEMStrings, []string{"Intel Corporation", "Supermicro"}) ||
		hw.BIOSLanguage != "en|US|iso8859-1" {
		t.Fatalf("string structures = %+v", hw)
	}
}

func TestCollectionBounds(t *testing.T) {
	var ss []SMBIOSStructure
	for i := 0; i < store.MaxProcessors+2; i++ {
		ss = append(ss, st(4, uint16(i), formatted(0x1A)))
	}
	for i := 0; i < store.MaxMemoryArrays+3; i++ {
		ss = append(ss, physArray(uint16(0x1000+i), 3, 3, 5, 1))
	}
	for i := 0; i < store.MaxMemorySlots+4; i++ {
		ss = append(ss, memDevice(uint16(0x2000+i), 0x1000, 0, 0, 0))
	}
	hw := DecodeSMBIOS(v33, ss)
	if len(hw.Processors) != store.MaxProcessors || len(hw.Memory.Arrays) != store.MaxMemoryArrays || len(hw.Memory.Modules) != store.MaxMemorySlots {
		t.Fatalf("bounds: %d processors %d arrays %d modules", len(hw.Processors), len(hw.Memory.Arrays), len(hw.Memory.Modules))
	}
	if hw.Truncated != (store.CollectionLimits{Processors: 2, MemoryArrays: 3, MemorySlots: 4}) {
		t.Fatalf("truncated = %+v", hw.Truncated)
	}
	if hw.Memory.SlotsTotal != store.MaxMemorySlots {
		t.Fatalf("slots total = %d", hw.Memory.SlotsTotal)
	}
}

func TestAvailability(t *testing.T) {
	full := []SMBIOSStructure{st(0, 0, formatted(0x12)), st(1, 1, formatted(0x1B)), st(2, 2, formatted(0x0F)), st(3, 3, formatted(0x15)),
		st(4, 4, formatted(0x1A)), physArray(0x1000, 3, 3, 5, 1), memDevice(0x1100, 0x1000, 1024, 0, 0)}
	if got := DecodeSMBIOS(v33, full).Availability; got != store.AvailOK {
		t.Fatalf("full table = %q", got)
	}
	for i := range full {
		part := append(append([]SMBIOSStructure{}, full[:i]...), full[i+1:]...)
		if got := DecodeSMBIOS(v33, part).Availability; got != store.AvailPartial {
			t.Errorf("without type %d = %q", full[i].Type, got)
		}
	}
	if got := DecodeSMBIOS(v33, nil).Availability; got != store.AvailPartial {
		t.Fatalf("empty table = %q", got)
	}
}

func ExampleMemoryTypeName() {
	fmt.Println(MemoryTypeName(0x1A), FormFactorName(0x09), ArrayUseName(0x03), ErrorCorrectionName(0x05))
	// Output: DDR4 DIMM System memory Single-bit ECC
}

func TestSmallEdges(t *testing.T) {
	// 80000000h without the 2.7 extended capacity field: unknown (0).
	a := physArray(0x1000, 3, 3, 5, 1)
	a = st(16, 0x1000, a.Formatted[:0x0F-4])
	if got := DecodeSMBIOS(v33, []SMBIOSStructure{a}).Memory.Array.MaximumCapacity; got != 0 {
		t.Fatalf("capacity = %d", got)
	}
	if got := clipString(strings.Repeat("é", 200), 5); got != "éé" {
		t.Fatalf("clip = %q", got)
	}
	// Two maximal port designations joined stay within the string bound (fuzz 650c7fd52dd3536c).
	if got := portLabel(strings.Repeat("a", maxSMBIOSString), strings.Repeat("b", maxSMBIOSString)); len(got) != maxSMBIOSString {
		t.Fatalf("port label = %d bytes", len(got))
	}
	// go-smbios nil entries are skipped and the structure count is bounded.
	tbl, err := smbiosDecode(encode(qemuTable()), SMBIOSVersion{2, 8})
	if err != nil {
		t.Fatal(err)
	}
	tbl.Structures = append(tbl.Structures, nil)
	for len(tbl.Structures) <= maxSMBIOSStructures {
		tbl.Structures = append(tbl.Structures, tbl.Structures[0])
	}
	if _, s := FromGoSMBIOS(tbl); len(s) != maxSMBIOSStructures {
		t.Fatalf("structures = %d", len(s))
	}
}
