package agentfacts

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// updateFixtures rewrites the synthetic tables and their golden decodings:
//
//	go test ./internal/agentfacts -run TestSMBIOSCorpus -update-smbios
var updateFixtures = flag.Bool("update-smbios", false, "rewrite testdata/smbios fixtures and golden files")

const smbiosDir = "testdata/smbios"

func decodeFixture(t *testing.T, name string) SMBIOSHardware {
	t.Helper()
	table, err := os.ReadFile(filepath.Join(smbiosDir, name+".bin"))
	if err != nil {
		t.Fatal(err)
	}
	ep, err := os.ReadFile(filepath.Join(smbiosDir, name+".ep"))
	if err != nil {
		t.Fatal(err)
	}
	v, ok := ParseEntryPoint(ep)
	if !ok {
		t.Fatalf("%s: unreadable entry point", name)
	}
	hw, err := DecodeSMBIOSTable(table, v)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return hw
}

func TestSMBIOSCorpus(t *testing.T) {
	if *updateFixtures {
		for _, f := range fixtures {
			writeFile(t, f.name+".bin", encode(f.build()))
			writeFile(t, f.name+".ep", entryPoint(f.version))
		}
	}
	bins, err := filepath.Glob(filepath.Join(smbiosDir, "*.bin"))
	if err != nil || len(bins) < len(fixtures) {
		t.Fatalf("corpus = %v %v", bins, err)
	}
	for _, bin := range bins {
		name := strings.TrimSuffix(filepath.Base(bin), ".bin")
		t.Run(name, func(t *testing.T) {
			hw := decodeFixture(t, name)
			got, err := json.MarshalIndent(hw, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join(smbiosDir, name+".golden.json")
			if *updateFixtures {
				writeFile(t, name+".golden.json", got)
			}
			want, err := os.ReadFile(golden) // #nosec G304 -- test fixture
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s decodes differently from its golden file:\n%s", name, got)
			}
			assertNoShiftedNames(t, hw)
		})
	}
}

