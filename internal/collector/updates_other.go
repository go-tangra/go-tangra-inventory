//go:build !linux

package collector

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// collectUpdates is Linux only: elsewhere the state is unknown and the
// programs are unchanged.
func collectUpdates(_ context.Context, _ Options, programs []store.Program) ([]store.Program, store.UpdateState, uint32) {
	return programs, store.UpdateState{Status: store.UpdateUnknown, RebootRequired: store.TriUnknown, AutomaticUpdates: store.TriUnknown}, 0
}
