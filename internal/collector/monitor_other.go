//go:build !windows

package collector

import "github.com/go-freya/freya/services/inventory/internal/store"

// collectMonitors has no portable EDID source outside Windows.
func collectMonitors() []store.Monitor { return nil }
