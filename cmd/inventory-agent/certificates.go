package main

import (
	"errors"
	"log"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentcerts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/daemon"
)

// certificateStore opens the local certificate store (feature 033) and
// cleans up after a crash. nil — no cert.v1 capability — when certificate
// delivery is disabled locally, on platforms other than Linux, or when the
// configured directory cannot be used.
func certificateStore(c config.AgentCertificates, deps agentcerts.Deps) daemon.CertInstaller {
	if !c.Enabled {
		log.Println("certificates: delivery disabled in the local configuration")
		return nil
	}
	st, err := agentcerts.NewOS(agentcerts.ConfigFrom(c), deps)
	if err != nil {
		if !errors.Is(err, agentcerts.ErrUnsupported) {
			log.Printf("certificates: store %s unavailable, delivery not announced: %v", c.Directory, err)
		}
		return nil
	}
	if err := st.Recover(); err != nil {
		log.Printf("certificates: store %s unsafe, delivery not announced: %v", c.Directory, err)
		return nil
	}
	return st
}
