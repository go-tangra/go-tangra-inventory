package agentfacts

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// SMBIOS decoding (feature 023, research D1). go-smbios v0.3.4 numbers its
// enumerations from 0 while DSP0134 codes start at 1 (and its memory type
// list lacks two reserved entries), which shifted every decoded value on
// real hardware ("LPDDR3" for DDR4, "TSOP" for DIMM, "Video memory" for
// System memory, ...). The agent therefore keeps go-smbios only to find and
// split the table and decodes every field here from the raw structures,
// keyed by the DSP0134 3.7 code tables. Trademark symbols of the table text
// are omitted. A code without a name is reported as "Unknown (code N)",
// never as a neighbouring name; a field beyond the structure's length is
// not available and left empty.

// SMBIOSStructure is one raw SMBIOS structure: the header fields, the
// formatted area starting at offset 04h and the string set.
type SMBIOSStructure struct {
	Type      uint8
	Length    uint8 // header length including the 4-byte header
	Handle    uint16
	Formatted []byte
	Strings   []string
}

// SMBIOSVersion is the table's major.minor version (entry point).
type SMBIOSVersion struct {
	Major, Minor int
}

func (v SMBIOSVersion) atLeast(major, minor int) bool {
	return v.Major > major || (v.Major == major && v.Minor >= minor)
}

// SMBIOSHardware is the hardware decoded from an SMBIOS table.
type SMBIOSHardware struct {
	BIOS         store.BIOSInfo
	System       store.SystemInfo
	Baseboard    store.BaseboardInfo
	Chassis      store.ChassisInfo
	Processors   []store.Processor
	Cache        []store.CacheInfo
	Memory       store.MemoryInfo
	Ports        []string
	Slots        []string
	OEMStrings   []string
	BIOSLanguage string
	// Truncated counts processors, memory arrays and memory slots dropped at
	// the collection bounds.
	Truncated store.CollectionLimits
	// Availability is ok when the BIOS, system, baseboard, chassis,
	// processor, memory array and memory device structures are all present,
	// partial otherwise.
	Availability string
}

// maxSMBIOSString bounds every decoded string (bytes).
const maxSMBIOSString = store.MaxHWString

// Bounds of the lists that are not collection bounds of the report.
const (
	maxCaches     = 64
	maxPorts      = 128
	maxSlots      = 128
	maxOEMStrings = 64
)

// DecodeSMBIOS decodes the hardware of a raw SMBIOS table. It never panics
// on malformed structures; the BIOS, system, baseboard and chassis come from
// the last structure of their type (as go-smbios did, keeping the system
// UUID — a host identity key — stable across the decoder change).
func DecodeSMBIOS(v SMBIOSVersion, structs []SMBIOSStructure) SMBIOSHardware {
	var hw SMBIOSHardware
	seen := map[uint8]bool{}
	var arrays []store.MemoryArray
	var modules []store.MemoryModule
	for _, s := range structs {
		r := raw{s}
		seen[s.Type] = true
		switch s.Type {
		case 0:
			hw.BIOS = store.BIOSInfo{Vendor: r.str(0x04), Version: r.str(0x05), ReleaseDate: r.str(0x08)}
		case 1:
			hw.System = decodeSystem(v, r)
		case 2:
			hw.Baseboard = store.BaseboardInfo{
				Manufacturer: r.str(0x04), Product: r.str(0x05), Version: r.str(0x06), SerialNumber: r.str(0x07),
				AssetTag: r.str(0x08), LocationInChassis: r.str(0x0A), BoardType: r.enum8(0x0D, BoardTypeName),
			}
		case 3:
			hw.Chassis = decodeChassis(r)
		case 4:
			if len(hw.Processors) == store.MaxProcessors {
				hw.Truncated.Processors++
				continue
			}
			hw.Processors = append(hw.Processors, decodeProcessor(r))
		case 7:
			if len(hw.Cache) < maxCaches {
				hw.Cache = append(hw.Cache, store.CacheInfo{SocketDesignation: r.str(0x04)})
			}
		case 8:
			if l := portLabel(r.str(0x04), r.str(0x06)); l != "" && len(hw.Ports) < maxPorts {
				hw.Ports = append(hw.Ports, l)
			}
		case 9:
			if d := r.str(0x04); d != "" && len(hw.Slots) < maxSlots {
				hw.Slots = append(hw.Slots, d)
			}
		case 11:
			for i := range s.Strings {
				if str := r.index(i + 1); str != "" && len(hw.OEMStrings) < maxOEMStrings {
					hw.OEMStrings = append(hw.OEMStrings, str)
				}
			}
		case 13:
			hw.BIOSLanguage = r.str(0x15)
		case 16:
			if len(arrays) == store.MaxMemoryArrays {
				hw.Truncated.MemoryArrays++
				continue
			}
			arrays = append(arrays, decodeArray(r))
		case 17:
			if len(modules) == store.MaxMemorySlots {
				hw.Truncated.MemorySlots++
				continue
			}
			modules = append(modules, decodeMemoryDevice(r))
		}
	}
	hw.Memory = memoryInfo(arrays, modules)
	hw.Availability = store.AvailOK
	for _, t := range []uint8{0, 1, 2, 3, 4, 16, 17} {
		if !seen[t] {
			hw.Availability = store.AvailPartial
		}
	}
	return hw
}

