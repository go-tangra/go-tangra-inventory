package agentfacts

import (
	"encoding/binary"
	"fmt"
)

// Synthetic SMBIOS tables built from the DSP0134 structure layouts (feature
// 023, T005/T006). They are encoded as raw structure tables — exactly what
// /sys/firmware/dmi/tables/DMI holds — so the corpus test runs them through
// go-smbios (table splitting) and the agent decoder end to end. node-1
// reproduces the production host of the bug report (2x Xeon Silver 4310,
// 16x 16 GB Samsung M393A2K43EB3 DDR4 RDIMM 3200 MT/s configured 2666,
// System board / System memory / Single-bit ECC, 12 TB maximum, AMI BIOS 2.5,
// Supermicro Super Server) with synthetic serial numbers; no production host
// was accessed. Real captures (scripts/capture-smbios.sh) can be added next
// to them as <name>.bin + <name>.ep.

type fixture struct {
	name    string
	version SMBIOSVersion
	build   func() []rawStruct
}

var fixtures = []fixture{
	{"node-1", SMBIOSVersion{3, 3}, node1Table},
	{"qemu-vm", SMBIOSVersion{2, 8}, qemuTable},
	{"hyperv-vm", SMBIOSVersion{2, 3}, hypervTable},
	{"desktop", SMBIOSVersion{3, 4}, desktopTable},
}

// rawStruct is a structure under construction: fields are written at their
// DSP0134 offsets.
type rawStruct struct {
	typ     uint8
	handle  uint16
	b       []byte // whole structure incl. the 4-byte header area
	strings []string
}

func newStruct(typ uint8, length int, handle uint16) *rawStruct {
	return &rawStruct{typ: typ, handle: handle, b: make([]byte, length)}
}

func (r *rawStruct) u8(off int, v uint8) *rawStruct   { r.b[off] = v; return r }
func (r *rawStruct) u16(off int, v uint16) *rawStruct { binary.LittleEndian.PutUint16(r.b[off:], v); return r }
func (r *rawStruct) u32(off int, v uint32) *rawStruct { binary.LittleEndian.PutUint32(r.b[off:], v); return r }
func (r *rawStruct) u64(off int, v uint64) *rawStruct { binary.LittleEndian.PutUint64(r.b[off:], v); return r }
func (r *rawStruct) bytes(off int, v []byte) *rawStruct {
	copy(r.b[off:], v)
	return r
}

// s adds a string and stores its 1-based index at off ("" stores index 0).
func (r *rawStruct) s(off int, v string) *rawStruct {
	if v == "" {
		r.b[off] = 0
		return r
	}
	r.strings = append(r.strings, v)
	r.b[off] = uint8(len(r.strings))
	return r
}

// encode serialises structures into a DMI table ending with type 127.
func encode(structs []rawStruct) []byte {
	var out []byte
	for _, r := range append(structs, *newStruct(127, 4, 0xFEFF)) {
		b := append([]byte(nil), r.b...)
		b[0], b[1] = r.typ, uint8(len(b))
		binary.LittleEndian.PutUint16(b[2:], r.handle)
		out = append(out, b...)
		if len(r.strings) == 0 {
			out = append(out, 0, 0)
			continue
		}
		for _, s := range r.strings {
			out = append(append(out, s...), 0)
		}
		out = append(out, 0)
	}
	return out
}

// entryPoint returns a minimal entry point carrying the version.
func entryPoint(v SMBIOSVersion) []byte {
	if v.Major >= 3 {
		ep := make([]byte, 0x18)
		copy(ep, "_SM3_")
		ep[6], ep[7], ep[8], ep[10] = 0x18, uint8(v.Major), uint8(v.Minor), 1
		return ep
	}
	ep := make([]byte, 0x1F)
	copy(ep, "_SM_")
	ep[5], ep[6], ep[7] = 0x1F, uint8(v.Major), uint8(v.Minor)
	copy(ep[0x10:], "_DMI_")
	return ep
}

