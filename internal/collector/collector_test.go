package collector

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestCollectComposesWithoutError verifies the orchestrator returns an inventory
// (never an error for a missing platform capability) and stamps the basics.
func TestCollectComposesWithoutError(t *testing.T) {
	inv, err := Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if inv.CollectedAt.IsZero() {
		t.Error("CollectedAt not set")
	}
	if inv.AgentVersion == "" {
		t.Error("AgentVersion not set")
	}
	host, _ := os.Hostname()
	if inv.Identity.Hostname != host {
		t.Errorf("Hostname = %q, want %q", inv.Identity.Hostname, host)
	}
}

// TestCollectCanceledContext verifies a canceled context is surfaced.
func TestCollectCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Collect(ctx); err == nil {
		t.Fatal("expected error for canceled context")
	}
}

// TestApplyHardware verifies synthetic host values land on the inventory.
func TestApplyHardware(t *testing.T) {
	hw := agentfacts.SMBIOSHardware{
		BIOS:         store.BIOSInfo{Vendor: "SynthCorp", Version: "1.2.3"},
		System:       store.SystemInfo{Manufacturer: "SynthCorp", ProductName: "Model-X", UUID: "11111111-2222-3333-4444-555555555555"},
		Baseboard:    store.BaseboardInfo{Manufacturer: "SynthBoard"},
		Chassis:      store.ChassisInfo{Manufacturer: "SynthCase", AssetTag: "ASSET-1", Type: "Tower"},
		Processors:   []store.Processor{{SocketDesignation: "CPU0", CoreCount: 8}},
		Cache:        []store.CacheInfo{{SocketDesignation: "L1"}},
		Memory:       store.MemoryInfo{TotalPhysicalBytes: 8 << 30},
		Ports:        []string{"USB1"},
		Slots:        []string{"PCIe0"},
		OEMStrings:   []string{"oem"},
		BIOSLanguage: "en|US|iso8859-1",
		Truncated:    store.CollectionLimits{Processors: 1, MemorySlots: 2, MemoryArrays: 3},
		Availability: store.AvailPartial,
	}
	inv := store.Inventory{Truncated: store.CollectionLimits{Disks: 4}}
	applyHardware(&inv, hw)

	if inv.BIOS.Vendor != "SynthCorp" || inv.System.UUID != hw.System.UUID || inv.Chassis.Type != "Tower" {
		t.Errorf("identity parts = %+v %+v %+v", inv.BIOS, inv.System, inv.Chassis)
	}
	if len(inv.Processors) != 1 || inv.Processors[0].CoreCount != 8 || inv.Memory.TotalPhysicalBytes != 8<<30 {
		t.Errorf("processors/memory = %+v %+v", inv.Processors, inv.Memory)
	}
	if inv.Chassis.AssetTag != "ASSET-1" || inv.BIOSLanguage != "en|US|iso8859-1" || len(inv.Ports) != 1 || len(inv.Slots) != 1 || len(inv.OEMStrings) != 1 || len(inv.Cache) != 1 {
		t.Errorf("extras = %+v", inv)
	}
	if inv.Truncated != (store.CollectionLimits{Disks: 4, Processors: 1, MemorySlots: 2, MemoryArrays: 3}) {
		t.Errorf("truncated = %+v", inv.Truncated)
	}
	if inv.Availability.SMBIOS != store.AvailPartial {
		t.Errorf("availability = %+v", inv.Availability)
	}
}

// TestMapHardwareNode1 maps the node-1 fixture table end to end (read, split,
// decode, apply) as the agent does.
func TestMapHardwareNode1(t *testing.T) {
	hw, ok := readHardware("../agentfacts/testdata/smbios/node-1.ep", "../agentfacts/testdata/smbios/node-1.bin", nil)
	if !ok {
		t.Fatal("fixture table not read")
	}
	var inv store.Inventory
	applyHardware(&inv, hw)
	if inv.Availability.SMBIOS != store.AvailOK || inv.Chassis.Type != "Rack Mount Chassis" || inv.Chassis.BootupState != "Safe" {
		t.Fatalf("chassis/availability = %+v %+v", inv.Chassis, inv.Availability)
	}
	m := inv.Memory
	if m.SlotsTotal != 16 || m.SlotsPopulated != 16 || len(m.Arrays) != 1 || m.TotalPhysicalBytes != 256<<30 ||
		m.Array.Use != "System memory" || m.Array.ErrorCorrection != "Single-bit ECC" || m.Modules[0].MemoryType != "DDR4" {
		t.Fatalf("memory = %+v", m.Array)
	}
	p := inv.Processors[0]
	if p.Family != "Intel Xeon processor" || p.Type != "Central Processor" || p.Upgrade != "Socket LGA4189" {
		t.Fatalf("processor = %+v", p)
	}
}

