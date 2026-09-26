package agentfacts

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"net"
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Netlink constants (linux/netlink.h, linux/rtnetlink.h, linux/if_addr.h).
// They are defined here instead of taken from package syscall so the parser
// is portable, fuzzable on any platform and independent of the agent's OS.
const (
	nlmsgHdrLen  = 16
	nlmsgError   = 2
	nlmsgDone    = 3
	rtmNewAddr   = 20
	rtmNewRoute  = 24
	ifaddrmsgLen = 8
	rtmsgLen     = 12

	afInet  = 2
	afInet6 = 10

	ifaAddress = 1
	ifaLocal   = 2
	ifaFlags   = 8

	ifaFTemporary  = 0x01
	ifaFDeprecated = 0x20
	ifaFPermanent  = 0x80

	scopeUniverse = 0
	scopeSite     = 200
	scopeLink     = 253
	scopeHost     = 254

	rtaDst      = 1
	rtaOIF      = 4
	rtaGateway  = 5
	rtaPriority = 6
	rtaTable    = 15

	rtTableMain = 254
	rtnUnicast  = 1

	arphrdEther    = 1
	arphrdLoopback = 772

	// maxNetlinkMessages bounds one dump.
	maxNetlinkMessages = 65536
)

var errNetlink = errors.New("agentfacts: malformed netlink message")

// AddrEntry is one address of an interface (by kernel ifindex).
type AddrEntry struct {
	Index int
	Addr  store.IfAddress
}

// DefaultRoute is a default route (destination length 0, main table).
type DefaultRoute struct {
	Index   int
	Gateway string // "" for a device route
	Metric  uint32
	Family  string // ipv4|ipv6
}

// nlMessages splits a netlink dump into (type, payload) pairs, stopping at
// NLMSG_DONE; NLMSG_ERROR and structural errors fail the dump.
func nlMessages(b []byte, fn func(typ uint16, payload []byte)) error {
	for n := 0; len(b) > 0 && n < maxNetlinkMessages; n++ {
		if len(b) < nlmsgHdrLen {
			return errNetlink
		}
		l := int(binary.NativeEndian.Uint32(b[0:4]))
		typ := binary.NativeEndian.Uint16(b[4:6])
		if l < nlmsgHdrLen || l > len(b) {
			return errNetlink
		}
		switch typ {
		case nlmsgDone:
			return nil
		case nlmsgError:
			return errNetlink
		}
		fn(typ, b[nlmsgHdrLen:l])
		next := (l + 3) &^ 3
		if next > len(b) {
			return nil
		}
		b = b[next:]
	}
	return nil
}

// attrs walks rtattrs; it returns false when an attribute is malformed.
func attrs(b []byte, fn func(typ uint16, data []byte)) bool {
	for len(b) >= 4 {
		l := int(binary.NativeEndian.Uint16(b[0:2]))
		typ := binary.NativeEndian.Uint16(b[2:4]) & 0x3fff // strip NLA_F_NESTED/NET_BYTEORDER
		if l < 4 || l > len(b) {
			return false
		}
		fn(typ, b[4:l])
		next := (l + 3) &^ 3
		if next >= len(b) {
			return true
		}
		b = b[next:]
	}
	return true
}

func ipFrom(family uint8, raw []byte) (netip.Addr, bool) {
	switch {
	case family == afInet && len(raw) == 4:
		return netip.AddrFrom4([4]byte(raw)), true
	case family == afInet6 && len(raw) == 16:
		return netip.AddrFrom16([16]byte(raw)), true
	}
	return netip.Addr{}, false
}

func familyName(family uint8) string {
	if family == afInet {
		return "ipv4"
	}
	return "ipv6"
}

func scopeName(s uint8) string {
	switch s {
	case scopeUniverse:
		return "global"
	case scopeSite:
		return "site"
	case scopeLink:
		return "link"
	case scopeHost:
		return "host"
	}
	return ""
}

