package diff_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/diff"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// find returns the change for a category+key, if present.
func find(changes []store.Change, cat, key string) (store.Change, bool) {
	for _, c := range changes {
		if c.Category == cat && c.ComponentKey == key {
			return c, true
		}
	}
	return store.Change{}, false
}

func TestDiff_NoChangesWhenEqual(t *testing.T) {
	inv := store.Inventory{
		BIOS:       store.BIOSInfo{Vendor: "Acme", Version: "1.0"},
		System:     store.SystemInfo{Manufacturer: "Acme", ProductName: "Box"},
		Processors: []store.Processor{{SocketDesignation: "CPU0", CoreCount: 4}},
		Disks:      []store.Disk{{Serial: "D1", SizeBytes: 100}},
	}
	if got := diff.Diff(inv, inv); len(got) != 0 {
		t.Fatalf("expected no changes for identical inventories, got %d: %+v", len(got), got)
	}
}

func TestDiff_SingletonModified(t *testing.T) {
	prev := store.Inventory{BIOS: store.BIOSInfo{Vendor: "Acme", Version: "1.0"}}
	next := store.Inventory{BIOS: store.BIOSInfo{Vendor: "Acme", Version: "2.0"}}

	c, ok := find(diff.Diff(prev, next), diff.CatBIOS, diff.CatBIOS)
	if !ok {
		t.Fatalf("no bios change reported")
	}
	if c.ChangeType != store.ChangeModified {
		t.Errorf("change_type = %q, want %q", c.ChangeType, store.ChangeModified)
	}
	if !strings.Contains(c.Before, "1.0") || !strings.Contains(c.After, "2.0") {
		t.Errorf("before/after not populated: before=%q after=%q", c.Before, c.After)
	}
}

func TestDiff_SingletonAddedAndRemoved(t *testing.T) {
	// OS added: prev zero-value OS, next populated.
	added := diff.Diff(store.Inventory{}, store.Inventory{OS: store.OSInfo{Name: "Linux"}})
	c, ok := find(added, diff.CatOS, diff.CatOS)
	if !ok || c.ChangeType != store.ChangeAdded {
		t.Fatalf("expected os added, got %+v (ok=%v)", c, ok)
	}
	if c.Before != "" || !strings.Contains(c.After, "Linux") {
		t.Errorf("added change before=%q after=%q", c.Before, c.After)
	}

	// System removed: prev populated, next zero-value.
	removed := diff.Diff(store.Inventory{System: store.SystemInfo{Manufacturer: "Acme"}}, store.Inventory{})
	c, ok = find(removed, diff.CatSystem, diff.CatSystem)
	if !ok || c.ChangeType != store.ChangeRemoved {
		t.Fatalf("expected system removed, got %+v (ok=%v)", c, ok)
	}
	if c.After != "" || !strings.Contains(c.Before, "Acme") {
		t.Errorf("removed change before=%q after=%q", c.Before, c.After)
	}
}

func TestDiff_CollectionAddedRemovedModified(t *testing.T) {
	prev := store.Inventory{
		Disks: []store.Disk{
			{Serial: "keep", SizeBytes: 100},
			{Serial: "gone", SizeBytes: 200},
			{Serial: "grow", SizeBytes: 300},
		},
	}
	next := store.Inventory{
		Disks: []store.Disk{
			{Serial: "keep", SizeBytes: 100}, // unchanged
			{Serial: "grow", SizeBytes: 999}, // modified
			{Serial: "new", SizeBytes: 400},  // added
		},
	}
	got := diff.Diff(prev, next)

	if _, ok := find(got, diff.CatDisk, "keep"); ok {
		t.Errorf("unchanged disk should not produce a change")
	}
	if c, ok := find(got, diff.CatDisk, "new"); !ok || c.ChangeType != store.ChangeAdded {
		t.Errorf("new disk: %+v ok=%v", c, ok)
	}
	if c, ok := find(got, diff.CatDisk, "gone"); !ok || c.ChangeType != store.ChangeRemoved {
		t.Errorf("gone disk: %+v ok=%v", c, ok)
	}
	if c, ok := find(got, diff.CatDisk, "grow"); !ok || c.ChangeType != store.ChangeModified {
		t.Errorf("grow disk: %+v ok=%v", c, ok)
	}
}

