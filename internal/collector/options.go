package collector

import (
	"path/filepath"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
)

// Options select the optional host report collections.
type Options struct {
	CollectBMC          bool
	CollectUpdates      bool
	RefreshPackageLists bool
	UpdateTimeout       time.Duration
	// StateDir holds the package-list refresh timestamp ("" = in memory only).
	StateDir string
}

// DefaultOptions are the agent defaults: BMC and updates read, package lists
// never refreshed, 120 s update budget.
func DefaultOptions() Options {
	return OptionsFrom(config.DefaultAgent())
}

// OptionsFrom maps the agent configuration to collector options.
func OptionsFrom(cfg config.AgentConfig) Options {
	o := Options{
		CollectBMC: cfg.CollectBMC, CollectUpdates: cfg.CollectUpdates,
		RefreshPackageLists: cfg.RefreshPackageLists, UpdateTimeout: cfg.UpdateTimeout(),
	}
	switch {
	case cfg.StateFile != "":
		o.StateDir = filepath.Dir(cfg.StateFile)
	case cfg.CredentialFile != "":
		o.StateDir = filepath.Dir(cfg.CredentialFile)
	}
	return o
}
