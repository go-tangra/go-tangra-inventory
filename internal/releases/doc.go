// Package releases makes signed agent releases available to agents (feature
// 023): it verifies release bundles shipped in the inventory image (or given
// to `inventorysvc agent-release import`) against the compiled keyring,
// stores them in PostgreSQL in 1 MiB chunks idempotently across replicas,
// applies retention (never deleting the platform current version, a tenant
// pin or an active upgrade target) and streams artifacts back in order.
//
// Security role: an unsigned, unknown-key, tampered, truncated or
// path-traversing bundle is refused before any row is written and the
// refusal is audited; only verified artifacts are ever offered to agents.
package releases
