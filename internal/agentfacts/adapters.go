package agentfacts

import (
	"net/netip"
	"strings"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Windows IF_TYPE / NL_PREFIX_ORIGIN / NL_SUFFIX_ORIGIN / NL_DAD_STATE values
// (ipifcons.h, nldef.h).
const (
	ifTypeEthernet    = 6
	ifTypeLoopback    = 24
	ifTypePropVirtual = 53
	ifTypeWireless    = 71
	ifTypeTunnel      = 131

	prefixOriginManual              = 1
	prefixOriginWellKnown           = 2
	prefixOriginDhcp                = 3
	prefixOriginRouterAdvertisement = 4

	suffixOriginManual    = 1
	suffixOriginDhcp      = 3
	suffixOriginLinkLayer = 4
	suffixOriginRandom    = 5

	dadDeprecated = 3
	dadPreferred  = 4
)

// Adapter is the part of a Windows IP_ADAPTER_ADDRESSES entry the agent uses,
// already copied out of the OS structure by the collector.
type Adapter struct {
	Name        string // FriendlyName
	Description string
	MAC         string
	IfType      uint32
	Up          bool // OperStatus == IfOperStatusUp
	SpeedBps    uint64
	Gateways    []string
	Ipv4Metric  uint32
	Ipv6Metric  uint32
	Unicast     []UnicastAddr
}

// UnicastAddr is one IP_ADAPTER_UNICAST_ADDRESS.
type UnicastAddr struct {
	Address      string
	PrefixLength uint8 // OnLinkPrefixLength
	PrefixOrigin int32
	SuffixOrigin int32
	DadState     int32
}

// FromAdapters maps Windows adapters to bounded interfaces and selects the
// primary addresses like BuildInterfaces: addresses are dynamic when their
// prefix or suffix came from DHCP or router advertisement, temporary when
// the suffix is random (privacy address), deprecated per DAD state.
func FromAdapters(in []Adapter) Network {
	var res Network
	var routes []DefaultRoute
	index := map[int]string{}
	pos := map[string]int{}
	for _, a := range in {
		if a.Name == "" {
			continue
		}
		if len(res.Interfaces) == store.MaxInterfaces {
			res.Truncated.Interfaces++
			continue
		}
		n := store.NetIface{Name: a.Name, MAC: canonicalMAC(a.MAC), Type: adapterKind(a), Up: a.Up}
		if a.SpeedBps != ^uint64(0) {
			n.SpeedBps = a.SpeedBps
		}
		for _, u := range a.Unicast {
			ad, ok := unicast(u)
			if !ok {
				continue
			}
			if len(n.Addresses) == store.MaxIfaceAddresses {
				res.Truncated.Addresses++
				continue
			}
			n.Addresses = append(n.Addresses, ad)
			n.IPAddresses = append(n.IPAddresses, netip.PrefixFrom(netip.MustParseAddr(ad.Address), int(ad.PrefixLength)).String())
			if ad.Family == "ipv4" && ad.DHCP {
				n.DHCP = true
			}
		}
		idx := len(res.Interfaces) + 1
		index[idx] = a.Name
		pos[a.Name] = len(res.Interfaces)
		for _, g := range a.Gateways {
			ga, err := netip.ParseAddr(g)
			if err != nil {
				continue
			}
			r := DefaultRoute{Index: idx, Gateway: ga.WithZone("").String(), Family: "ipv6", Metric: a.Ipv6Metric}
			if ga.Unmap().Is4() {
				r.Family, r.Metric, r.Gateway = "ipv4", a.Ipv4Metric, ga.Unmap().String()
			}
			routes = append(routes, r)
		}
		res.Interfaces = append(res.Interfaces, n)
	}
	res.PrimaryIPv4, res.PrimaryIPv6 = applyRoutes(res.Interfaces, pos, index, routes)
	return res
}

func adapterKind(a Adapter) string {
	switch {
	case a.IfType == ifTypeLoopback:
		return store.IfaceLoopback
	case strings.Contains(a.Description, "Multiplexor"):
		return store.IfaceBond
	case strings.HasPrefix(a.Name, "vEthernet"):
		return store.IfaceVirtual
	case a.IfType == ifTypeWireless:
		return store.IfaceWireless
	case a.IfType == ifTypeTunnel || a.IfType == ifTypePropVirtual:
		return store.IfaceVirtual
	case a.IfType == ifTypeEthernet:
		return store.IfaceEthernet
	}
	return store.IfaceOther
}

func unicast(u UnicastAddr) (store.IfAddress, bool) {
	ip, err := netip.ParseAddr(u.Address)
	if err != nil {
		return store.IfAddress{}, false
	}
	ip = ip.WithZone("").Unmap()
	if int(u.PrefixLength) > ip.BitLen() {
		return store.IfAddress{}, false
	}
	ad := store.IfAddress{
		Address: ip.String(), PrefixLength: uint32(u.PrefixLength), Family: "ipv6",
		DHCP: u.PrefixOrigin == prefixOriginDhcp || u.SuffixOrigin == suffixOriginDhcp ||
			(u.PrefixOrigin == prefixOriginRouterAdvertisement && u.SuffixOrigin != suffixOriginManual),
		Temporary:  u.SuffixOrigin == suffixOriginRandom,
		Deprecated: u.DadState == dadDeprecated,
		Scope:      addrScope(ip),
	}
	if ip.Is4() {
		ad.Family = "ipv4"
	}
	return ad, true
}

// addrScope derives the scope of an address from its value.
func addrScope(ip netip.Addr) string {
	switch {
	case ip.IsLoopback():
		return "host"
	case ip.IsLinkLocalUnicast():
		return "link"
	case ip.Is6() && netip.MustParsePrefix("fec0::/10").Contains(ip):
		return "site"
	}
	return "global"
}