func node1Table() []rawStruct {
	var t []rawStruct
	t = append(t, *newStruct(0, 0x1A, 0x0000).
		s(0x04, "American Megatrends International, LLC.").s(0x05, "2.5").u16(0x06, 0xF000).
		s(0x08, "11/26/2025").u8(0x09, 0xFF).u64(0x0A, 0x08000000_1BDD9880).u16(0x12, 0x0D03).u8(0x14, 5).u8(0x15, 32).u8(0x16, 0xFF).u8(0x17, 0xFF))
	t = append(t, *newStruct(1, 0x1B, 0x0001).
		s(0x04, "Supermicro").s(0x05, "Super Server").s(0x06, "0123456789").s(0x07, "A000000000001").
		bytes(0x08, []byte{0x00, 0x69, 0x8b, 0x3c, 0x4a, 0x1e, 0xc5, 0x11, 0x80, 0x00, 0x3c, 0xec, 0xef, 0x00, 0x00, 0x01}).
		u8(0x18, 0x06).s(0x19, "To be filled by O.E.M.").s(0x1A, "To be filled by O.E.M."))
	t = append(t, *newStruct(2, 0x0F, 0x0002).
		s(0x04, "Supermicro").s(0x05, "X12DPi-NT6").s(0x06, "1.02").s(0x07, "BB000000000001").s(0x08, "Base Board Asset Tagging").
		u8(0x09, 0x09).s(0x0A, "Part Component").u16(0x0B, 0x0003).u8(0x0D, 0x0A))
	t = append(t, *newStruct(3, 0x16, 0x0003).
		s(0x04, "Supermicro").u8(0x05, 0x17).s(0x06, "0123456789").s(0x07, "C000000000001").s(0x08, "Chassis Asset Tag").
		u8(0x09, 0x03).u8(0x0A, 0x03).u8(0x0B, 0x03).u8(0x0C, 0x03).u8(0x12, 1).s(0x15, "To be filled by O.E.M."))
	for i, sock := range []string{"CPU1", "CPU2"} {
		t = append(t, *newStruct(4, 0x30, uint16(0x0010+i)).
			s(0x04, sock).u8(0x05, 0x03).u8(0x06, 0xB3).s(0x07, "Intel(R) Corporation").u64(0x08, 0xBFEBFBFF000606A6).
			s(0x10, "Intel(R) Xeon(R) Silver 4310 CPU @ 2.10GHz").u8(0x11, 0x90).u16(0x12, 100).u16(0x14, 4000).u16(0x16, 2100).
			u8(0x18, 0x41).u8(0x19, 0x3D).u16(0x1A, uint16(0x0020+3*i)).u16(0x1C, uint16(0x0021+3*i)).u16(0x1E, uint16(0x0022+3*i)).
			s(0x20, "").s(0x21, fmt.Sprintf("%s AssetTag", sock)).s(0x22, "").
			u8(0x23, 12).u8(0x24, 12).u8(0x25, 24).u16(0x26, 0x00FC).u16(0x28, 0xB3).u16(0x2A, 12).u16(0x2C, 12).u16(0x2E, 24))
	}
	for i, level := range []string{"L1", "L2", "L3", "L1", "L2", "L3"} {
		t = append(t, *newStruct(7, 0x1B, uint16(0x0020+i)).s(0x04, level+"-Cache"))
	}
	t = append(t, *newStruct(8, 0x09, 0x0030).s(0x04, "JUSB1").u8(0x05, 0x12).s(0x06, "Rear USB 3.0").u8(0x07, 0x12).u8(0x08, 0x10))
	t = append(t, *newStruct(9, 0x11, 0x0031).s(0x04, "CPU1 SLOT1 PCI-E 4.0 X16").u8(0x05, 0xB6).u8(0x06, 0x0D))
	t = append(t, *newStruct(11, 0x05, 0x0032).u8(0x04, 2))
	t[len(t)-1].strings = []string{"Intel Ice Lake/Whitley", "Supermicro motherboard-X12 Series"}
	t = append(t, *newStruct(13, 0x16, 0x0033).u8(0x04, 1).u8(0x05, 0x01).s(0x15, "en|US|iso8859-1"))
	t = append(t, *newStruct(16, 0x17, 0x0040).
		u8(0x04, 0x03).u8(0x05, 0x03).u8(0x06, 0x05).u32(0x07, 0x80000000).u16(0x0B, 0xFFFE).u16(0x0D, 16).u64(0x0F, 12<<40))
	for i := 0; i < 16; i++ {
		cpu, ch := i/8, i%8
		loc := fmt.Sprintf("P%d-DIMM%c1", cpu+1, 'A'+ch)
		bank := fmt.Sprintf("P%d_Node%d_Channel%d_Dimm0", cpu, cpu, ch)
		t = append(t, *newStruct(17, 0x5C, uint16(0x0041+i)).
			u16(0x04, 0x0040).u16(0x06, 0xFFFE).u16(0x08, 72).u16(0x0A, 64).u16(0x0C, 16384).u8(0x0E, 0x09).
			s(0x10, loc).s(0x11, bank).u8(0x12, 0x1A).u16(0x13, 0x2080).u16(0x15, 3200).
			s(0x17, "Samsung").s(0x18, fmt.Sprintf("%08X", 0x10000001+i)).s(0x19, loc+"_AssetTag (date:21/07)").
			s(0x1A, "M393A2K43EB3-CWE    ").u8(0x1B, 0x02).u32(0x1C, 0).u16(0x20, 2666).u16(0x22, 1200).u16(0x24, 1200).
			u16(0x26, 1200).u8(0x28, 0x03).u16(0x29, 0x0004).u16(0x2C, 0xCE00).u16(0x2E, 0).u16(0x30, 0).u16(0x32, 0).
			u64(0x3C, 16<<30))
	}
	return t
}

