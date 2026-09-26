package agentfacts

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Proxmox guest configuration bounds.
const (
	MaxProxmoxConfBytes = 64 << 10
	maxGuestNameLen     = 256
)

var vmidFileRE = regexp.MustCompile(`^([0-9]{1,9})\.conf$`)

// macKeys are the net<N> option keys that carry a guest NIC's MAC.
var macKeys = []string{"virtio", "e1000", "e1000e", "vmxnet3", "rtl8139", "hwaddr"}

// ProxmoxFile is one guest configuration read by the collector from
// /etc/pve/qemu-server (Type "qemu") or /etc/pve/lxc (Type "lxc").
type ProxmoxFile struct {
	Name string
	Type string
	Data []byte
}

// ProxmoxGuests parses guest configurations, keeping at most
// store.MaxGuests; dropped counts the valid guests beyond the bound.
func ProxmoxGuests(files []ProxmoxFile) ([]store.HypervisorGuest, uint32) {
	var out []store.HypervisorGuest
	var dropped uint32
	for _, f := range files {
		g, ok := ParseProxmoxConf(f.Name, f.Type, f.Data)
		if !ok {
			continue
		}
		if len(out) == store.MaxGuests {
			dropped++
			continue
		}
		out = append(out, g)
	}
	return out, dropped
}

// ParseProxmoxConf parses the current (pre-snapshot) section of a guest
// configuration: the VMID comes from the file name, the name from "name:"
// (qemu) or "hostname:" (lxc), MACs from net<N> options. Only the first
// MaxProxmoxConfBytes are read; parsing stops at the first "[" section.
func ParseProxmoxConf(fileName, typ string, data []byte) (store.HypervisorGuest, bool) {
	m := vmidFileRE.FindStringSubmatch(fileName)
	if m == nil {
		return store.HypervisorGuest{}, false
	}
	g := store.HypervisorGuest{ID: m[1], Platform: "proxmox"}
	nameKey := ""
	switch typ {
	case "qemu":
		g.Kind, nameKey = "vm", "name"
	case "lxc":
		g.Kind, nameKey = "container", "hostname"
	default:
		return store.HypervisorGuest{}, false
	}
	if len(data) > MaxProxmoxConfBytes {
		data = data[:MaxProxmoxConfBytes]
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "[") {
			break
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch {
		case key == nameKey:
			g.Name = guestName(val)
		case strings.HasPrefix(key, "net") && isDigits(key[3:]):
			if mac := netMAC(val); mac != "" && len(g.MACs) < store.MaxGuestMACs {
				g.MACs = append(g.MACs, mac)
			}
		}
	}
	return g, true
}

func guestName(v string) string {
	if !utf8.ValidString(v) {
		return ""
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ""
		}
	}
	if len(v) > maxGuestNameLen {
		n := maxGuestNameLen
		for n > 0 && !utf8.RuneStart(v[n]) {
			n--
		}
		v = v[:n]
	}
	return v
}

func isDigits(s string) bool {
	if s == "" || len(s) > 3 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// netMAC extracts the MAC from a net<N> value ("virtio=AA:..,bridge=vmbr0").
func netMAC(v string) string {
	for _, opt := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(opt, "=")
		if !ok {
			continue
		}
		for _, mk := range macKeys {
			if k == mk {
				return canonicalMAC(val)
			}
		}
	}
	return ""
}
