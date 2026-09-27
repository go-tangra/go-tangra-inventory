package agentfacts

import (
	"bytes"
	"testing"
	"unicode/utf8"

	"github.com/siderolabs/go-smbios/smbios"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func smbiosDecode(b []byte, v SMBIOSVersion) (*smbios.SMBIOS, error) {
	return smbios.Decode(bytes.NewReader(b), smbios.Version{Major: v.Major, Minor: v.Minor})
}

// FuzzSMBIOSStructures decodes arbitrary structures of every decoded type:
// never a panic, every string within the bound and valid UTF-8, list counts
// within the collection bounds.
func FuzzSMBIOSStructures(f *testing.F) {
	for _, fx := range fixtures {
		for _, r := range fx.build() {
			f.Add(r.typ, uint8(len(r.b)), r.b[4:], "a\x00b", uint8(fx.version.Major), uint8(fx.version.Minor))
		}
	}
	f.Add(uint8(17), uint8(0xFF), []byte{0xFF}, "", uint8(3), uint8(7))
	f.Add(uint8(4), uint8(0), []byte{}, "", uint8(2), uint8(0))
	f.Fuzz(func(t *testing.T, typ, length uint8, formatted []byte, strs string, major, minor uint8) {
		s := SMBIOSStructure{Type: typ, Length: length, Handle: 1, Formatted: formatted, Strings: bytes2strings(strs)}
		// Every structure type the decoder knows, plus the fuzzed one.
		var all []SMBIOSStructure
		for _, tt := range []uint8{0, 1, 2, 3, 4, 7, 8, 9, 11, 13, 16, 17, typ} {
			c := s
			c.Type = tt
			all = append(all, c, c)
		}
		hw := DecodeSMBIOS(SMBIOSVersion{Major: int(major), Minor: int(minor)}, all)
		assertSMBIOSBounds(t, hw)
	})
}

// FuzzSMBIOSTable feeds raw table bytes through go-smbios and the decoder.
func FuzzSMBIOSTable(f *testing.F) {
	for _, fx := range fixtures {
		f.Add(encode(fx.build()))
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, table []byte) {
		// go-smbios' typed decoding may panic on malformed tables; the
		// wrapper must turn that into an error.
		_, _ = OpenGoSMBIOS(func() (*smbios.SMBIOS, error) { return smbiosDecode(table, SMBIOSVersion{3, 3}) })
		for _, s := range SplitSMBIOS(table) {
			if int(s.Length) < 4 || len(s.Formatted) != int(s.Length)-4 || len(s.Strings) > maxStructureStrings {
				t.Fatalf("split structure out of bounds: %+v", s)
			}
		}
		hw, err := DecodeSMBIOSTable(table, SMBIOSVersion{3, 3})
		if err != nil {
			return
		}
		assertSMBIOSBounds(t, hw)
	})
}

func bytes2strings(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range bytes.Split([]byte(s), []byte{0}) {
		out = append(out, string(p))
	}
	return out
}

func assertSMBIOSBounds(t *testing.T, hw SMBIOSHardware) {
	t.Helper()
	if len(hw.Processors) > store.MaxProcessors || len(hw.Memory.Modules) > store.MaxMemorySlots || len(hw.Memory.Arrays) > store.MaxMemoryArrays ||
		hw.Memory.SlotsPopulated > hw.Memory.SlotsTotal || int(hw.Memory.SlotsTotal) != len(hw.Memory.Modules) {
		t.Fatalf("bounds: %d processors %d modules %d arrays", len(hw.Processors), len(hw.Memory.Modules), len(hw.Memory.Arrays))
	}
	check := func(s string) {
		if len(s) > maxSMBIOSString || !utf8.ValidString(s) {
			t.Fatalf("string out of bounds: %q", s)
		}
	}
	for _, s := range []string{hw.BIOS.Vendor, hw.BIOS.Version, hw.System.Manufacturer, hw.System.UUID, hw.System.WakeUpType,
		hw.Baseboard.BoardType, hw.Chassis.Type, hw.Chassis.SKUNumber, hw.Chassis.BootupState, hw.BIOSLanguage} {
		check(s)
	}
	for _, p := range hw.Processors {
		check(p.Family)
		check(p.Version)
		check(p.Upgrade)
	}
	for _, m := range hw.Memory.Modules {
		check(m.DeviceLocator)
		check(m.MemoryType)
		check(m.PartNumber)
		if len(m.TypeDetail) > 15 {
			t.Fatalf("type detail = %v", m.TypeDetail)
		}
	}
	for _, s := range append(append(append([]string{}, hw.OEMStrings...), hw.Ports...), hw.Slots...) {
		check(s)
	}
	if len(hw.OEMStrings) > maxOEMStrings || len(hw.Ports) > maxPorts || len(hw.Slots) > maxSlots || len(hw.Cache) > maxCaches {
		t.Fatal("extra lists out of bounds")
	}
}
