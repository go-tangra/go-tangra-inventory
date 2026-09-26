//go:build !linux

package collector

import "github.com/go-tangra/go-tangra-inventory/v4/internal/store"

// collectGuests is Linux (Proxmox) only.
func collectGuests() ([]store.HypervisorGuest, uint32) { return nil, 0 }
