//go:build !linux

package collector

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// collectBMC is Linux only; other platforms report no BMC.
func collectBMC(context.Context, bool) (*store.Bmc, uint32) { return nil, 0 }