func decodeSystem(v SMBIOSVersion, r raw) store.SystemInfo {
	return store.SystemInfo{
		Manufacturer: r.str(0x04), ProductName: r.str(0x05), Version: r.str(0x06), SerialNumber: r.str(0x07),
		UUID: r.uuid(0x08, v), WakeUpType: r.enum8(0x18, WakeUpTypeName), SKUNumber: r.str(0x19), Family: r.str(0x1A),
	}
}

func decodeChassis(r raw) store.ChassisInfo {
	c := store.ChassisInfo{
		Manufacturer: r.str(0x04), Version: r.str(0x06), SerialNumber: r.str(0x07), AssetTag: r.str(0x08),
		BootupState: r.enum8(0x09, ChassisStateName),
	}
	if t, ok := r.byte(0x05); ok {
		c.Type = ChassisTypeName(uint16(t & 0x7F)) // bit 7 = chassis lock present
	}
	n, okN := r.byte(0x13)
	m, okM := r.byte(0x14)
	if okN && okM {
		c.SKUNumber = r.str(0x15 + int(n)*int(m))
	}
	return c
}

func decodeProcessor(r raw) store.Processor {
	p := store.Processor{
		SocketDesignation: r.str(0x04), Manufacturer: r.str(0x07), Version: r.str(0x10),
		SerialNumber: r.str(0x20), PartNumber: r.str(0x22),
		Type: r.enum8(0x05, ProcessorTypeName), Upgrade: r.enum8(0x19, ProcessorUpgradeName),
	}
	if w, ok := r.word(0x14); ok {
		p.MaxSpeedMHz = uint32(w)
	}
	if w, ok := r.word(0x16); ok {
		p.CurrentSpeedMHz = uint32(w)
	}
	if st, ok := r.byte(0x18); ok {
		p.SocketPopulated = st&0x40 != 0
	}
	if fam, ok := r.byte(0x06); ok {
		code := uint16(fam)
		if fam == 0xFE {
			if f2, ok2 := r.word(0x28); ok2 {
				code = f2
			}
		}
		p.Family = ProcessorFamilyName(code)
	}
	p.CoreCount = r.count(0x23, 0x2A)
	p.CoreEnabled = r.count(0x24, 0x2C)
	p.ThreadCount = r.count(0x25, 0x2E)
	return p
}

func decodeArray(r raw) store.MemoryArray {
	a := store.MemoryArray{
		Handle: uint32(r.s.Handle), Location: r.enum8(0x04, ArrayLocationName), Use: r.enum8(0x05, ArrayUseName),
		ErrorCorrection: r.enum8(0x06, ErrorCorrectionName),
	}
	if kb, ok := r.dword(0x07); ok {
		if kb == 0x80000000 {
			a.MaximumCapacity, _ = r.qword(0x0F)
		} else {
			a.MaximumCapacity = uint64(kb) * 1024
		}
	}
	if n, ok := r.word(0x0D); ok {
		a.NumberOfDevices = uint32(n)
	}
	return a
}

func decodeMemoryDevice(r raw) store.MemoryModule {
	m := store.MemoryModule{DeviceLocator: r.str(0x10), BankLocator: r.str(0x11)}
	if h, ok := r.word(0x04); ok {
		m.ArrayHandle = uint32(h)
	}
	size, ok := r.word(0x0C)
	if !ok || size == 0 {
		return m // empty slot (or size not reported): locators only
	}
	m.Populated = true
	switch {
	case size == 0xFFFF: // unknown size
	case size == 0x7FFF:
		if ext, ok := r.dword(0x1C); ok {
			m.CapacityBytes = uint64(ext&0x7FFFFFFF) << 20
		}
	case size&0x8000 != 0:
		m.CapacityBytes = uint64(size&0x7FFF) << 10
	default:
		m.CapacityBytes = uint64(size) << 20
	}
	m.FormFactor = r.enum8(0x0E, FormFactorName)
	m.MemoryType = r.enum8(0x12, MemoryTypeName)
	if td, ok := r.word(0x13); ok {
		m.TypeDetail = TypeDetailNames(td)
	}
	m.SpeedMTs = r.speed(0x15, 0x54)
	m.ConfiguredSpeedMTs = r.speed(0x20, 0x58)
	m.Manufacturer = r.str(0x17)
	m.SerialNumber = r.str(0x18)
	m.AssetTag = r.str(0x19)
	m.PartNumber = r.str(0x1A)
	if at, ok := r.byte(0x1B); ok {
		m.RankCount = uint32(at & 0x0F)
	}
	return m
}