// ParseAddrMessages decodes an RTM_GETADDR dump. Addresses are dynamic (DHCP,
// DHCPv6 or SLAAC) when the kernel does not mark them permanent; IFA_FLAGS
// (32 bit) supersedes the 8-bit ifa_flags when present. For IPv4 the local
// address (IFA_LOCAL) is used, IFA_ADDRESS being the peer on point-to-point
// links. Malformed entries are skipped.
func ParseAddrMessages(dump []byte) ([]AddrEntry, error) {
	var out []AddrEntry
	var bad bool
	err := nlMessages(dump, func(typ uint16, p []byte) {
		if typ != rtmNewAddr {
			return
		}
		if len(p) < ifaddrmsgLen {
			bad = true
			return
		}
		fam, prefix, flags8, scope := p[0], p[1], p[2], p[3]
		if fam != afInet && fam != afInet6 {
			return
		}
		flags := uint32(flags8)
		var addr, local netip.Addr
		ok := attrs(p[ifaddrmsgLen:], func(t uint16, d []byte) {
			switch t {
			case ifaAddress:
				addr, _ = ipFrom(fam, d)
			case ifaLocal:
				local, _ = ipFrom(fam, d)
			case ifaFlags:
				if len(d) == 4 {
					flags = binary.NativeEndian.Uint32(d)
				}
			}
		})
		if fam == afInet && local.IsValid() {
			addr = local
		}
		if !ok || !addr.IsValid() || int(prefix) > addr.BitLen() {
			return
		}
		out = append(out, AddrEntry{
			Index: int(binary.NativeEndian.Uint32(p[4:8])),
			Addr: store.IfAddress{
				Address: addr.String(), PrefixLength: uint32(prefix), Family: familyName(fam),
				DHCP: flags&ifaFPermanent == 0, Temporary: flags&ifaFTemporary != 0,
				Deprecated: flags&ifaFDeprecated != 0, Scope: scopeName(scope),
			},
		})
	})
	if err == nil && bad {
		err = errNetlink
	}
	return out, err
}

// ParseRouteMessages decodes an RTM_GETROUTE dump and returns the default
// routes (destination length 0) of the main table with unicast type.
func ParseRouteMessages(dump []byte) ([]DefaultRoute, error) {
	var out []DefaultRoute
	var bad bool
	err := nlMessages(dump, func(typ uint16, p []byte) {
		if typ != rtmNewRoute {
			return
		}
		if len(p) < rtmsgLen {
			bad = true
			return
		}
		fam, dstLen, table, rtype := p[0], p[1], uint32(p[4]), p[7]
		if (fam != afInet && fam != afInet6) || dstLen != 0 || rtype != rtnUnicast {
			return
		}
		r := DefaultRoute{Family: familyName(fam)}
		hasDst := false
		ok := attrs(p[rtmsgLen:], func(t uint16, d []byte) {
			switch t {
			case rtaDst:
				hasDst = true
			case rtaGateway:
				if a, ok := ipFrom(fam, d); ok {
					r.Gateway = a.String()
				}
			case rtaOIF:
				if len(d) == 4 {
					r.Index = int(binary.NativeEndian.Uint32(d))
				}
			case rtaPriority:
				if len(d) == 4 {
					r.Metric = binary.NativeEndian.Uint32(d)
				}
			case rtaTable:
				if len(d) == 4 {
					table = binary.NativeEndian.Uint32(d)
				}
			}
		})
		if !ok || hasDst || table != rtTableMain || r.Index == 0 {
			return
		}
		out = append(out, r)
	})
	if err == nil && bad {
		err = errNetlink
	}
	return out, err
}

// IfaceFacts are the sysfs facts of one interface.
type IfaceFacts struct {
	Name      string
	MAC       string
	Kind      string
	Up        bool
	SpeedMbps int64
	Master    string
	VLANID    uint32
}

// Iface is the interface without addresses.
func (f IfaceFacts) Iface() store.NetIface {
	n := store.NetIface{Name: f.Name, MAC: f.MAC, Type: f.Kind, Up: f.Up, Master: f.Master, VLANID: f.VLANID}
	if f.SpeedMbps > 0 {
		n.SpeedBps = uint64(f.SpeedMbps) * 1_000_000
	}
	return n
}

