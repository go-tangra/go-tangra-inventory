//go:build !linux

package agentteleport

import "time"

// NewOS: Teleport joins are Linux (systemd) only.
func NewOS(time.Duration) (*Installer, error) { return nil, ErrUnsupported }
