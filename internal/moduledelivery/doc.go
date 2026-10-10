// Package moduledelivery is the server-side relay of feature 037: it turns
// a source's delivery request (references only: tenant, delivery id, host,
// module, version, expiry) into one persisted item for the host's enrolled
// agent, pushes MODULE commands carrying the item id to the online agent and
// replays them on reconnect, and, when the agent fetches its item, asks the
// source (the gateway, ModuleBundleSource over the mesh) to render the
// bundle at that moment, checks its size and digest and relays it. It
// records the agents' reports and sweeps items whose report or window
// lapsed.
//
// Security role: the rendered bundle (a zip holding a join token and store
// passwords) exists only in the memory of the fetch call and is zeroed
// afterwards; no column, command, event, log or audit detail holds any of
// it. Fetches and reports are bound to the verified agent's own active item,
// and the source is told the verified agent's host. Every transition is
// audited in the transaction of the state change. The package is pure over
// interfaces and held to 100 % test coverage.
package moduledelivery
