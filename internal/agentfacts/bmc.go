package agentfacts

import (
	"net"
	"net/netip"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// IPMI LAN configuration parameter selectors (IPMI v2.0 table 23-4) the agent
// reads. Nothing else is ever requested: in particular never parameter 16
// (community string), the authentication type settings (1, 2), or the cipher
// suite parameters (22-24), and no user/password command is issued.
const (
	LanParamIP             uint8 = 3
	LanParamIPSource       uint8 = 4
	LanParamMAC            uint8 = 5
	LanParamSubnetMask     uint8 = 6
	LanParamDefaultGateway uint8 = 12
	LanParamVLANID         uint8 = 20
)

// AllowedLanParams is the complete, ordered set of LAN parameters the agent
// may request.
var AllowedLanParams = []uint8{LanParamIP, LanParamIPSource, LanParamMAC, LanParamSubnetMask, LanParamDefaultGateway, LanParamVLANID}

// IsAllowedLanParam reports whether selector is in AllowedLanParams.
func IsAllowedLanParam(selector uint8) bool {
	for _, p := range AllowedLanParams {
		if p == selector {
			return true
		}
	}
	return false
}

// BmcChannel is the raw parameter data read from one LAN channel.
type BmcChannel struct {
	Channel uint8
	Params  map[uint8][]byte
}

// DecodeBmc builds the BMC report: the first channel with a non-zero IP
// gives address, prefix, gateway, IP source and VLAN; every channel with a
// non-zero MAC becomes a port (at most store.MaxBmcPorts; dropped counts the
// rest). It returns nil when nothing was reported.
func DecodeBmc(chans []BmcChannel) (*store.Bmc, uint32) {
	b := &store.Bmc{}
	var dropped uint32
	for _, ch := range chans {
		ip := ipv4(ch.Params[LanParamIP])
		mac := macOf(ch.Params[LanParamMAC])
		if ip != "" && b.Address == "" {
			b.Address = ip
			b.PrefixLength = maskPrefix(ch.Params[LanParamSubnetMask])
			b.Gateway = ipv4(ch.Params[LanParamDefaultGateway])
			b.IPSource = ipSource(ch.Params[LanParamIPSource])
			b.VLANID = vlanID(ch.Params[LanParamVLANID])
		}
		if mac == "" && ip == "" {
			continue
		}
		if len(b.Ports) == store.MaxBmcPorts {
			dropped++
			continue
		}
		b.Ports = append(b.Ports, store.BmcPort{Channel: uint32(ch.Channel), MAC: mac, Address: ip})
	}
	if b.Address == "" && len(b.Ports) == 0 {
		return nil, dropped
	}
	return b, dropped
}

func ipv4(d []byte) string {
	if len(d) < 4 {
		return ""
	}
	a := netip.AddrFrom4([4]byte(d[:4]))
	if a.IsUnspecified() {
		return ""
	}
	return a.String()
}

func macOf(d []byte) string {
	if len(d) < 6 {
		return ""
	}
	return canonicalMAC(net.HardwareAddr(d[:6]).String())
}

// maskPrefix converts a contiguous IPv4 netmask to its prefix length (0 when
// absent or non-contiguous).
func maskPrefix(d []byte) uint32 {
	if len(d) < 4 {
		return 0
	}
	ones, bits := net.IPv4Mask(d[0], d[1], d[2], d[3]).Size()
	if bits == 0 {
		return 0
	}
	return uint32(ones) // #nosec G115 -- 0..32
}

// ipSource decodes parameter 4 (bits 3:0).
func ipSource(d []byte) string {
	if len(d) < 1 {
		return ""
	}
	switch d[0] & 0x0f {
	case 0:
		return ""
	case 1:
		return "static"
	case 2:
		return "dhcp"
	case 3:
		return "bios"
	}
	return "other"
}

// vlanID decodes parameter 20: 12-bit id, bit 7 of the second byte enables it.
func vlanID(d []byte) uint32 {
	if len(d) < 2 || d[1]&0x80 == 0 {
		return 0
	}
	return uint32(d[1]&0x0f)<<8 | uint32(d[0])
}
