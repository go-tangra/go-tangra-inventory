// Package agentteleport is the agent side of feature 038: it checks the
// Teleport join material relayed by the inventory (a single-use provision
// token, the cluster's proxy address, CA pins, the Teleport version and
// static node labels), installs that exact Teleport release from the fixed
// origin https://cdn.teleport.dev, writes /etc/teleport.yaml and the
// teleport systemd unit, starts Teleport as an SSH node and waits until it
// joined the cluster.
//
// Security role: nothing the server sends selects a path, a command or a
// download origin. The material is validated strictly before anything is
// touched (version x.y.z, a public proxy host:port, sha256 CA pins, a
// printable token without whitespace, labels under tangra.io/). The archive
// is verified against the published SHA-256 and size-capped, and only the
// teleport binary is taken from it (matched by name, never extracted by
// path). Configuration and binary are written atomically (temporary file
// created with O_EXCL, then renamed); /etc/teleport.yaml is 0600. Commands
// run with a fixed argv and environment, never through a shell. The token
// lives only in /etc/teleport.yaml: it is never logged or reported, and on
// a failure after the configuration was written it is removed again (the
// configuration is rewritten with an empty token_name and the unit stopped
// and disabled). The core runs over injectable filesystem root, systemctl
// runner, HTTP client and clock, and is held to 100 % test coverage; the
// OS glue lives in installer_linux.go.
package agentteleport
