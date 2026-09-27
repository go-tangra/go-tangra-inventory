//go:build !linux && !windows

package collector

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// physicalDisks has no implementation on this platform.
func physicalDisks(context.Context) (agentfacts.BlockDevices, agentfacts.DiskResolver) {
	return agentfacts.BlockDevices{Availability: store.AvailUnsupported}, nil
}
