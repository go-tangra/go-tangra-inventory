package main

import (
	"log"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentteleport"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/daemon"
)

// teleportJoiner builds the Teleport installer (feature 038). nil on
// platforms other than Linux or without systemctl: the agent then reports
// joins as unsupported_platform. A disabled configuration still gets the
// installer; the daemon reports disabled_locally.
func teleportJoiner(c config.AgentTeleport) daemon.TeleportJoiner {
	if !c.Enabled {
		log.Println("teleport: joins disabled in the local configuration")
	}
	in, err := agentteleport.NewOS(c.Timeout())
	if err != nil {
		return nil
	}
	return in
}