func qemuTable() []rawStruct {
	t := []rawStruct{
		*newStruct(0, 0x18, 0x0000).s(0x04, "SeaBIOS").s(0x05, "1.16.3-debian-1.16.3-2").u16(0x06, 0xE800).s(0x08, "04/01/2014").u8(0x14, 0).u8(0x15, 0),
		*newStruct(1, 0x1B, 0x0100).s(0x04, "QEMU").s(0x05, "Standard PC (Q35 + ICH9, 2009)").s(0x06, "pc-q35-8.2").
			bytes(0x08, []byte{0x5c, 0x7a, 0x1f, 0x40, 0x0b, 0x2d, 0x4e, 0x44, 0x9f, 0x1b, 0x6f, 0x55, 0x2a, 0x10, 0x31, 0xe7}).u8(0x18, 0x06),
		*newStruct(3, 0x16, 0x0300).s(0x04, "QEMU").u8(0x05, 0x01).s(0x06, "pc-q35-8.2").u8(0x09, 0x03).u8(0x0A, 0x03).u8(0x0B, 0x03).u8(0x0C, 0x02),
		*newStruct(4, 0x30, 0x0400).s(0x04, "CPU 0").u8(0x05, 0x03).u8(0x06, 0x01).s(0x07, "QEMU").s(0x10, "pc-q35-8.2").
			u16(0x14, 2000).u16(0x16, 2000).u8(0x18, 0x41).u8(0x19, 0x01).u16(0x1A, 0xFFFF).u16(0x1C, 0xFFFF).u16(0x1E, 0xFFFF).
			u8(0x23, 4).u8(0x24, 4).u8(0x25, 4).u16(0x26, 0x0002).u16(0x28, 0x01).u16(0x2A, 4).u16(0x2C, 4).u16(0x2E, 4),
		*newStruct(16, 0x17, 0x1000).u8(0x04, 0x01).u8(0x05, 0x03).u8(0x06, 0x06).u32(0x07, 4<<20).u16(0x0B, 0xFFFE).u16(0x0D, 1),
		*newStruct(17, 0x28, 0x1100).u16(0x04, 0x1000).u16(0x06, 0xFFFE).u16(0x0C, 4096).u8(0x0E, 0x09).s(0x10, "DIMM 0").
			u8(0x12, 0x07).u16(0x13, 0x0002).s(0x17, "QEMU").s(0x1A, ""),
	}
	return t
}

