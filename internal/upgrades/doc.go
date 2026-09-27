// Package upgrades is the server-side lifecycle of agent upgrade requests
// (feature 023): creation for one agent, a selection or all outdated agents,
// delivery over the command stream (on creation and on every reconnect),
// the state machine driven by the agents' reports, expiry and stale-progress
// sweepers, the derived per-agent fleet state and the optional per-tenant
// automatic upgrade policy with its scheduler.
//
// Security role: requests are persisted and every transition is audited in
// the same transaction; reports and downloads are bound to the verified
// agent's own active request; downgrades are only requested for an
// administrator pin. The package is pure over repo interfaces and held to
// 100 % test coverage.
package upgrades
