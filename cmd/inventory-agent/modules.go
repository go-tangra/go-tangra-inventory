package main

import (
	"errors"
	"log"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentmodules"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/daemon"
)

// moduleWriter opens the local modules directory (feature 037) and removes
// writes interrupted by a crash. nil — no module.v1 capability — when
// module delivery is disabled locally, on platforms other than Linux, or
// when the configured directory cannot be used safely.
func moduleWriter(c config.AgentModules, deps agentmodules.Deps) daemon.ModuleInstaller {
	if !c.Enabled {
		log.Println("modules: delivery disabled in the local configuration")
		return nil
	}
	w, err := agentmodules.NewOS(agentmodules.ConfigFrom(c), deps)
	if err != nil {
		if !errors.Is(err, agentmodules.ErrUnsupported) {
			log.Printf("modules: directory %s unavailable, delivery not announced: %v", c.Directory, err)
		}
		return nil
	}
	if err := w.Recover(); err != nil {
		log.Printf("modules: directory %s unsafe, delivery not announced: %v", c.Directory, err)
		return nil
	}
	return w
}
