//go:build !linux

package collector

import "github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"

// platformVirtFacts: only SMBIOS is used outside Linux.
func platformVirtFacts(*agentfacts.VirtFacts) {}