// TestFixturesMatchBuilder keeps the committed tables and the builder in step.
func TestFixturesMatchBuilder(t *testing.T) {
	for _, f := range fixtures {
		b, err := os.ReadFile(filepath.Join(smbiosDir, f.name+".bin"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, encode(f.build())) {
			t.Errorf("%s.bin differs from the builder (run with -update-smbios)", f.name)
		}
	}
}

func TestNode1Fixture(t *testing.T) {
	hw := decodeFixture(t, "node-1")
	if hw.Availability != store.AvailOK {
		t.Fatalf("availability = %q", hw.Availability)
	}
	if hw.BIOS != (store.BIOSInfo{Vendor: "American Megatrends International, LLC.", Version: "2.5", ReleaseDate: "11/26/2025"}) {
		t.Errorf("bios = %+v", hw.BIOS)
	}
	if hw.System.Manufacturer != "Supermicro" || hw.System.ProductName != "Super Server" || hw.System.WakeUpType != "Power Switch" ||
		hw.System.SKUNumber != "" || hw.System.UUID != "3c8b6900-1e4a-11c5-8000-3cecef000001" {
		t.Errorf("system = %+v", hw.System)
	}
	if hw.Baseboard.Product != "X12DPi-NT6" || hw.Baseboard.BoardType != "Motherboard (includes processor, memory, and I/O)" {
		t.Errorf("baseboard = %+v", hw.Baseboard)
	}
	if hw.Chassis.Type != "Rack Mount Chassis" || hw.Chassis.BootupState != "Safe" || hw.Chassis.Manufacturer != "Supermicro" {
		t.Errorf("chassis = %+v", hw.Chassis)
	}
	if len(hw.Processors) != 2 {
		t.Fatalf("processors = %d", len(hw.Processors))
	}
	for i, p := range hw.Processors {
		if p.Family != "Intel Xeon processor" || p.Upgrade != "Socket LGA4189" || p.Type != "Central Processor" ||
			p.CoreCount != 12 || p.ThreadCount != 24 || !p.SocketPopulated || p.Version != "Intel(R) Xeon(R) Silver 4310 CPU @ 2.10GHz" ||
			p.MaxSpeedMHz != 4000 || p.CurrentSpeedMHz != 2100 {
			t.Errorf("processor %d = %+v", i, p)
		}
	}
	m := hw.Memory
	if m.Array.Location != "System board or motherboard" || m.Array.Use != "System memory" || m.Array.ErrorCorrection != "Single-bit ECC" ||
		m.Array.MaximumCapacity != 12<<40 || m.Array.NumberOfDevices != 16 || len(m.Arrays) != 1 {
		t.Errorf("array = %+v", m.Array)
	}
	if m.SlotsTotal != 16 || m.SlotsPopulated != 16 || m.TotalPhysicalBytes != 256<<30 || len(m.Modules) != 16 {
		t.Fatalf("memory = %d/%d slots, %d bytes", m.SlotsPopulated, m.SlotsTotal, m.TotalPhysicalBytes)
	}
	for i, mod := range m.Modules {
		if mod.MemoryType != "DDR4" || mod.FormFactor != "DIMM" || !reflect.DeepEqual(mod.TypeDetail, []string{"Synchronous", "Registered (Buffered)"}) ||
			mod.CapacityBytes != 16<<30 || mod.SpeedMTs != 3200 || mod.ConfiguredSpeedMTs != 2666 || mod.Manufacturer != "Samsung" ||
			mod.PartNumber != "M393A2K43EB3-CWE" || mod.RankCount != 2 || !mod.Populated || mod.ArrayHandle != uint32(m.Array.Handle) {
			t.Errorf("module %d = %+v", i, mod)
		}
	}
	if m.Modules[0].DeviceLocator != "P1-DIMMA1" || m.Modules[0].BankLocator != "P0_Node0_Channel0_Dimm0" ||
		m.Modules[15].DeviceLocator != "P2-DIMMH1" || m.Modules[15].BankLocator != "P1_Node1_Channel7_Dimm0" {
		t.Errorf("locators = %q %q / %q %q", m.Modules[0].DeviceLocator, m.Modules[0].BankLocator, m.Modules[15].DeviceLocator, m.Modules[15].BankLocator)
	}
	if hw.BIOSLanguage != "en|US|iso8859-1" || len(hw.OEMStrings) != 2 || len(hw.Cache) != 6 || len(hw.Ports) != 1 || len(hw.Slots) != 1 {
		t.Errorf("extras = %q %v %d %v %v", hw.BIOSLanguage, hw.OEMStrings, len(hw.Cache), hw.Ports, hw.Slots)
	}
}

func TestVMFixturesPartial(t *testing.T) {
	q := decodeFixture(t, "qemu-vm")
	if q.Availability != store.AvailPartial || q.System.Manufacturer != "QEMU" || q.Chassis.Type != "Other" || q.Baseboard != (store.BaseboardInfo{}) {
		t.Fatalf("qemu = %+v", q)
	}
	if q.Memory.Array.ErrorCorrection != "Multi-bit ECC" || q.Memory.TotalPhysicalBytes != 4<<30 || q.Memory.Modules[0].MemoryType != "RAM" ||
		q.Memory.Modules[0].SpeedMTs != 0 || q.Processors[0].Family != "Other" {
		t.Fatalf("qemu memory/cpu = %+v %+v", q.Memory, q.Processors)
	}
	h := decodeFixture(t, "hyperv-vm")
	if h.Availability != store.AvailPartial || len(h.Memory.Arrays) != 0 || h.Memory.TotalPhysicalBytes != 2<<30 {
		t.Fatalf("hyperv = %+v", h.Memory)
	}
	// SMBIOS 2.3 structures: no core counts, no configured speed.
	if p := h.Processors[0]; p.CoreCount != 0 || p.ThreadCount != 0 || p.Family != "Intel Xeon processor" || p.PartNumber != "None" {
		t.Fatalf("hyperv cpu = %+v", p)
	}
	if mod := h.Memory.Modules[0]; mod.ConfiguredSpeedMTs != 0 || mod.MemoryType != "Unknown" {
		t.Fatalf("hyperv module = %+v", mod)
	}
	if h.System.UUID != "d12a4b98-5e0f-464b-a188-61770210a35f" {
		t.Fatalf("SMBIOS < 2.6 UUID keeps the raw byte order: %s", h.System.UUID)
	}
	d := decodeFixture(t, "desktop")
	if d.Availability != store.AvailOK || d.Memory.SlotsTotal != 4 || d.Memory.SlotsPopulated != 2 || d.Memory.Array.ErrorCorrection != "None" ||
		d.Chassis.Type != "Desktop" || d.Processors[0].Family != "Intel Core i7 processor" || d.Processors[0].Upgrade != "Socket LGA1200" {
		t.Fatalf("desktop = %+v", d)
	}
	if mod := d.Memory.Modules[1]; mod.Populated || mod.Manufacturer != "" || mod.DeviceLocator != "DIMM2" {
		t.Fatalf("desktop empty slot = %+v", mod)
	}
	if mod := d.Memory.Modules[0]; !reflect.DeepEqual(mod.TypeDetail, []string{"Synchronous", "Unbuffered (Unregistered)"}) {
		t.Fatalf("desktop module = %+v", mod)
	}
}

// assertNoShiftedNames fails on the names the shifted go-smbios decoding
// produced for node-1 class hardware (SC-001: 0 shifted values).
func assertNoShiftedNames(t *testing.T, hw SMBIOSHardware) {
	t.Helper()
	for _, m := range hw.Memory.Modules {
		if m.MemoryType == "LPDDR3" && m.FormFactor == "TSOP" {
			t.Errorf("shifted memory decoding: %+v", m)
		}
	}
	for _, a := range hw.Memory.Arrays {
		if a.Location == "ISA add-on card" || a.Use == "Video memory" {
			t.Errorf("shifted array decoding: %+v", a)
		}
	}
}

func writeFile(t *testing.T, name string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(smbiosDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(smbiosDir, name), b, 0o644); err != nil { // #nosec G306 -- test fixture
		t.Fatal(err)
	}
}

// TestGoSMBIOSShiftReproduced documents the root cause (research F1): the
// go-smbios typed enumerations decode the node-1 table to exactly the
// production symptoms, the agent decoder to the DSP0134 names. It doubles as
// the reproducer for an upstream report.
func TestGoSMBIOSShiftReproduced(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(smbiosDir, "node-1.bin"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := smbiosDecode(b, SMBIOSVersion{3, 3})
	if err != nil {
		t.Fatal(err)
	}
	d, pma := s.MemoryDevices[0], s.PhysicalMemoryArray
	got := []string{d.MemoryType.String(), d.FormFactor.String(), pma.Location.String(), pma.Use.String(), pma.MemoryErrorCorrection.String()}
	if !reflect.DeepEqual(got, []string{"LPDDR3", "TSOP", "ISA add-on card", "Video memory", "Multi-bit ECC"}) {
		t.Logf("go-smbios no longer shifts (upstream fixed?): %q", got)
	}
	hw := decodeFixture(t, "node-1")
	want := []string{"DDR4", "DIMM", "System board or motherboard", "System memory", "Single-bit ECC"}
	m, a := hw.Memory.Modules[0], hw.Memory.Array
	if !reflect.DeepEqual([]string{m.MemoryType, m.FormFactor, a.Location, a.Use, a.ErrorCorrection}, want) {
		t.Fatalf("agent decoder = %q", []string{m.MemoryType, m.FormFactor, a.Location, a.Use, a.ErrorCorrection})
	}
}
