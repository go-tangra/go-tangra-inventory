package diff_test

import (
	"encoding/json"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/diff"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// FuzzDiff feeds arbitrary bytes as two inventory payloads (parsed leniently)
// and asserts Diff never panics regardless of the pair it is given.
func FuzzDiff(f *testing.F) {
	seedA, _ := json.Marshal(store.Inventory{
		BIOS:  store.BIOSInfo{Vendor: "Acme", Version: "1.0"},
		Disks: []store.Disk{{Serial: "D1", SizeBytes: 100}},
	})
	seedB, _ := json.Marshal(store.Inventory{
		BIOS:  store.BIOSInfo{Vendor: "Acme", Version: "2.0"},
		Disks: []store.Disk{{Serial: "D2", SizeBytes: 200}},
	})
	f.Add(seedA, seedB)
	rich, _ := json.Marshal(store.Inventory{
		Networks: []store.NetIface{{Name: "eth0", MAC: "00:11:22:33:44:55", Type: "ethernet", SpeedBps: 1e9, Gateway: "192.0.2.1",
			DHCP: true, Addresses: []store.IfAddress{{Address: "192.0.2.10", PrefixLength: 24, Family: "ipv4", Temporary: true}}}},
		Bmc:              &store.Bmc{Address: "10.0.0.5"},
		HypervisorGuests: []store.HypervisorGuest{{ID: "100"}},
		UpdateState:      store.UpdateState{Status: "up_to_date"},
		Virtualization:   store.Virtualization{Role: "vm"},
	})
	f.Add(rich, seedA)
	f.Add([]byte(`{}`), []byte(`{}`))
	f.Add([]byte("not json"), []byte("also not json"))
	f.Add([]byte(``), []byte(``))

	f.Fuzz(func(t *testing.T, a, b []byte) {
		var prev, next store.Inventory
		_ = json.Unmarshal(a, &prev)
		_ = json.Unmarshal(b, &next)
		// Must not panic; the result is otherwise unconstrained.
		_ = diff.Diff(prev, next)
		// Diffing an inventory against itself must be empty.
		if got := diff.Diff(prev, prev); len(got) != 0 {
			t.Fatalf("self-diff produced %d changes", len(got))
		}
	})
}
