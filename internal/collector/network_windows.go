//go:build windows

package collector

import (
	"context"
	"errors"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
)

const (
	gaaFlagIncludePrefix   = 0x10
	gaaFlagIncludeGateways = 0x80
	ifOperStatusUp         = 1
	maxAdapterBuffer       = 4 << 20
)

// collectNetwork reads the adapters with GetAdaptersAddresses (including
// gateways) within networkBudget.
func collectNetwork(ctx context.Context) (agentfacts.Network, bool) {
	ctx, cancel := context.WithTimeout(ctx, networkBudget)
	defer cancel()
	ch := make(chan agentfacts.Network, 1)
	go func() { ch <- windowsNetwork() }()
	select {
	case n := <-ch:
		return n, len(n.Interfaces) > 0
	case <-ctx.Done():
		return agentfacts.Network{}, false
	}
}

func windowsNetwork() agentfacts.Network {
	size := uint32(15 << 10)
	var buf []byte
	for {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, gaaFlagIncludePrefix|gaaFlagIncludeGateways, 0,
			(*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size) // #nosec G103 -- documented Win32 buffer contract
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) || size > maxAdapterBuffer {
			return agentfacts.Network{}
		}
	}
	var adapters []agentfacts.Adapter
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next { // #nosec G103 -- see above
		a := agentfacts.Adapter{
			Name:        windows.UTF16PtrToString(aa.FriendlyName),
			Description: windows.UTF16PtrToString(aa.Description),
			IfType:      aa.IfType,
			Up:          aa.OperStatus == ifOperStatusUp,
			SpeedBps:    aa.TransmitLinkSpeed,
			Ipv4Metric:  aa.Ipv4Metric,
			Ipv6Metric:  aa.Ipv6Metric,
		}
		if l := int(aa.PhysicalAddressLength); l > 0 && l <= len(aa.PhysicalAddress) {
			a.MAC = net.HardwareAddr(aa.PhysicalAddress[:l]).String()
		}
		for g := aa.FirstGatewayAddress; g != nil; g = g.Next {
			if ip := g.Address.IP(); ip != nil {
				a.Gateways = append(a.Gateways, ip.String())
			}
		}
		for u := aa.FirstUnicastAddress; u != nil; u = u.Next {
			ip := u.Address.IP()
			if ip == nil {
				continue
			}
			a.Unicast = append(a.Unicast, agentfacts.UnicastAddr{
				Address: ip.String(), PrefixLength: u.OnLinkPrefixLength,
				PrefixOrigin: u.PrefixOrigin, SuffixOrigin: u.SuffixOrigin, DadState: u.DadState,
			})
		}
		adapters = append(adapters, a)
	}
	return agentfacts.FromAdapters(adapters)
}
