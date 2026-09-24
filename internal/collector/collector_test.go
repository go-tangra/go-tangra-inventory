package collector

import (
	"context"
	"os"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/siderolabs/go-smbios/smbios"
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
	hw := hardware{
		BIOS:         store.BIOSInfo{Vendor: "SynthCorp", Version: "1.2.3"},
		System:       store.SystemInfo{Manufacturer: "SynthCorp", ProductName: "Model-X", UUID: "11111111-2222-3333-4444-555555555555"},
		Baseboard:    store.BaseboardInfo{Manufacturer: "SynthBoard"},
		Chassis:      store.ChassisInfo{Manufacturer: "SynthCase", AssetTag: "ASSET-1"},
		Processors:   []store.Processor{{SocketDesignation: "CPU0", CoreCount: 8}},
		Cache:        []store.CacheInfo{{SocketDesignation: "L1"}},
		Memory:       store.MemoryInfo{TotalPhysicalBytes: 8 << 30},
		Ports:        []string{"USB1"},
		Slots:        []string{"PCIe0"},
		OEMStrings:   []string{"oem"},
		BIOSLanguage: "en|US|iso8859-1",
	}
	var inv store.Inventory
	applyHardware(&inv, hw)

	if inv.BIOS.Vendor != "SynthCorp" {
		t.Errorf("BIOS.Vendor = %q", inv.BIOS.Vendor)
	}
	if inv.System.UUID != hw.System.UUID {
		t.Errorf("System.UUID = %q", inv.System.UUID)
	}
	if len(inv.Processors) != 1 || inv.Processors[0].CoreCount != 8 {
		t.Errorf("Processors = %+v", inv.Processors)
	}
	if inv.Memory.TotalPhysicalBytes != 8<<30 {
		t.Errorf("Memory.TotalPhysicalBytes = %d", inv.Memory.TotalPhysicalBytes)
	}
	if inv.Chassis.AssetTag != "ASSET-1" {
		t.Errorf("Chassis.AssetTag = %q", inv.Chassis.AssetTag)
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

// TestMemoryCapacity verifies the byte conversions on synthetic SMBIOS values.
func TestMemoryCapacity(t *testing.T) {
	// 8192 MB module (bit 15 clear -> value is in MB).
	if got := moduleCapacityBytes(smbios.MemoryDevice{Size: 8192}); got != 8192*1024*1024 {
		t.Errorf("moduleCapacityBytes = %d, want %d", got, 8192*1024*1024)
	}
	// Empty slot.
	if got := moduleCapacityBytes(smbios.MemoryDevice{Size: 0}); got != 0 {
		t.Errorf("empty slot capacity = %d, want 0", got)
	}
	// Extended-size escape (0x7FFF): value read from ExtendedSize in MB.
	const extMB = 40960 // 40 GB, well past the 32 GB-1 MB extended-size threshold
	ext := smbios.MemoryDevice{Size: 0x7FFF, ExtendedSize: extMB}
	if got := moduleCapacityBytes(ext); got != extMB*1024*1024 {
		t.Errorf("extended capacity = %d, want %d", got, extMB*1024*1024)
	}

	// Maximum capacity: kilobytes when only the 32-bit field is set.
	if got := maxCapacityBytes(smbios.PhysicalMemoryArray{MaximumCapacity: 1048576}); got != 1048576*1024 {
		t.Errorf("maxCapacityBytes(kb) = %d", got)
	}
	// Extended maximum capacity (bytes) wins when present.
	if got := maxCapacityBytes(smbios.PhysicalMemoryArray{ExtendedMaximumCapacity: 1 << 40}); got != 1<<40 {
		t.Errorf("maxCapacityBytes(ext) = %d", got)
	}
}

// TestMapHardwareEmpty verifies the pure mapper tolerates a zero-value SMBIOS.
func TestMapHardwareEmpty(t *testing.T) {
	hw := mapHardware(&smbios.SMBIOS{})
	if len(hw.Processors) != 0 || len(hw.Modules()) != 0 {
		t.Errorf("expected empty hardware, got %+v", hw)
	}
}

// Modules is a tiny helper for the test above to read memory modules.
func (h hardware) Modules() []store.MemoryModule { return h.Memory.Modules }

func TestPortLabel(t *testing.T) {
	cases := []struct {
		in   smbios.PortConnectorInformation
		want string
	}{
		{smbios.PortConnectorInformation{ExternalReferenceDesignator: "USB1"}, "USB1"},
		{smbios.PortConnectorInformation{InternalReferenceDesignator: "J1"}, "J1"},
		{smbios.PortConnectorInformation{InternalReferenceDesignator: "J1", ExternalReferenceDesignator: "USB1"}, "J1 / USB1"},
		{smbios.PortConnectorInformation{}, ""},
	}
	for _, c := range cases {
		if got := portLabel(c.in); got != c.want {
			t.Errorf("portLabel(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}