func TestDiff_AllCategoriesAdded(t *testing.T) {
	next := store.Inventory{
		BIOS:       store.BIOSInfo{Vendor: "V"},
		System:     store.SystemInfo{Manufacturer: "M"},
		Baseboard:  store.BaseboardInfo{Manufacturer: "B"},
		Chassis:    store.ChassisInfo{Manufacturer: "C"},
		OS:         store.OSInfo{Name: "OS"},
		Processors: []store.Processor{{SocketDesignation: "CPU0"}},
		Memory:     store.MemoryInfo{Modules: []store.MemoryModule{{DeviceLocator: "DIMM0", SerialNumber: "S"}}},
		Disks:      []store.Disk{{Serial: "D"}},
		Networks:   []store.NetIface{{Name: "eth0", MAC: "aa:bb"}},
		Monitors:   []store.Monitor{{SerialNumber: "MON"}},
		Programs:   []store.Program{{Name: "app", Version: "1"}},
		Services:   []store.Service{{Name: "svc"}},
		Users:      []store.UserAccount{{Name: "root"}},
		Patches:    []store.Patch{{ID: "KB1"}},
	}
	got := diff.Diff(store.Inventory{}, next)

	wantCats := []string{
		diff.CatBIOS, diff.CatSystem, diff.CatBaseboard, diff.CatChassis, diff.CatProcessor,
		diff.CatMemory, diff.CatDisk, diff.CatNetwork, diff.CatMonitor, diff.CatSoftware,
		diff.CatService, diff.CatOS, diff.CatUser, diff.CatPatch,
	}
	for _, cat := range wantCats {
		found := false
		for _, c := range got {
			if c.Category == cat {
				if c.ChangeType != store.ChangeAdded {
					t.Errorf("category %s: change_type=%q want added", cat, c.ChangeType)
				}
				found = true
			}
		}
		if !found {
			t.Errorf("category %s produced no change", cat)
		}
	}
}

func TestDiff_Deterministic(t *testing.T) {
	prev := store.Inventory{
		Programs: []store.Program{{Name: "z", Version: "1"}, {Name: "a", Version: "1"}},
	}
	next := store.Inventory{
		Programs: []store.Program{{Name: "m", Version: "1"}, {Name: "b", Version: "1"}},
	}
	a, _ := json.Marshal(diff.Diff(prev, next))
	b, _ := json.Marshal(diff.Diff(prev, next))
	if string(a) != string(b) {
		t.Errorf("Diff not deterministic:\n%s\n%s", a, b)
	}

	// Component keys within a category must be ascending.
	got := diff.Diff(prev, next)
	var lastKey string
	for _, c := range got {
		if c.Category != diff.CatSoftware {
			continue
		}
		if lastKey != "" && c.ComponentKey < lastKey {
			t.Errorf("software keys not sorted: %q before %q", lastKey, c.ComponentKey)
		}
		lastKey = c.ComponentKey
	}
}

func TestDiff_MemoryKeyedByLocatorAndSerial(t *testing.T) {
	prev := store.Inventory{Memory: store.MemoryInfo{Modules: []store.MemoryModule{
		{DeviceLocator: "DIMM0", SerialNumber: "S1", CapacityBytes: 8},
	}}}
	next := store.Inventory{Memory: store.MemoryInfo{Modules: []store.MemoryModule{
		{DeviceLocator: "DIMM0", SerialNumber: "S2", CapacityBytes: 8}, // different serial => replacement
	}}}
	got := diff.Diff(prev, next)
	var added, removed int
	for _, c := range got {
		if c.Category != diff.CatMemory {
			continue
		}
		switch c.ChangeType {
		case store.ChangeAdded:
			added++
		case store.ChangeRemoved:
			removed++
		}
	}
	if added != 1 || removed != 1 {
		t.Errorf("swapped memory module: added=%d removed=%d, want 1/1", added, removed)
	}
}
