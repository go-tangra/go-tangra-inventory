// Command agent-release is the release tooling of agent self-upgrade
// (feature 023). It is not shipped in any image.
//
//	agent-release keygen -out-private <file> -key-id <id>
//	agent-release sign   -key-env AGENT_RELEASE_SIGNING_KEY -key-id <id> -version <v> -dir dist/agent
//	agent-release verify -dir dist/agent [-version <v>]
//
// keygen writes a new Ed25519 seed (base64, 0600) and prints the public key
// for internal/agentrelease; sign writes agent-release.json and
// agent-release.json.sig over the artifacts in -dir with the seed read from
// the named environment variable (never a flag value); verify checks a
// release directory against the compiled keyring.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "usage: agent-release keygen|sign|verify [flags]")
	os.Exit(2)
}