// hypervTable uses SMBIOS 2.3 structure lengths (no core counts, no
// configured memory speed) and has no memory array structure.
func hypervTable() []rawStruct {
	t := []rawStruct{
		*newStruct(0, 0x14, 0x0000).s(0x04, "American Megatrends Inc.").s(0x05, "090008").s(0x08, "12/07/2018"),
		*newStruct(1, 0x19, 0x0001).s(0x04, "Microsoft Corporation").s(0x05, "Virtual Machine").s(0x06, "7.0").
			s(0x07, "0000-0001-0002-0003-0004-0005-00").
			bytes(0x08, []byte{0xd1, 0x2a, 0x4b, 0x98, 0x5e, 0x0f, 0x46, 0x4b, 0xa1, 0x88, 0x61, 0x77, 0x02, 0x10, 0xa3, 0x5f}).u8(0x18, 0x06),
		*newStruct(2, 0x08, 0x0002).s(0x04, "Microsoft Corporation").s(0x05, "Virtual Machine").s(0x06, "7.0").s(0x07, "0000-0002-0003-0004-0005-0006-00"),
		*newStruct(3, 0x11, 0x0003).s(0x04, "Microsoft Corporation").u8(0x05, 0x03).s(0x06, "7.0").s(0x07, "0000-0003-0004-0005-0006-0007-00").
			s(0x08, "7777-7777-7777-7777-7777-7777-77").u8(0x09, 0x03).u8(0x0A, 0x03).u8(0x0B, 0x03).u8(0x0C, 0x03),
		*newStruct(4, 0x23, 0x0004).s(0x04, "None").u8(0x05, 0x03).u8(0x06, 0xB3).s(0x07, "GenuineIntel").
			s(0x10, "Intel(R) Xeon(R) Gold 6338 CPU @ 2.00GHz").u16(0x14, 3700).u16(0x16, 2000).u8(0x18, 0x41).u8(0x19, 0x01).
			s(0x20, "None").s(0x21, "None").s(0x22, "None"),
		*newStruct(17, 0x1B, 0x0011).u16(0x04, 0xFFFE).u16(0x06, 0xFFFE).u16(0x0C, 2048).u8(0x0E, 0x09).s(0x10, "M0001").
			s(0x11, "BANK 0").u8(0x12, 0x02).u16(0x13, 0x0002).u16(0x15, 0).s(0x17, "Microsoft Corporation").s(0x18, "None").s(0x1A, "None"),
	}
	return t
}

func desktopTable() []rawStruct {
	t := []rawStruct{
		*newStruct(0, 0x1A, 0x0000).s(0x04, "Dell Inc.").s(0x05, "1.21.0").s(0x08, "03/10/2025"),
		*newStruct(1, 0x1B, 0x0100).s(0x04, "Dell Inc.").s(0x05, "OptiPlex 7090").s(0x07, "DSK0001").
			bytes(0x08, []byte{0x4c, 0x4c, 0x45, 0x44, 0x00, 0x30, 0x31, 0x10, 0x80, 0x35, 0xb4, 0xc0, 0x4f, 0x30, 0x30, 0x31}).
			u8(0x18, 0x06).s(0x19, "0A5B").s(0x1A, "OptiPlex"),
		*newStruct(2, 0x11, 0x0200).s(0x04, "Dell Inc.").s(0x05, "0K2WJ4").s(0x06, "A00").s(0x07, "/DSK0001/CNFCW0000000A/").u8(0x0D, 0x0A),
		*newStruct(3, 0x16, 0x0300).s(0x04, "Dell Inc.").u8(0x05, 0x83).s(0x07, "DSK0001").u8(0x09, 0x03).u8(0x0A, 0x03).u8(0x0B, 0x03).u8(0x0C, 0x03),
		*newStruct(4, 0x30, 0x0400).s(0x04, "CPU 1").u8(0x05, 0x03).u8(0x06, 0xC6).s(0x07, "Intel(R) Corporation").
			s(0x10, "Intel(R) Core(TM) i7-10700 CPU @ 2.90GHz").u16(0x14, 4800).u16(0x16, 2900).u8(0x18, 0x41).u8(0x19, 0x3E).
			u8(0x23, 8).u8(0x24, 8).u8(0x25, 16).u16(0x28, 0xC6).u16(0x2A, 8).u16(0x2C, 8).u16(0x2E, 16),
		*newStruct(16, 0x17, 0x1000).u8(0x04, 0x03).u8(0x05, 0x03).u8(0x06, 0x03).u32(0x07, 128<<20).u16(0x0B, 0xFFFE).u16(0x0D, 4),
	}
	for i, size := range []uint16{16384, 0, 16384, 0} {
		d := newStruct(17, 0x54, uint16(0x1100+i)).u16(0x04, 0x1000).u16(0x0C, size).u8(0x0E, 0x09).
			s(0x10, fmt.Sprintf("DIMM%d", i+1)).s(0x11, fmt.Sprintf("BANK %d", i))
		if size != 0 {
			d.u8(0x12, 0x1A).u16(0x13, 0x4080).u16(0x15, 2933).s(0x17, "SK Hynix").s(0x18, fmt.Sprintf("2B%06X", i)).
				s(0x1A, "HMA82GU6DJR8N-WM").u8(0x1B, 0x02).u16(0x20, 2933)
		} else {
			d.u8(0x12, 0x02).u16(0x13, 0x0004).s(0x17, "NO DIMM").s(0x18, "NO DIMM").s(0x1A, "NO DIMM")
		}
		t = append(t, *d)
	}
	return t
}
