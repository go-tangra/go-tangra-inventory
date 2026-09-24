//go:build !linux && !windows

package collector

import "github.com/go-tangra/go-tangra-inventory/v4/internal/store"

// collectPrograms is not implemented outside Linux and Windows.
func collectPrograms() []store.Program { return nil }

// collectServices is not implemented outside Linux and Windows.
func collectServices() []store.Service { return nil }

// collectUsers is not implemented outside Linux and Windows.
func collectUsers() []store.UserAccount { return nil }