// memoryInfo derives the primary array (first of use "System memory", else
// the first), slot counts and the total of populated modules in "System
// memory" arrays (every populated module when no such array is reported).
func memoryInfo(arrays []store.MemoryArray, modules []store.MemoryModule) store.MemoryInfo {
	info := store.MemoryInfo{Arrays: arrays, Modules: modules, SlotsTotal: uint32(len(modules))} // #nosec G115 -- <= MaxMemorySlots
	system := map[uint32]bool{}
	for _, a := range arrays {
		if a.Use == ArrayUseName(0x03) {
			if len(system) == 0 {
				info.Array = a
			}
			system[a.Handle] = true
		}
	}
	if len(system) == 0 && len(arrays) > 0 {
		info.Array = arrays[0]
	}
	for _, m := range modules {
		if !m.Populated {
			continue
		}
		info.SlotsPopulated++
		if len(system) == 0 || system[m.ArrayHandle] {
			info.TotalPhysicalBytes += m.CapacityBytes
		}
	}
	return info
}

// portLabel builds a port connector designation, preferring "internal /
// external" and falling back to whichever exists.
func portLabel(in, ext string) string {
	switch {
	case in != "" && ext != "":
		return in + " / " + ext
	case ext != "":
		return ext
	default:
		return in
	}
}

// raw reads fields of one structure by their DSP0134 offset (from the start
// of the structure, header included). A field is available only when it lies
// completely within both the header length and the formatted bytes.
type raw struct{ s SMBIOSStructure }

func (r raw) field(off, n int) ([]byte, bool) {
	if off < 4 || off+n > int(r.s.Length) || off-4+n > len(r.s.Formatted) {
		return nil, false
	}
	return r.s.Formatted[off-4 : off-4+n], true
}

func (r raw) byte(off int) (uint8, bool) {
	b, ok := r.field(off, 1)
	if !ok {
		return 0, false
	}
	return b[0], true
}

func (r raw) word(off int) (uint16, bool) {
	b, ok := r.field(off, 2)
	if !ok {
		return 0, false
	}
	return binary.LittleEndian.Uint16(b), true
}

func (r raw) dword(off int) (uint32, bool) {
	b, ok := r.field(off, 4)
	if !ok {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b), true
}

func (r raw) qword(off int) (uint64, bool) {
	b, ok := r.field(off, 8)
	if !ok {
		return 0, false
	}
	return binary.LittleEndian.Uint64(b), true
}

// str returns the string referenced by the index byte at off.
func (r raw) str(off int) string {
	i, ok := r.byte(off)
	if !ok {
		return ""
	}
	return r.index(int(i))
}

// index returns string number i (1-based) of the string set, trimmed, "" for
// index 0, a missing string, invalid UTF-8 or the "To be filled by O.E.M."
// placeholder (the go-smbios semantics the stored values already follow),
// clipped to maxSMBIOSString bytes.
func (r raw) index(i int) string {
	if i < 1 || i > len(r.s.Strings) {
		return ""
	}
	s := strings.TrimSpace(r.s.Strings[i-1])
	if !utf8.ValidString(s) || strings.EqualFold(s, "to be filled by o.e.m.") {
		return ""
	}
	return clipString(s, maxSMBIOSString)
}

// enum8 decodes the byte at off through name; "" when not available.
func (r raw) enum8(off int, name func(uint16) string) string {
	b, ok := r.byte(off)
	if !ok {
		return ""
	}
	return name(uint16(b))
}

// count reads a BYTE count at off, switching to the WORD count at off2 when
// the byte is FFh (DSP0134 core/thread count 2).
func (r raw) count(off, off2 int) uint32 {
	b, ok := r.byte(off)
	if !ok {
		return 0
	}
	if b == 0xFF {
		if w, ok2 := r.word(off2); ok2 {
			return uint32(w)
		}
	}
	return uint32(b)
}

