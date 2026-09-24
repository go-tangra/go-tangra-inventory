//go:build !windows

package collector

import "github.com/go-tangra/go-tangra-inventory/v4/internal/store"

// collectMonitors has no portable EDID source outside Windows.
func collectMonitors() []store.Monitor { return nil }