func TestReadHardwareFallbacks(t *testing.T) {
	dir := t.TempDir()
	// No sysfs table: the fallback (go-smbios on Windows) is used.
	calls := 0
	fallback := func() (agentfacts.SMBIOSVersion, []agentfacts.SMBIOSStructure, error) {
		calls++
		return agentfacts.SMBIOSVersion{Major: 3, Minor: 3}, []agentfacts.SMBIOSStructure{{Type: 1, Length: 8, Formatted: []byte{1, 0, 0, 0}, Strings: []string{"Vendor"}}}, nil
	}
	hw, ok := readHardware(dir+"/ep", dir+"/DMI", fallback)
	if !ok || calls != 1 || hw.System.Manufacturer != "Vendor" {
		t.Fatalf("fallback = %+v %v %d", hw.System, ok, calls)
	}
	// Fallback failing or empty: SMBIOS unavailable.
	if _, ok := readHardware(dir+"/ep", dir+"/DMI", func() (agentfacts.SMBIOSVersion, []agentfacts.SMBIOSStructure, error) {
		return agentfacts.SMBIOSVersion{}, nil, os.ErrNotExist
	}); ok {
		t.Fatal("failed fallback must report unavailable")
	}
	if _, ok := readHardware(dir+"/ep", dir+"/DMI", nil); ok {
		t.Fatal("no source must report unavailable")
	}
	// A readable table with an unreadable entry point assumes SMBIOS 3.0.
	if err := os.WriteFile(dir+"/DMI", []byte{0, 4, 0, 0, 0, 0, 127, 4, 0, 0, 0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readHardware(dir+"/ep", dir+"/DMI", nil); !ok {
		t.Fatal("table without entry point must still decode")
	}
}

// TestCollectMarksHardwareSchema: every collection is schema 2 and reports
// SMBIOS availability even when the table cannot be read.
func TestCollectMarksHardwareSchema(t *testing.T) {
	inv, err := Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inv.HardwareSchema != store.HardwareSchemaCurrent {
		t.Fatalf("hardware schema = %d", inv.HardwareSchema)
	}
	switch inv.Availability.SMBIOS {
	case store.AvailOK, store.AvailPartial, store.AvailUnavailable:
	default:
		t.Fatalf("smbios availability = %q", inv.Availability.SMBIOS)
	}
}

// TestHardwareNoGoSMBIOSEnums guards the root cause of the shifted values:
// the collector must not use go-smbios' typed enumerations.
func TestHardwareNoGoSMBIOSEnums(t *testing.T) {
	src, err := os.ReadFile("hardware.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{".String()", "MemoryType", "FormFactor", "PhysicalMemoryArray", "MemoryDevices", "BoardType"} {
		if strings.Contains(string(src), bad) {
			t.Errorf("hardware.go uses go-smbios typed decoding (%s)", bad)
		}
	}
}

// TestMergeUser verifies dedup-by-name and empty-name skipping.
func TestMergeUser(t *testing.T) {
	base := []store.UserAccount{{Name: "alice"}}
	if got := mergeUser(base, store.UserAccount{Name: "alice", IsAdmin: true}); len(got) != 1 {
		t.Errorf("duplicate name should not be appended: %+v", got)
	}
	if got := mergeUser(base, store.UserAccount{Name: ""}); len(got) != 1 {
		t.Errorf("empty name should not be appended: %+v", got)
	}
	if got := mergeUser(base, store.UserAccount{Name: "bob"}); len(got) != 2 {
		t.Errorf("new name should be appended: %+v", got)
	}
}

func TestHasFlag(t *testing.T) {
	if !hasFlag([]string{"broadcast", "up"}, "UP") {
		t.Error("expected case-insensitive match")
	}
	if hasFlag([]string{"broadcast"}, "up") {
		t.Error("unexpected match")
	}
}

func TestHostArchFallback(t *testing.T) {
	if hostArch("arm64") != "arm64" {
		t.Error("explicit arch should be returned")
	}
	if hostArch("") == "" {
		t.Error("empty arch should fall back to runtime GOARCH")
	}
}
