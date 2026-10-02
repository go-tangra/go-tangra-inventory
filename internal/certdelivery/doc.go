// Package certdelivery is the server-side relay of feature 033: it turns a
// deployer delivery request (references only: tenant, job id, lcm
// certificate id, name, host selection, key policy) into one persisted
// delivery item per host, pushes CERTIFICATE commands carrying item ids to
// online agents and replays them on reconnect, serves the material to the
// owning agent at fetch time (downloaded from lcm, validated, relayed and
// dropped), records the agents' reports, and sweeps, re-arms, cancels,
// verifies and flags items.
//
// Security role: the private key never leaves the fetch call's memory (no
// column, cache, event, log or audit detail holds material); fetches and
// reports are bound to the verified agent's own active item; every
// transition is audited in the transaction of the state change. The package
// is pure over repo interfaces and held to 100 % test coverage.
package certdelivery
