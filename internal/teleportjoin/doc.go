// Package teleportjoin is the server-side relay of feature 038: it turns a
// source's enrollment request (references only: tenant, enrollment id, host,
// Teleport cluster, expiry) into one persisted item for the host's enrolled
// Linux agent, pushes TELEPORT_JOIN commands carrying the item id to the
// online agent and replays them on reconnect, and, when the agent fetches
// its item, asks the source (the teleport module, TeleportJoinSource over
// the mesh) to render the join material at that moment and relays it. It
// records the agents' reports (installing, joined with the node's host
// uuid, failed) and sweeps items whose report or window lapsed.
//
// Security role: the join material (a single-use Teleport provision token,
// role Node) exists only in the memory of the fetch call; no column,
// command, event, log or audit detail holds the token, the CA pins or the
// labels. Fetches and reports are bound to the verified agent's own active
// item, and the source is told the verified agent's host. Every transition
// is audited in the transaction of the state change. The package is pure
// over interfaces and held to 100 % test coverage.
package teleportjoin
