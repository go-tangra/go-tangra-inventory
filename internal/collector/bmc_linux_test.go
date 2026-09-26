//go:build linux

package collector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
)

// fakeIPMI records every request. Channel 1 is LAN, 2 is not, 3 is LAN with
// an IP.
type fakeIPMI struct {
	mu        sync.Mutex
	channels  []uint8
	selectors map[uint8]int
	closed    bool
	block     chan struct{}
}

func (f *fakeIPMI) ChannelIsLAN(_ context.Context, ch uint8) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels = append(f.channels, ch)
	return ch == 1 || ch == 3, nil
}

func (f *fakeIPMI) LanParam(ctx context.Context, ch, sel uint8) ([]byte, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selectors[sel]++
	switch {
	case ch == 3 && sel == agentfacts.LanParamIP:
		return []byte{10, 0, 0, 5}, nil
	case ch == 3 && sel == agentfacts.LanParamSubnetMask:
		return []byte{255, 255, 255, 0}, nil
	case sel == agentfacts.LanParamMAC:
		return []byte{0, 0xaa, 0xbb, 0xcc, 0xdd, ch}, nil
	case sel == agentfacts.LanParamVLANID:
		return nil, errors.New("not supported")
	}
	return []byte{0, 0, 0, 0}, nil
}

func (f *fakeIPMI) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func newFake() *fakeIPMI { return &fakeIPMI{selectors: map[uint8]int{}} }

// Negative (SR-005): the requested commands are exactly channel info plus
// LAN parameters {3,4,5,6,12,20}; never 16 or any other selector.
func TestReadBMCRequestsOnlyAllowedParameters(t *testing.T) {
	f := newFake()
	b, dropped := readBMC(context.Background(), func(context.Context) (ipmiLAN, error) { return f, nil }, time.Second)
	if b == nil || b.Address != "10.0.0.5" || b.PrefixLength != 24 || len(b.Ports) != 2 || dropped != 0 {
		t.Fatalf("bmc = %+v", b)
	}
	if len(f.channels) != maxLanChannel {
		t.Fatalf("channels probed = %v", f.channels)
	}
	allowed := map[uint8]bool{3: true, 4: true, 5: true, 6: true, 12: true, 20: true}
	for sel, n := range f.selectors {
		if !allowed[sel] {
			t.Errorf("selector %d requested", sel)
		}
		if n != 2 { // two LAN channels
			t.Errorf("selector %d requested %d times", sel, n)
		}
	}
	if len(f.selectors) != len(allowed) || f.selectors[16] != 0 {
		t.Fatalf("selectors = %v", f.selectors)
	}
	if !f.closed {
		t.Fatal("client not closed")
	}
}

// The real adapter refuses any non-allowed selector before touching the
// device (a nil client would panic otherwise).
func TestGoIPMIRefusesForbiddenSelectors(t *testing.T) {
	g := goIPMI{}
	for sel := 0; sel < 256; sel++ {
		if agentfacts.IsAllowedLanParam(uint8(sel)) {
			continue
		}
		if _, err := g.LanParam(context.Background(), 1, uint8(sel)); !errors.Is(err, errForbiddenLanParam) {
			t.Fatalf("selector %d: %v", sel, err)
		}
	}
}

// Static guard: the BMC collector never references bulk LAN reads (which
// include parameter 16), user/password/cipher/session commands or the
// community string.
func TestBMCSourceHasNoCredentialCalls(t *testing.T) {
	src, err := os.ReadFile("bmc_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := regexp.MustCompile(`GetLanConfig\(|GetLanConfigParams|GetLanConfigParamFor|SetLanConfig|GetUser|SetUser|Password|Cipher|CommunityString|Session|Username`)
	if m := forbidden.Find(src); m != nil {
		t.Fatalf("bmc_linux.go references %q", m)
	}
}

func TestCollectBMCDisabledOrNoDevice(t *testing.T) {
	opened := 0
	prevOpen, prevDev := bmcOpener, ipmiDevices
	t.Cleanup(func() { bmcOpener, ipmiDevices = prevOpen, prevDev })
	bmcOpener = func(context.Context) (ipmiLAN, error) { opened++; return newFake(), nil }

	dev := filepath.Join(t.TempDir(), "ipmi0")
	ipmiDevices = []string{dev}
	if b, _ := collectBMC(context.Background(), true); b != nil || opened != 0 {
		t.Fatalf("no device: bmc=%+v opened=%d", b, opened)
	}
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := collectBMC(context.Background(), false); b != nil || opened != 0 {
		t.Fatalf("collect_bmc false: bmc=%+v opened=%d", b, opened)
	}
	if b, _ := collectBMC(context.Background(), true); b == nil || opened != 1 {
		t.Fatalf("enabled: bmc=%+v opened=%d", b, opened)
	}
}

func TestReadBMCTimeoutAndOpenFailure(t *testing.T) {
	f := newFake()
	f.block = make(chan struct{})
	defer close(f.block)
	start := time.Now()
	b, _ := readBMC(context.Background(), func(context.Context) (ipmiLAN, error) { return f, nil }, 50*time.Millisecond)
	if b != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout: bmc=%+v after %v", b, time.Since(start))
	}
	if b, _ := readBMC(context.Background(), func(context.Context) (ipmiLAN, error) { return nil, errors.New("EACCES") }, time.Second); b != nil {
		t.Fatalf("open failure: %+v", b)
	}
}