// speed reads a WORD speed in MT/s at off (0 = unknown), switching to the
// DWORD extended speed at ext when the word is FFFFh.
func (r raw) speed(off, ext int) uint32 {
	w, ok := r.word(off)
	if !ok {
		return 0
	}
	if w == 0xFFFF {
		e, _ := r.dword(ext)
		return e & 0x7FFFFFFF
	}
	return uint32(w)
}

// uuid formats the 16-byte system UUID at off like go-smbios did: the first
// three fields little-endian from SMBIOS 2.6 on, raw byte order before.
func (r raw) uuid(off int, v SMBIOSVersion) string {
	b, ok := r.field(off, 16)
	if !ok {
		return ""
	}
	u := make([]byte, 16)
	copy(u, b)
	if v.atLeast(2, 6) {
		u[0], u[1], u[2], u[3] = b[3], b[2], b[1], b[0]
		u[4], u[5] = b[5], b[4]
		u[6], u[7] = b[7], b[6]
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// clipString shortens s to at most n bytes without splitting a rune.
func clipString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func lookup(table map[uint16]string, code uint16) string {
	if n, ok := table[code]; ok {
		return n
	}
	return fmt.Sprintf("Unknown (code %d)", code)
}

// MemoryTypeName is DSP0134 7.18.2 (memory device type).
func MemoryTypeName(code uint16) string { return lookup(memoryTypes, code) }

// FormFactorName is DSP0134 7.18.1 (memory device form factor).
func FormFactorName(code uint16) string { return lookup(formFactors, code) }

// ArrayLocationName is DSP0134 7.17.1 (physical memory array location).
func ArrayLocationName(code uint16) string { return lookup(arrayLocations, code) }

// ArrayUseName is DSP0134 7.17.2 (physical memory array use).
func ArrayUseName(code uint16) string { return lookup(arrayUses, code) }

// ErrorCorrectionName is DSP0134 7.17.3 (memory error correction).
func ErrorCorrectionName(code uint16) string { return lookup(errorCorrections, code) }

// ChassisTypeName is DSP0134 7.4.1 (bit 7, the lock flag, masked off by the caller).
func ChassisTypeName(code uint16) string { return lookup(chassisTypes, code) }

// ChassisStateName is DSP0134 7.4.2 (boot-up, power supply and thermal state).
func ChassisStateName(code uint16) string { return lookup(chassisStates, code) }

// ProcessorTypeName is DSP0134 7.5.1.
func ProcessorTypeName(code uint16) string { return lookup(processorTypes, code) }

// ProcessorFamilyName is DSP0134 7.5.2 (family and family 2).
func ProcessorFamilyName(code uint16) string { return lookup(processorFamilies, code) }

// ProcessorUpgradeName is DSP0134 7.5.5 (processor socket).
func ProcessorUpgradeName(code uint16) string { return lookup(processorUpgrades, code) }

// BoardTypeName is DSP0134 7.3.2 (note: 01h is Unknown, 02h Other).
func BoardTypeName(code uint16) string { return lookup(boardTypes, code) }

// WakeUpTypeName is DSP0134 7.2.2 (00h is reserved).
func WakeUpTypeName(code uint16) string { return lookup(wakeUpTypes, code) }

// typeDetailBits is DSP0134 7.18.3 (memory device type detail, a WORD; bit 0
// is reserved).
var typeDetailBits = [16]string{"", "Other", "Unknown", "Fast-paged", "Static column", "Pseudo-static", "RAMBUS",
	"Synchronous", "CMOS", "EDO", "Window DRAM", "Cache DRAM", "Non-volatile", "Registered (Buffered)",
	"Unbuffered (Unregistered)", "LRDIMM"}

// TypeDetailNames lists the names of the bits set in a type-detail WORD, in
// bit order; nil when none is set.
func TypeDetailNames(w uint16) []string {
	var out []string
	for bit := 1; bit < 16; bit++ {
		if w&(1<<bit) != 0 {
			out = append(out, typeDetailBits[bit])
		}
	}
	return out
}

var memoryTypes = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "DRAM", 0x04: "EDRAM", 0x05: "VRAM", 0x06: "SRAM", 0x07: "RAM", 0x08: "ROM",
	0x09: "FLASH", 0x0A: "EEPROM", 0x0B: "FEPROM", 0x0C: "EPROM", 0x0D: "CDRAM", 0x0E: "3DRAM", 0x0F: "SDRAM",
	0x10: "SGRAM", 0x11: "RDRAM", 0x12: "DDR", 0x13: "DDR2", 0x14: "DDR2 FB-DIMM",
	// 15h-17h are reserved.
	0x18: "DDR3", 0x19: "FBD2", 0x1A: "DDR4", 0x1B: "LPDDR", 0x1C: "LPDDR2", 0x1D: "LPDDR3", 0x1E: "LPDDR4",
	0x1F: "Logical non-volatile device", 0x20: "HBM", 0x21: "HBM2", 0x22: "DDR5", 0x23: "LPDDR5", 0x24: "HBM3",
}

var formFactors = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "SIMM", 0x04: "SIP", 0x05: "Chip", 0x06: "DIP", 0x07: "ZIP",
	0x08: "Proprietary Card", 0x09: "DIMM", 0x0A: "TSOP", 0x0B: "Row of chips", 0x0C: "RIMM", 0x0D: "SODIMM",
	0x0E: "SRIMM", 0x0F: "FB-DIMM", 0x10: "Die", 0x11: "CAMM",
}