// ReadSysfsIface reads /sys/class/net/<name> facts from fsys (rooted at
// /sys/class/net) and classifies the interface kind:
// loopback (type 772) > bond (bonding/) > bridge (bridge/) > wireless
// (wireless/ or phy80211) > vlan (/proc/net/vlan/config) > virtual (no device
// link) > other (non-Ethernet hardware) > ethernet.
func ReadSysfsIface(fsys fs.FS, name string, vlans map[string]uint32) IfaceFacts {
	read := func(file string) string {
		b, err := fs.ReadFile(fsys, name+"/"+file)
		if err != nil || len(b) > 4096 {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	exists := func(file string) bool {
		_, err := fs.Stat(fsys, name+"/"+file)
		return err == nil
	}
	f := IfaceFacts{Name: name, MAC: canonicalMAC(read("address"))}
	state := read("operstate")
	f.Up = state == "up" || state == "unknown"
	if v, err := strconv.ParseInt(read("speed"), 10, 64); err == nil && v > 0 && v < 10_000_000 {
		f.SpeedMbps = v
	}
	if target, err := fs.ReadLink(fsys, name+"/master"); err == nil {
		f.Master = path.Base(target)
	}
	arphrd, _ := strconv.Atoi(read("type"))
	vid, isVLAN := vlans[name]
	switch {
	case arphrd == arphrdLoopback:
		f.Kind = store.IfaceLoopback
	case exists("bonding"):
		f.Kind = store.IfaceBond
	case exists("bridge"):
		f.Kind = store.IfaceBridge
	case exists("wireless") || exists("phy80211"):
		f.Kind = store.IfaceWireless
	case isVLAN:
		f.Kind, f.VLANID = store.IfaceVLAN, vid
	case !exists("device"):
		f.Kind = store.IfaceVirtual
	case arphrd != arphrdEther:
		f.Kind = store.IfaceOther
	default:
		f.Kind = store.IfaceEthernet
	}
	return f
}

// ParseVLANConfig parses /proc/net/vlan/config ("name | vid | parent").
func ParseVLANConfig(text string) map[string]uint32 {
	out := map[string]uint32{}
	for _, line := range strings.Split(text, "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		vid, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		if err != nil || vid > 4094 {
			continue
		}
		out[strings.TrimSpace(parts[0])] = uint32(vid)
	}
	return out
}

// Network is the agent's network collection result.
type Network struct {
	Interfaces  []store.NetIface
	PrimaryIPv4 string
	PrimaryIPv6 string
	Truncated   store.CollectionLimits
}

// BuildInterfaces joins sysfs facts, addresses (by ifindex) and default
// routes into bounded interfaces with gateway, default-route and DHCP flags,
// and selects the primary addresses: per family, the first global,
// non-temporary, non-deprecated address of the lowest-metric default-route
// interface.
func BuildInterfaces(facts []IfaceFacts, index map[int]string, addrs []AddrEntry, routes []DefaultRoute) Network {
	var res Network
	pos := map[string]int{}
	for _, f := range facts {
		if len(res.Interfaces) == store.MaxInterfaces {
			res.Truncated.Interfaces++
			continue
		}
		pos[f.Name] = len(res.Interfaces)
		res.Interfaces = append(res.Interfaces, f.Iface())
	}
	for _, a := range addrs {
		i, ok := pos[index[a.Index]]
		if !ok {
			continue
		}
		n := &res.Interfaces[i]
		if len(n.Addresses) == store.MaxIfaceAddresses {
			res.Truncated.Addresses++
			continue
		}
		n.Addresses = append(n.Addresses, a.Addr)
		n.IPAddresses = append(n.IPAddresses, a.Addr.Address+"/"+strconv.FormatUint(uint64(a.Addr.PrefixLength), 10))
		if a.Addr.Family == "ipv4" && a.Addr.DHCP {
			n.DHCP = true
		}
	}
	res.PrimaryIPv4, res.PrimaryIPv6 = applyRoutes(res.Interfaces, pos, index, routes)
	return res
}

// applyRoutes marks default-route interfaces, sets their gateway (IPv4
// preferred) and returns the primary addresses.
func applyRoutes(ifaces []store.NetIface, pos map[string]int, index map[int]string, routes []DefaultRoute) (string, string) {
	sorted := append([]DefaultRoute(nil), routes...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Metric < sorted[j].Metric })
	gw4 := map[int]bool{}
	for _, r := range sorted {
		i, ok := pos[index[r.Index]]
		if !ok {
			continue
		}
		n := &ifaces[i]
		n.DefaultRoute = true
		if r.Gateway != "" && (n.Gateway == "" || (r.Family == "ipv4" && !gw4[i])) {
			n.Gateway = r.Gateway
			gw4[i] = gw4[i] || r.Family == "ipv4"
		}
	}
	primary := func(family string) string {
		for _, r := range sorted {
			if r.Family != family {
				continue
			}
			i, ok := pos[index[r.Index]]
			if !ok {
				continue
			}
			for _, a := range ifaces[i].Addresses {
				if a.Family == family && a.Scope == "global" && !a.Temporary && !a.Deprecated {
					return a.Address
				}
			}
		}
		return ""
	}
	return primary("ipv4"), primary("ipv6")
}

// canonicalMAC returns the lower-case colon form of a 6- or 8-byte hardware
// address other than all-zero, or "".
func canonicalMAC(s string) string {
	hw, err := net.ParseMAC(s)
	if err != nil || (len(hw) != 6 && len(hw) != 8) {
		return ""
	}
	for _, b := range hw {
		if b != 0 {
			return hw.String()
		}
	}
	return ""
}
