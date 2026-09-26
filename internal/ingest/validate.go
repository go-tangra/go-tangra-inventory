package ingest

import (
	"net"
	"net/netip"
	"regexp"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// String bounds for the host report fields.
const (
	maxNameLen    = 256 // interface, master, guest names
	maxVersionLen = 128 // package available version
	maxVLANID     = 4094
)

var (
	guestIDRE = regexp.MustCompile(`^[0-9]{1,9}$`)
	tokenRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

	ifaceKinds = set(store.IfaceEthernet, store.IfaceWireless, store.IfaceBond, store.IfaceBridge,
		store.IfaceVLAN, store.IfaceVirtual, store.IfaceLoopback, store.IfaceOther)
	scopes       = set("global", "site", "link", "host")
	roles        = set(store.RolePhysical, store.RoleVM, store.RoleContainer, store.RoleUnknown)
	ipSources    = set("static", "dhcp", "bios", "other")
	guestKinds   = set("vm", "container")
	platforms    = set("proxmox")
	pkgManagers  = set("apt", "dnf", "yum", "apk", "pacman")
	updStatuses  = set(store.UpdateUnknown, store.UpdateUpToDate, store.UpdateAvailable, store.UpdateUnsupported, store.UpdateError)
	tristates    = set(store.TriUnknown, store.TriTrue, store.TriFalse)
	maxUint32Val = ^uint32(0)
)

func set(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// validateExtended bounds and sanitises the host report fields of an agent
// payload at the ingest edge (D16). Lists above their bound are truncated to
// the first N entries and the excess is added to inv.Truncated; structurally
// invalid values (MAC, IP, prefix, closed-set enums) are dropped or cleared;
// names with control characters or invalid UTF-8 drop their entry. The
// snapshot itself is always kept: one oversized list must not lose the rest
// of the report. Fields an older agent leaves empty stay empty.
func validateExtended(inv *store.Inventory) {
	lim := &inv.Truncated

	// Interfaces.
	nets := inv.Networks[:0:0]
	for _, n := range inv.Networks {
		if !cleanName(n.Name) {
			continue
		}
		if len(nets) == store.MaxInterfaces {
			lim.Interfaces = addSat(lim.Interfaces, 1)
			continue
		}
		nets = append(nets, validIface(n, lim))
	}
	if len(inv.Networks) == 0 {
		nets = nil
	}
	inv.Networks = nets

	inv.PrimaryIPv4 = addrOfFamily(inv.PrimaryIPv4, true)
	inv.PrimaryIPv6 = addrOfFamily(inv.PrimaryIPv6, false)

	// Virtualization.
	v := &inv.Virtualization
	v.Role = enum(v.Role, roles, store.RoleUnknown)
	if v.Kind != "" && !tokenRE.MatchString(v.Kind) {
		v.Kind = ""
	}
	if v.Source != "" && !tokenRE.MatchString(v.Source) {
		v.Source = ""
	}

	inv.Bmc = validBmc(inv.Bmc, lim)
	inv.HypervisorGuests = validGuests(inv.HypervisorGuests, lim)

	// Update state.
	u := &inv.UpdateState
	u.PackageManager = enum(u.PackageManager, pkgManagers, "")
	u.Status = enum(u.Status, updStatuses, store.UpdateUnknown)
	u.RebootRequired = enum(u.RebootRequired, tristates, store.TriUnknown)
	u.AutomaticUpdates = enum(u.AutomaticUpdates, tristates, store.TriUnknown)

	// Pending updates (programs with an available version).
	pending := 0
	for i := range inv.Programs {
		p := &inv.Programs[i]
		if p.AvailableVersion == "" {
			p.SecurityUpdate = false
			continue
		}
		if !cleanText(p.AvailableVersion, maxVersionLen) {
			p.AvailableVersion, p.SecurityUpdate = "", false
			continue
		}
		if pending == store.MaxPendingUpdates {
			p.AvailableVersion, p.SecurityUpdate = "", false
			lim.Packages = addSat(lim.Packages, 1)
			continue
		}
		pending++
	}
}

func validIface(n store.NetIface, lim *store.CollectionLimits) store.NetIface {
	n.MAC = canonicalMAC(n.MAC)
	if n.Type != "" {
		n.Type = enum(n.Type, ifaceKinds, store.IfaceOther)
	}
	if n.Gateway != "" {
		if a, err := netip.ParseAddr(n.Gateway); err == nil {
			n.Gateway = a.String()
		} else {
			n.Gateway = ""
		}
	}
	if n.Master != "" && !cleanName(n.Master) {
		n.Master = ""
	}
	if n.VLANID > maxVLANID {
		n.VLANID = 0
	}

	var cidrs []string
	for _, c := range n.IPAddresses {
		pfx, err := netip.ParsePrefix(c)
		if err != nil {
			// Old agents may send bare addresses; keep those.
			a, aerr := netip.ParseAddr(c)
			if aerr != nil {
				continue
			}
			c = a.String()
		} else {
			c = pfx.String()
		}
		if len(cidrs) == store.MaxIfaceAddresses {
			continue // counted once through Addresses below (same list)
		}
		cidrs = append(cidrs, c)
	}
	n.IPAddresses = cidrs

	var addrs []store.IfAddress
	for _, a := range n.Addresses {
		ip, err := netip.ParseAddr(a.Address)
		if err != nil || ip.Zone() != "" {
			continue
		}
		ip = ip.Unmap()
		if a.PrefixLength > uint32(ip.BitLen()) { // #nosec G115 -- BitLen is 32 or 128
			continue
		}
		if len(addrs) == store.MaxIfaceAddresses {
			lim.Addresses = addSat(lim.Addresses, 1)
			continue
		}
		a.Address = ip.String()
		a.Family = "ipv6"
		if ip.Is4() {
			a.Family = "ipv4"
		}
		if a.Scope != "" && !scopes[a.Scope] {
			a.Scope = ""
		}
		addrs = append(addrs, a)
	}
	n.Addresses = addrs
	return n
}

func validBmc(b *store.Bmc, lim *store.CollectionLimits) *store.Bmc {
	if b == nil {
		return nil
	}
	out := *b
	bits := 0
	if a, err := netip.ParseAddr(out.Address); err == nil && a.Zone() == "" {
		out.Address = a.Unmap().String()
		bits = a.Unmap().BitLen()
	} else {
		out.Address = ""
	}
	if out.PrefixLength > uint32(bits) { // #nosec G115 -- 0, 32 or 128
		out.PrefixLength = 0
	}
	if a, err := netip.ParseAddr(out.Gateway); err == nil && a.Zone() == "" {
		out.Gateway = a.Unmap().String()
	} else {
		out.Gateway = ""
	}
	out.IPSource = enum(out.IPSource, ipSources, "")
	if out.VLANID > maxVLANID {
		out.VLANID = 0
	}
	var ports []store.BmcPort
	for _, p := range b.Ports {
		p.MAC = canonicalMAC(p.MAC)
		if a, err := netip.ParseAddr(p.Address); err == nil && a.Zone() == "" {
			p.Address = a.Unmap().String()
		} else {
			p.Address = ""
		}
		if p.MAC == "" && p.Address == "" {
			continue
		}
		if len(ports) == store.MaxBmcPorts {
			lim.BmcPorts = addSat(lim.BmcPorts, 1)
			continue
		}
		ports = append(ports, p)
	}
	out.Ports = ports
	if out.Address == "" && len(out.Ports) == 0 {
		return nil
	}
	return &out
}

func validGuests(in []store.HypervisorGuest, lim *store.CollectionLimits) []store.HypervisorGuest {
	var out []store.HypervisorGuest
	for _, g := range in {
		if !guestIDRE.MatchString(g.ID) {
			continue
		}
		if !utf8.ValidString(g.Name) || hasControl(g.Name) {
			continue
		}
		if len(out) == store.MaxGuests {
			lim.Guests = addSat(lim.Guests, 1)
			continue
		}
		g.Name = clip(g.Name, maxNameLen)
		g.Kind = enum(g.Kind, guestKinds, "")
		g.Platform = enum(g.Platform, platforms, "")
		var macs []string
		for _, m := range g.MACs {
			if c := canonicalMAC(m); c != "" && len(macs) < store.MaxGuestMACs {
				macs = append(macs, c)
			}
		}
		g.MACs = macs
		out = append(out, g)
	}
	return out
}

// canonicalMAC returns the lower-case colon form of a 6- or 8-byte hardware
// address, or "" when s is not one.
func canonicalMAC(s string) string {
	if s == "" {
		return ""
	}
	hw, err := net.ParseMAC(s)
	if err != nil || (len(hw) != 6 && len(hw) != 8) {
		return ""
	}
	return hw.String()
}

// addrOfFamily returns the canonical form of s when it is an address of the
// wanted family (v4 when v4 is true), "" otherwise.
func addrOfFamily(s string, v4 bool) string {
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return ""
	}
	a = a.Unmap()
	if a.Is4() != v4 {
		return ""
	}
	return a.String()
}

func enum(v string, allowed map[string]bool, fallback string) string {
	if v == "" || allowed[v] {
		return v
	}
	return fallback
}

// cleanName reports whether s is a usable identifier: non-empty, valid UTF-8,
// no control characters, at most maxNameLen bytes.
func cleanName(s string) bool {
	return s != "" && cleanText(s, maxNameLen)
}

func cleanText(s string, maxLen int) bool {
	return len(s) <= maxLen && utf8.ValidString(s) && !hasControl(s)
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return true
		}
	}
	return false
}

// clip shortens s to at most n bytes without splitting a rune.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func addSat(v, d uint32) uint32 {
	if v > maxUint32Val-d {
		return maxUint32Val
	}
	return v + d
}
