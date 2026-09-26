//go:build linux

package collector

import (
	"context"
	"errors"
	"os"
	"time"

	ipmi "github.com/bougou/go-ipmi"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// bmcBudget bounds the whole BMC read.
const bmcBudget = 10 * time.Second

// maxLanChannel is the highest IPMI channel number probed (1-11 are the
// implementation-specific channels; 0 is IPMB, 14/15 are special).
const maxLanChannel = 11

// ipmiLAN is the only IPMI surface the agent uses: channel medium lookup and
// single LAN configuration parameters. There is deliberately no bulk "get
// all LAN parameters", user, password, session or cipher operation.
type ipmiLAN interface {
	ChannelIsLAN(ctx context.Context, channel uint8) (bool, error)
	LanParam(ctx context.Context, channel, selector uint8) ([]byte, error)
	Close(ctx context.Context) error
}

type ipmiOpener func(ctx context.Context) (ipmiLAN, error)

// Overridable in tests.
var (
	ipmiDevices            = []string{"/dev/ipmi0", "/dev/ipmi/0", "/dev/ipmidev/0"}
	bmcOpener   ipmiOpener = openIPMI
)

var errForbiddenLanParam = errors.New("collector: LAN parameter not allowed")

// collectBMC reads the BMC LAN settings in-band (OpenIPMI) when enabled and a
// device node exists (root, ipmi_devintf + ipmi_si loaded; the agent never
// loads modules). Any failure or timeout reports no BMC.
func collectBMC(ctx context.Context, enabled bool) (*store.Bmc, uint32) {
	if !enabled || !ipmiDevicePresent() {
		return nil, 0
	}
	return readBMC(ctx, bmcOpener, bmcBudget)
}

func ipmiDevicePresent() bool {
	for _, p := range ipmiDevices {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// readBMC probes channels 1-11 and, on LAN channels only, requests exactly
// the parameters in agentfacts.AllowedLanParams, within budget.
func readBMC(ctx context.Context, open ipmiOpener, budget time.Duration) (*store.Bmc, uint32) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	type result struct {
		b       *store.Bmc
		dropped uint32
	}
	done := make(chan result, 1)
	go func() {
		c, err := open(ctx)
		if err != nil {
			done <- result{}
			return
		}
		var chans []agentfacts.BmcChannel
		for ch := uint8(1); ch <= maxLanChannel && ctx.Err() == nil; ch++ {
			lan, err := c.ChannelIsLAN(ctx, ch)
			if err != nil || !lan {
				continue
			}
			params := map[uint8][]byte{}
			for _, sel := range agentfacts.AllowedLanParams {
				if d, err := c.LanParam(ctx, ch, sel); err == nil {
					params[sel] = d
				}
			}
			chans = append(chans, agentfacts.BmcChannel{Channel: ch, Params: params})
		}
		_ = c.Close(ctx)
		b, dropped := agentfacts.DecodeBmc(chans)
		done <- result{b, dropped}
	}()
	select {
	case r := <-done:
		return r.b, r.dropped
	case <-ctx.Done():
		return nil, 0
	}
}

// goIPMI adapts the go-ipmi OpenIPMI client to ipmiLAN.
type goIPMI struct{ c *ipmi.Client }

func openIPMI(ctx context.Context) (ipmiLAN, error) {
	c, err := ipmi.NewOpenClient()
	if err != nil {
		return nil, err
	}
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	return goIPMI{c: c}, nil
}

func (g goIPMI) ChannelIsLAN(ctx context.Context, channel uint8) (bool, error) {
	r, err := g.c.GetChannelInfo(ctx, channel)
	if err != nil {
		return false, err
	}
	return r.ChannelMedium == ipmi.ChannelMediumLAN, nil
}

// LanParam reads one LAN configuration parameter (set/block selector 0). It
// refuses any selector outside the allow-list before touching the device.
func (g goIPMI) LanParam(ctx context.Context, channel, selector uint8) ([]byte, error) {
	if !agentfacts.IsAllowedLanParam(selector) {
		return nil, errForbiddenLanParam
	}
	r, err := g.c.GetLanConfigParam(ctx, channel, ipmi.LanConfigParamSelector(selector), 0, 0)
	if err != nil {
		return nil, err
	}
	return r.ParamData, nil
}

func (g goIPMI) Close(ctx context.Context) error { return g.c.Close(ctx) }