var arrayLocations = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "System board or motherboard", 0x04: "ISA add-on card",
	0x05: "EISA add-on card", 0x06: "PCI add-on card", 0x07: "MCA add-on card", 0x08: "PCMCIA add-on card",
	0x09: "Proprietary add-on card", 0x0A: "NuBus", 0xA0: "PC-98/C20 add-on card", 0xA1: "PC-98/C24 add-on card",
	0xA2: "PC-98/E add-on card", 0xA3: "PC-98/Local bus add-on card", 0xA4: "CXL add-on card",
}

var arrayUses = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "System memory", 0x04: "Video memory", 0x05: "Flash memory",
	0x06: "Non-volatile RAM", 0x07: "Cache memory",
}

var errorCorrections = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "None", 0x04: "Parity", 0x05: "Single-bit ECC", 0x06: "Multi-bit ECC", 0x07: "CRC",
}

var chassisTypes = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "Desktop", 0x04: "Low Profile Desktop", 0x05: "Pizza Box", 0x06: "Mini Tower",
	0x07: "Tower", 0x08: "Portable", 0x09: "Laptop", 0x0A: "Notebook", 0x0B: "Hand Held", 0x0C: "Docking Station",
	0x0D: "All in One", 0x0E: "Sub Notebook", 0x0F: "Space-saving", 0x10: "Lunch Box", 0x11: "Main Server Chassis",
	0x12: "Expansion Chassis", 0x13: "SubChassis", 0x14: "Bus Expansion Chassis", 0x15: "Peripheral Chassis",
	0x16: "RAID Chassis", 0x17: "Rack Mount Chassis", 0x18: "Sealed-case PC", 0x19: "Multi-system chassis",
	0x1A: "Compact PCI", 0x1B: "Advanced TCA", 0x1C: "Blade", 0x1D: "Blade Enclosure", 0x1E: "Tablet",
	0x1F: "Convertible", 0x20: "Detachable", 0x21: "IoT Gateway", 0x22: "Embedded PC", 0x23: "Mini PC", 0x24: "Stick PC",
}

var chassisStates = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "Safe", 0x04: "Warning", 0x05: "Critical", 0x06: "Non-recoverable",
}

var processorTypes = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "Central Processor", 0x04: "Math Processor", 0x05: "DSP Processor", 0x06: "Video Processor",
}

var boardTypes = map[uint16]string{
	0x01: "Unknown", 0x02: "Other", 0x03: "Server Blade", 0x04: "Connectivity Switch", 0x05: "System Management Module",
	0x06: "Processor Module", 0x07: "I/O Module", 0x08: "Memory Module", 0x09: "Daughter board",
	0x0A: "Motherboard (includes processor, memory, and I/O)", 0x0B: "Processor/Memory Module",
	0x0C: "Processor/IO Module", 0x0D: "Interconnect board",
}

var wakeUpTypes = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "APM Timer", 0x04: "Modem Ring", 0x05: "LAN Remote", 0x06: "Power Switch",
	0x07: "PCI PME#", 0x08: "AC Power Restored",
}

