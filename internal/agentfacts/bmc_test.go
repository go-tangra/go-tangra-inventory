package agentfacts

import (
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func lanParams(ip, mask, gw, mac []byte, src byte, vlan []byte) map[uint8][]byte {
	m := map[uint8][]byte{}
	if ip != nil {
		m[LanParamIP] = ip
	}
	if mask != nil {
		m[LanParamSubnetMask] = mask
	}
	if gw != nil {
		m[LanParamDefaultGateway] = gw
	}
	if mac != nil {
		m[LanParamMAC] = mac
	}
	m[LanParamIPSource] = []byte{src}
	if vlan != nil {
		m[LanParamVLANID] = vlan
	}
	return m
}

func TestAllowedLanParams(t *testing.T) {
	want := []uint8{3, 4, 5, 6, 12, 20}
	if len(AllowedLanParams) != len(want) {
		t.Fatalf("allowed = %v", AllowedLanParams)
	}
	for i, p := range want {
		if AllowedLanParams[i] != p {
			t.Fatalf("allowed = %v", AllowedLanParams)
		}
	}
	for _, forbidden := range []uint8{1, 2, 7, 16, 17, 18, 19, 22, 23, 24} { // auth, community string, cipher suites, …
		if IsAllowedLanParam(forbidden) {
			t.Errorf("parameter %d must never be read", forbidden)
		}
	}
}

func TestDecodeBmc(t *testing.T) {
	chans := []BmcChannel{
		{Channel: 1, Params: lanParams([]byte{0, 0, 0, 0}, nil, nil, []byte{0, 0, 0, 0, 0, 0}, 1, nil)}, // no IP, zero MAC
		{Channel: 2, Params: lanParams([]byte{10, 0, 0, 5}, []byte{255, 255, 255, 0}, []byte{10, 0, 0, 1},
			[]byte{0x00, 0xaa, 0xbb, 0xcc, 0xdd, 0xee}, 2, []byte{0x2c, 0x81})}, // dhcp, vlan 300 enabled
		{Channel: 3, Params: lanParams([]byte{10, 0, 1, 5}, []byte{255, 255, 0, 0}, nil,
			[]byte{0x00, 0xaa, 0xbb, 0xcc, 0xdd, 0xef}, 1, nil)},
	}
	b, dropped := DecodeBmc(chans)
	if dropped != 0 || b == nil {
		t.Fatalf("bmc = %+v dropped %d", b, dropped)
	}
	if b.Address != "10.0.0.5" || b.PrefixLength != 24 || b.Gateway != "10.0.0.1" || b.IPSource != "dhcp" || b.VLANID != 300 {
		t.Fatalf("first channel with an IP must win: %+v", b)
	}
	if len(b.Ports) != 2 || b.Ports[0] != (store.BmcPort{Channel: 2, MAC: "00:aa:bb:cc:dd:ee", Address: "10.0.0.5"}) ||
		b.Ports[1] != (store.BmcPort{Channel: 3, MAC: "00:aa:bb:cc:dd:ef", Address: "10.0.1.5"}) {
		t.Fatalf("ports = %+v", b.Ports)
	}
}

func TestDecodeBmcEdgeCases(t *testing.T) {
	if b, _ := DecodeBmc(nil); b != nil {
		t.Fatal("no channel → nil")
	}
	// Only zero values → nil (nothing to report).
	if b, _ := DecodeBmc([]BmcChannel{{Channel: 1, Params: lanParams([]byte{0, 0, 0, 0}, nil, nil, nil, 0, nil)}}); b != nil {
		t.Fatalf("zero bmc = %+v", b)
	}
	// Truncated/short data, non-contiguous mask, disabled VLAN, unknown source.
	b, _ := DecodeBmc([]BmcChannel{{Channel: 1, Params: lanParams([]byte{192, 168, 1, 9}, []byte{255, 0, 255, 0}, []byte{1, 2},
		[]byte{1, 2, 3}, 9, []byte{0x05, 0x00})}})
	if b == nil || b.Address != "192.168.1.9" || b.PrefixLength != 0 || b.Gateway != "" || b.IPSource != "other" || b.VLANID != 0 || len(b.Ports) != 1 || b.Ports[0].MAC != "" {
		t.Fatalf("edge = %+v", b)
	}
	for src, want := range map[byte]string{0: "", 1: "static", 2: "dhcp", 3: "bios", 4: "other", 0xf1: "static"} {
		if got := ipSource([]byte{src}); got != want {
			t.Errorf("source %d = %q", src, got)
		}
	}
	if ipSource(nil) != "" {
		t.Fatal("empty source")
	}
	// A zero gateway is no gateway; a MAC-only channel still yields a port.
	b, _ = DecodeBmc([]BmcChannel{{Channel: 4, Params: lanParams(nil, nil, []byte{0, 0, 0, 0}, []byte{2, 0, 0, 0, 0, 1}, 0, nil)}})
	if b == nil || b.Address != "" || b.Gateway != "" || len(b.Ports) != 1 {
		t.Fatalf("mac only = %+v", b)
	}
}

func TestDecodeBmcPortBound(t *testing.T) {
	var chans []BmcChannel
	for i := 1; i <= 11; i++ {
		chans = append(chans, BmcChannel{Channel: uint8(i), Params: lanParams(nil, nil, nil, []byte{2, 0, 0, 0, 0, byte(i)}, 0, nil)})
	}
	b, dropped := DecodeBmc(chans)
	if len(b.Ports) != store.MaxBmcPorts || dropped != 3 {
		t.Fatalf("ports = %d dropped = %d", len(b.Ports), dropped)
	}
}
