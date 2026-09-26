package agentfacts

import (
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// FuzzBmcLanParams decodes arbitrary parameter data for every allowed LAN
// parameter: no panic, bounded ports, prefix within 0-32.
func FuzzBmcLanParams(f *testing.F) {
	f.Add([]byte{10, 0, 0, 5}, []byte{2}, []byte{0, 1, 2, 3, 4, 5}, []byte{255, 255, 255, 0}, []byte{10, 0, 0, 1}, []byte{0x2c, 0x81})
	f.Add([]byte{}, []byte{}, []byte{}, []byte{}, []byte{}, []byte{})
	f.Fuzz(func(t *testing.T, ip, src, mac, mask, gw, vlan []byte) {
		ch := BmcChannel{Channel: 1, Params: map[uint8][]byte{
			LanParamIP: ip, LanParamIPSource: src, LanParamMAC: mac, LanParamSubnetMask: mask,
			LanParamDefaultGateway: gw, LanParamVLANID: vlan,
		}}
		b, _ := DecodeBmc([]BmcChannel{ch, ch, ch, ch, ch, ch, ch, ch, ch, ch})
		if b == nil {
			return
		}
		if len(b.Ports) > store.MaxBmcPorts || b.PrefixLength > 32 || b.VLANID > 4095 {
			t.Fatalf("out of bounds: %+v", b)
		}
	})
}
