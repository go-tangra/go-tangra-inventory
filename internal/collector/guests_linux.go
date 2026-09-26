//go:build linux

package collector

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// pveRoot is the Proxmox cluster file system. Only its qemu-server and lxc
// directories (which resolve to the local node) are read; priv/ and every
// other path are never opened.
const pveRoot = "/etc/pve"

// guestOpen opens one guest configuration (overridable in tests).
var guestOpen = func(p string) (*os.File, error) { return os.Open(p) } // #nosec G304 -- fixed Proxmox directories, *.conf only

// collectGuests reports the Proxmox guests defined on this node.
func collectGuests() ([]store.HypervisorGuest, uint32) { return collectGuestsFrom(pveRoot) }

func collectGuestsFrom(root string) ([]store.HypervisorGuest, uint32) {
	var files []agentfacts.ProxmoxFile
	found := false
	for _, dir := range []struct{ sub, typ string }{{"qemu-server", "qemu"}, {"lxc", "lxc"}} {
		entries, err := os.ReadDir(filepath.Join(root, dir.sub))
		if err != nil {
			continue
		}
		found = true
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".conf") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			if len(files) > store.MaxGuests { // one past the bound is enough to count the excess
				files = append(files, agentfacts.ProxmoxFile{Name: n, Type: dir.typ})
				continue
			}
			f, err := guestOpen(filepath.Join(root, dir.sub, n))
			if err != nil {
				continue
			}
			data, _ := io.ReadAll(io.LimitReader(f, agentfacts.MaxProxmoxConfBytes))
			_ = f.Close()
			files = append(files, agentfacts.ProxmoxFile{Name: n, Type: dir.typ, Data: data})
		}
	}
	if !found {
		return nil, 0
	}
	return agentfacts.ProxmoxGuests(files)
}