var processorUpgrades = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "Daughter Board", 0x04: "ZIF Socket", 0x05: "Replaceable Piggy Back",
	0x06: "None", 0x07: "LIF Socket", 0x08: "Slot 1", 0x09: "Slot 2", 0x0A: "370-pin socket", 0x0B: "Slot A",
	0x0C: "Slot M", 0x0D: "Socket 423", 0x0E: "Socket A (Socket 462)", 0x0F: "Socket 478", 0x10: "Socket 754",
	0x11: "Socket 940", 0x12: "Socket 939", 0x13: "Socket mPGA604", 0x14: "Socket LGA771", 0x15: "Socket LGA775",
	0x16: "Socket S1", 0x17: "Socket AM2", 0x18: "Socket F (1207)", 0x19: "Socket LGA1366", 0x1A: "Socket G34",
	0x1B: "Socket AM3", 0x1C: "Socket C32", 0x1D: "Socket LGA1156", 0x1E: "Socket LGA1567", 0x1F: "Socket PGA988A",
	0x20: "Socket BGA1288", 0x21: "Socket rPGA988B", 0x22: "Socket BGA1023", 0x23: "Socket BGA1224",
	0x24: "Socket LGA1155", 0x25: "Socket LGA1356", 0x26: "Socket LGA2011", 0x27: "Socket FS1", 0x28: "Socket FS2",
	0x29: "Socket FM1", 0x2A: "Socket FM2", 0x2B: "Socket LGA2011-3", 0x2C: "Socket LGA1356-3", 0x2D: "Socket LGA1150",
	0x2E: "Socket BGA1168", 0x2F: "Socket BGA1234", 0x30: "Socket BGA1364", 0x31: "Socket AM4", 0x32: "Socket LGA1151",
	0x33: "Socket BGA1356", 0x34: "Socket BGA1440", 0x35: "Socket BGA1515", 0x36: "Socket LGA3647-1", 0x37: "Socket SP3",
	0x38: "Socket SP3r2", 0x39: "Socket LGA2066", 0x3A: "Socket BGA1392", 0x3B: "Socket BGA1510", 0x3C: "Socket BGA1528",
	0x3D: "Socket LGA4189", 0x3E: "Socket LGA1200", 0x3F: "Socket LGA4677", 0x40: "Socket LGA1700",
	0x41: "Socket BGA1744", 0x42: "Socket BGA1781", 0x43: "Socket BGA1211", 0x44: "Socket BGA2422",
	0x45: "Socket LGA1211", 0x46: "Socket LGA2422", 0x47: "Socket LGA5773", 0x48: "Socket BGA5773",
	0x49: "Socket AM5", 0x4A: "Socket SP5", 0x4B: "Socket SP6", 0x4C: "Socket BGA883", 0x4D: "Socket BGA1190",
	0x4E: "Socket BGA4129", 0x4F: "Socket LGA4710", 0x50: "Socket LGA7529",
}

