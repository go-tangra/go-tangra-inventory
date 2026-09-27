// Package agentrelease is the trust root of agent self-upgrade (feature 023):
// the strict parser of the signed release manifest (agent-release.json), the
// compiled Ed25519 keyring, signature verification over the exact manifest
// bytes, platform selection and semantic version comparison.
//
// Security role: both the inventory server (bundle import) and the agent
// (before anything is installed) call Verify; an artifact whose manifest does
// not verify against a compiled key, whose entry does not match the agent's
// os/arch/install type or whose size or sha256 differs is refused. The
// private signing key never reaches this package or the platform runtime —
// it lives only in the release pipeline. A development key is compiled in
// only under the build tag agentdevkey (tests, local stacks); release builds
// never carry it (scripts/check-release-binary.sh). The package is pure (no
// I/O) and held to 100 % test coverage and fuzzed.
package agentrelease
