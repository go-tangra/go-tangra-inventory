// Package certmaterial holds the pure certificate-material rules of feature
// 033 shared by the inventory service and the inventory agent: the
// certificate name rule (a single safe path component), host tag selectors,
// and the PEM bundle parser that checks sizes, the leaf and chain, the
// private key against the leaf and the validity window, and derives the
// fingerprint, serial and SANs.
//
// Security role: the name rule is what keeps a server-supplied name from
// escaping the agent's certificate directory; the bundle parser is what the
// service runs on lcm material before relaying it and what the agent runs
// before writing anything. The package does no I/O, depends on the standard
// library only, never logs or returns key bytes in errors, and is held to
// 100 % test coverage.
package certmaterial
