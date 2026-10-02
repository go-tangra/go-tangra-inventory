// Package agentcerts is the agent side of feature 033: the certificate store
// under the locally configured directory (certbot layout: live/<name> is a
// symlink to archive/<name>/<generation>), the idempotency check, the atomic
// install with generation pruning and crash recovery, and the deploy hook
// (environment builder and file checks).
//
// Security role: the server chooses only the certificate name, re-validated
// here as a single path component; directory, owner, modes and the hook come
// from the local configuration only; directories are opened without
// following symlinks; a key is written with the configured restrictive mode
// before it becomes visible. The pure core runs over a fake filesystem and a
// fake executor and is held to 100 % test coverage; the OS glue
// (*_linux.go: ownership, process groups, user lookups) is excluded from the
// gate and covered by the privileged container test (make test-agent-certs).
package agentcerts
