//go:build !linux && !windows

package collector

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
)

// collectNetwork has no native implementation here: the portable gopsutil
// collector is used.
func collectNetwork(context.Context) (agentfacts.Network, bool) { return agentfacts.Network{}, false }
