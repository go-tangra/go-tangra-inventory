//go:build linux

package collector

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// slowFS delays every open, simulating a hung sysfs attribute.
type slowFS struct {
	fs.FS
	delay time.Duration
}

func (s slowFS) Open(name string) (fs.File, error) {
	time.Sleep(s.delay)
	return s.FS.Open(name)
}

func tree(n int) fstest.MapFS {
	m := fstest.MapFS{}
	for i := 0; i < n; i++ {
		base := "sys/block/sd" + string(rune('a'+i)) + "/"
		m[base+"size"] = &fstest.MapFile{Data: []byte("100\n")}
		m[base+"devpath"] = &fstest.MapFile{Data: []byte("/sys/devices/pci0000:00/ata1/block/x")}
		m[base+"queue/rotational"] = &fstest.MapFile{Data: []byte("1\n")}
	}
	return m
}

func TestLinuxDisksBudget(t *testing.T) {
	start := time.Now()
	res, _ := linuxDisks(context.Background(), agentfacts.TreeSource{FS: slowFS{FS: tree(20), delay: 20 * time.Millisecond}}, 150*time.Millisecond)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("budget not honoured: %s", time.Since(start))
	}
	if res.Availability != store.AvailPartial || len(res.Disks) >= 20 {
		t.Fatalf("budget exceeded must be partial: %d disks, %q", len(res.Disks), res.Availability)
	}
	res, r := linuxDisks(context.Background(), agentfacts.TreeSource{FS: tree(3)}, time.Second)
	if res.Availability != store.AvailOK || len(res.Disks) != 3 || r == nil {
		t.Fatalf("fast tree = %+v", res)
	}
}

func TestLinuxHostDisksDoNotFail(t *testing.T) {
	var inv store.Inventory
	collectDisks(context.Background(), &inv, Options{CollectDisks: true})
	switch inv.Availability.Disks {
	case store.AvailOK, store.AvailPartial, store.AvailUnavailable:
	default:
		t.Fatalf("availability = %q", inv.Availability.Disks)
	}
	inv = store.Inventory{}
	collectDisks(context.Background(), &inv, Options{})
	if inv.Disks != nil || inv.Availability.Disks != store.AvailUnsupported {
		t.Fatalf("disabled = %+v", inv.Availability)
	}
}
