// Package selfupdate is the agent-side core of self-upgrade (feature 023): it
// stages a release artifact streamed from the inventory module into a private
// directory, verifies it with internal/agentrelease (signature, platform,
// version, size, sha256) before anything is replaced, refuses downgrades
// without an administrator pin and below the compiled floor, serialises
// upgrades with a lock, persists the upgrade state file and decides on start
// whether to confirm the new version or report a rollback. The OS side
// effects (package manager, systemd, Windows service control, file renames)
// are injected through the Installer, Runner and FS interfaces implemented in
// internal/upgrader.
//
// Security role: nothing unverified is ever handed to an installer; the only
// upgrade source is the agent's own authenticated ingest connection (the
// Client interface), never a URL. The package is held to 100 % test coverage.
package selfupdate
