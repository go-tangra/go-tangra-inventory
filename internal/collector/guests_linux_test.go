//go:build linux

package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The guest collector reads only qemu-server/*.conf and lxc/*.conf below the
// Proxmox root and never anything under priv/.
func TestCollectGuestsReadsOnlyGuestConfigs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "qemu-server", "100.conf"), "name: web\nnet0: virtio=BC:24:11:00:00:01,bridge=vmbr0\n")
	writeFile(t, filepath.Join(root, "lxc", "200.conf"), "hostname: db\nnet0: name=eth0,hwaddr=BC:24:11:00:00:02\n")
	writeFile(t, filepath.Join(root, "qemu-server", "notes.txt"), "ignored")
	writeFile(t, filepath.Join(root, "priv", "token.cfg"), "SECRET")
	writeFile(t, filepath.Join(root, "priv", "100.conf"), "name: secret\n")
	writeFile(t, filepath.Join(root, "nodes", "pve1", "qemu-server", "300.conf"), "name: other-node\n")

	var opened []string
	prev := guestOpen
	t.Cleanup(func() { guestOpen = prev })
	guestOpen = func(p string) (*os.File, error) {
		opened = append(opened, p)
		return prev(p)
	}
	guests, dropped := collectGuestsFrom(root)
	if len(guests) != 2 || dropped != 0 || guests[0].Name != "web" || guests[1].Name != "db" || guests[1].Kind != "container" {
		t.Fatalf("guests = %+v", guests)
	}
	for _, p := range opened {
		if strings.Contains(p, "priv") || !strings.HasSuffix(p, ".conf") {
			t.Fatalf("opened %s", p)
		}
	}
	if len(opened) != 2 {
		t.Fatalf("opened = %v", opened)
	}
}

func TestCollectGuestsAbsentAndBounded(t *testing.T) {
	if g, _ := collectGuestsFrom(filepath.Join(t.TempDir(), "nope")); g != nil {
		t.Fatalf("no proxmox: %+v", g)
	}
	root := t.TempDir()
	for i := 0; i < 1003; i++ {
		writeFile(t, filepath.Join(root, "qemu-server", fmt.Sprint(1000+i, ".conf")), "name: g\n")
	}
	// A config larger than 64 KiB is read only up to the bound.
	writeFile(t, filepath.Join(root, "lxc", "5.conf"), "hostname: big\n"+strings.Repeat("#"+strings.Repeat("x", 1000)+"\n", 80)+"net0: hwaddr=BC:24:11:00:00:09\n")
	guests, dropped := collectGuestsFrom(root)
	if len(guests) != 1000 || dropped == 0 {
		t.Fatalf("bound: %d guests, dropped %d", len(guests), dropped)
	}
}