// processorFamilies is DSP0134 7.5.2 including the Processor Family 2 codes
// above FFh. FEh is the indicator to read Processor Family 2 and has no name
// of its own.
var processorFamilies = map[uint16]string{
	0x01: "Other", 0x02: "Unknown", 0x03: "8086", 0x04: "80286", 0x05: "Intel386 processor", 0x06: "Intel486 processor",
	0x07: "8087", 0x08: "80287", 0x09: "80387", 0x0A: "80487", 0x0B: "Intel Pentium processor", 0x0C: "Pentium Pro processor",
	0x0D: "Pentium II processor", 0x0E: "Pentium processor with MMX technology", 0x0F: "Intel Celeron processor",
	0x10: "Pentium II Xeon processor", 0x11: "Pentium III processor", 0x12: "M1 Family", 0x13: "M2 Family",
	0x14: "Intel Celeron M processor", 0x15: "Intel Pentium 4 HT processor", 0x16: "Intel Processor",
	0x18: "AMD Duron Processor Family", 0x19: "K5 Family", 0x1A: "K6 Family", 0x1B: "K6-2", 0x1C: "K6-3",
	0x1D: "AMD Athlon Processor Family", 0x1E: "AMD29000 Family", 0x1F: "K6-2+",
	0x20: "Power PC Family", 0x21: "Power PC 601", 0x22: "Power PC 603", 0x23: "Power PC 603+", 0x24: "Power PC 604",
	0x25: "Power PC 620", 0x26: "Power PC x704", 0x27: "Power PC 750", 0x28: "Intel Core Duo processor",
	0x29: "Intel Core Duo mobile processor", 0x2A: "Intel Core Solo mobile processor", 0x2B: "Intel Atom processor",
	0x2C: "Intel Core M processor", 0x2D: "Intel Core m3 processor", 0x2E: "Intel Core m5 processor", 0x2F: "Intel Core m7 processor",
	0x30: "Alpha Family", 0x31: "Alpha 21064", 0x32: "Alpha 21066", 0x33: "Alpha 21164", 0x34: "Alpha 21164PC",
	0x35: "Alpha 21164a", 0x36: "Alpha 21264", 0x37: "Alpha 21364",
	0x38: "AMD Turion II Ultra Dual-Core Mobile M Processor Family", 0x39: "AMD Turion II Dual-Core Mobile M Processor Family",
	0x3A: "AMD Athlon II Dual-Core M Processor Family", 0x3B: "AMD Opteron 6100 Series Processor",
	0x3C: "AMD Opteron 4100 Series Processor", 0x3D: "AMD Opteron 6200 Series Processor",
	0x3E: "AMD Opteron 4200 Series Processor", 0x3F: "AMD FX Series Processor",
	0x40: "MIPS Family", 0x41: "MIPS R4000", 0x42: "MIPS R4200", 0x43: "MIPS R4400", 0x44: "MIPS R4600", 0x45: "MIPS R10000",
	0x46: "AMD C-Series Processor", 0x47: "AMD E-Series Processor", 0x48: "AMD A-Series Processor",
	0x49: "AMD G-Series Processor", 0x4A: "AMD Z-Series Processor", 0x4B: "AMD R-Series Processor",
	0x4C: "AMD Opteron 4300 Series Processor", 0x4D: "AMD Opteron 6300 Series Processor",
	0x4E: "AMD Opteron 3300 Series Processor", 0x4F: "AMD FirePro Series Processor",
	0x50: "SPARC Family", 0x51: "SuperSPARC", 0x52: "microSPARC II", 0x53: "microSPARC IIep", 0x54: "UltraSPARC",
	0x55: "UltraSPARC II", 0x56: "UltraSPARC Iii", 0x57: "UltraSPARC III", 0x58: "UltraSPARC IIIi",
	0x60: "68040 Family", 0x61: "68xxx", 0x62: "68000", 0x63: "68010", 0x64: "68020", 0x65: "68030",
	0x66: "AMD Athlon X4 Quad-Core Processor Family", 0x67: "AMD Opteron X1000 Series Processor",
	0x68: "AMD Opteron X2000 Series APU", 0x69: "AMD Opteron A-Series Processor", 0x6A: "AMD Opteron X3000 Series APU",
	0x6B: "AMD Zen Processor Family",
	0x70: "Hobbit Family",
	0x78: "Crusoe TM5000 Family", 0x79: "Crusoe TM3000 Family", 0x7A: "Efficeon TM8000 Family",
	0x80: "Weitek",
	0x82: "Itanium processor", 0x83: "AMD Athlon 64 Processor Family", 0x84: "AMD Opteron Processor Family",
	0x85: "AMD Sempron Processor Family", 0x86: "AMD Turion 64 Mobile Technology", 0x87: "Dual-Core AMD Opteron Processor Family",
	0x88: "AMD Athlon 64 X2 Dual-Core Processor Family", 0x89: "AMD Turion 64 X2 Mobile Technology",
	0x8A: "Quad-Core AMD Opteron Processor Family", 0x8B: "Third-Generation AMD Opteron Processor Family",
	0x8C: "AMD Phenom FX Quad-Core Processor Family", 0x8D: "AMD Phenom X4 Quad-Core Processor Family",
	0x8E: "AMD Phenom X2 Dual-Core Processor Family", 0x8F: "AMD Athlon X2 Dual-Core Processor Family",
	0x90: "PA-RISC Family", 0x91: "PA-RISC 8500", 0x92: "PA-RISC 8000", 0x93: "PA-RISC 7300LC", 0x94: "PA-RISC 7200",
	0x95: "PA-RISC 7100LC", 0x96: "PA-RISC 7100",
	0xA0: "V30 Family", 0xA1: "Quad-Core Intel Xeon processor 3200 Series", 0xA2: "Dual-Core Intel Xeon processor 3000 Series",
	0xA3: "Quad-Core Intel Xeon processor 5300 Series", 0xA4: "Dual-Core Intel Xeon processor 5100 Series",
	0xA5: "Dual-Core Intel Xeon processor 5000 Series", 0xA6: "Dual-Core Intel Xeon processor LV",
	0xA7: "Dual-Core Intel Xeon processor ULV", 0xA8: "Dual-Core Intel Xeon processor 7100 Series",
	0xA9: "Quad-Core Intel Xeon processor 5400 Series", 0xAA: "Quad-Core Intel Xeon processor",
	0xAB: "Dual-Core Intel Xeon processor 5200 Series", 0xAC: "Dual-Core Intel Xeon processor 7200 Series",
	0xAD: "Quad-Core Intel Xeon processor 7300 Series", 0xAE: "Quad-Core Intel Xeon processor 7400 Series",
	0xAF: "Multi-Core Intel Xeon processor 7400 Series",
	0xB0: "Pentium III Xeon processor", 0xB1: "Pentium III Processor with Intel SpeedStep Technology",
	0xB2: "Pentium 4 Processor", 0xB3: "Intel Xeon processor", 0xB4: "AS400 Family", 0xB5: "Intel Xeon processor MP",
	0xB6: "AMD Athlon XP Processor Family", 0xB7: "AMD Athlon MP Processor Family", 0xB8: "Intel Itanium 2 processor",
	0xB9: "Intel Pentium M processor", 0xBA: "Intel Celeron D processor", 0xBB: "Intel Pentium D processor",
	0xBC: "Intel Pentium Processor Extreme Edition", 0xBD: "Intel Core Solo Processor", 0xBF: "Intel Core 2 Duo Processor",
	0xC0: "Intel Core 2 Solo processor", 0xC1: "Intel Core 2 Extreme processor", 0xC2: "Intel Core 2 Quad processor",
	0xC3: "Intel Core 2 Extreme mobile processor", 0xC4: "Intel Core 2 Duo mobile processor",
	0xC5: "Intel Core 2 Solo mobile processor", 0xC6: "Intel Core i7 processor", 0xC7: "Dual-Core Intel Celeron processor",
	0xC8: "IBM390 Family", 0xC9: "G4", 0xCA: "G5", 0xCB: "ESA/390 G6", 0xCC: "z/Architecture base",
	0xCD: "Intel Core i5 processor", 0xCE: "Intel Core i3 processor", 0xCF: "Intel Core i9 processor",
	0xD2: "VIA C7-M Processor Family", 0xD3: "VIA C7-D Processor Family", 0xD4: "VIA C7 Processor Family",
	0xD5: "VIA Eden Processor Family", 0xD6: "Multi-Core Intel Xeon processor", 0xD7: "Dual-Core Intel Xeon processor 3xxx Series",
	0xD8: "Quad-Core Intel Xeon processor 3xxx Series", 0xD9: "VIA Nano Processor Family",
	0xDA: "Dual-Core Intel Xeon processor 5xxx Series", 0xDB: "Quad-Core Intel Xeon processor 5xxx Series",
	0xDD: "Dual-Core Intel Xeon processor 7xxx Series", 0xDE: "Quad-Core Intel Xeon processor 7xxx Series",
	0xDF: "Multi-Core Intel Xeon processor 7xxx Series", 0xE0: "Multi-Core Intel Xeon processor 3400 Series",
	0xE4: "AMD Opteron 3000 Series Processor", 0xE5: "AMD Sempron II Processor",
	0xE6: "Embedded AMD Opteron Quad-Core Processor Family", 0xE7: "AMD Phenom Triple-Core Processor Family",
	0xE8: "AMD Turion Ultra Dual-Core Mobile Processor Family", 0xE9: "AMD Turion Dual-Core Mobile Processor Family",
	0xEA: "AMD Athlon Dual-Core Processor Family", 0xEB: "AMD Sempron SI Processor Family",
	0xEC: "AMD Phenom II Processor Family", 0xED: "AMD Athlon II Processor Family",
	0xEE: "Six-Core AMD Opteron Processor Family", 0xEF: "AMD Sempron M Processor Family",
	0xFA: "i860", 0xFB: "i960",
	// Processor Family 2 (read when the family byte is FEh).
	0x100: "ARMv7", 0x101: "ARMv8", 0x102: "ARMv9", 0x104: "SH-3", 0x105: "SH-4", 0x118: "ARM", 0x119: "StrongARM",
	0x12C: "6x86", 0x12D: "MediaGX", 0x12E: "MII", 0x140: "WinChip", 0x15E: "DSP", 0x1F4: "Video Processor",
	0x200: "RISC-V RV32", 0x201: "RISC-V RV64", 0x202: "RISC-V RV128",
	0x258: "LoongArch", 0x259: "Loongson 1 Processor Family", 0x25A: "Loongson 2 Processor Family",
	0x25B: "Loongson 3 Processor Family", 0x25C: "Loongson 2K Processor Family", 0x25D: "Loongson 3A Processor Family",
	0x25E: "Loongson 3B Processor Family", 0x25F: "Loongson 3C Processor Family", 0x260: "Loongson 3D Processor Family",
	0x261: "Loongson 3E Processor Family", 0x262: "Dual-Core Loongson 2K Processor 2xxx Series",
	0x26C: "Quad-Core Loongson 3A Processor 5xxx Series", 0x26D: "Multi-Core Loongson 3A Processor 5xxx Series",
	0x26E: "Quad-Core Loongson 3B Processor 5xxx Series", 0x26F: "Multi-Core Loongson 3B Processor 5xxx Series",
	0x270: "Multi-Core Loongson 3C Processor 5xxx Series", 0x271: "Multi-Core Loongson 3D Processor 5xxx Series",
	0x300: "Intel Core 3", 0x301: "Intel Core 5", 0x302: "Intel Core 7", 0x303: "Intel Core 9",
	0x304: "Intel Core Ultra 3", 0x305: "Intel Core Ultra 5", 0x306: "Intel Core Ultra 7", 0x307: "Intel Core Ultra 9",
}
