# Contract: agent CLI, upgrade helper and release tooling (023)

## `inventory-agent update`

```text
inventory-agent update [-check] [-config /etc/inventory-agent/agent.yaml]
                       [-ingest host:port] [-ca-file path] [-server-name name] [-insecure]
```

- Subcommand detected when `os.Args[1] == "update"`; all existing flag
  modes (`-daemon`, `-service`, `-o`, one-shot) are unchanged.
- Requires root (Linux) / Administrator (Windows) and a persisted
  credential (enrolled agent); otherwise exit 1 with
  `error: update requires an enrolled agent (credential_file)`.
- `-check`: calls `CheckAgentUpdate(apply=false)`; prints
  `current 4.4.0, available 4.5.0 (linux/amd64 deb)` or
  `current 4.5.0, up to date`; exit **0** up to date, **10** upgrade
  available, **1** error.
- Without `-check`: `CheckAgentUpdate(apply=true)` (server creates an
  audited request, origin `agent`), then the same `selfupdate` flow as a
  server-pushed upgrade (download, verify, stage, install via helper,
  confirm or roll back). Prints progress lines; exit 0 when the helper was
  started (the result is visible in the agent list and in
  `/var/lib/inventory-agent/upgrade/state.json`), 1 on any error before
  the install (nothing changed), 2 when another upgrade holds the lock.
- Never contacts anything but the configured ingest endpoint (SR-002).

## `inventory-agent upgrade-apply` (internal)

```text
<staged helper> upgrade-apply -state <staging>/state.json
```

Started only by the agent itself (transient systemd unit
`inventory-agent-upgrade-<request8>` on Linux; detached process on
Windows). Refuses to run unless the state file is owned by root/SYSTEM,
mode 0600, inside the configured staging directory, and its artifact
re-verifies (sha256 + manifest signature). Steps and rollback: research
D10. Writes the state file transitions `installing → awaiting_confirm →
confirmed | rolling_back → rolled_back`.

### State file (`state.json`, 0600)

```json
{"request_id": "uuid", "from_version": "4.4.0", "to_version": "4.5.0",
 "install_type": "deb", "artifact": "<staging>/4.5.0/tangra-inventory-agent_4.5.0_amd64.deb",
 "artifact_sha256": "…", "rollback_artifact": "<staging>/4.4.0/…deb", "rollback_sha256": "…",
 "previous_binary": "/usr/bin/inventory-agent.prev",
 "phase": "awaiting_confirm", "deadline": "2026-10-02T10:05:00Z", "reason": ""}
```

## Agent startup behaviour

On start, if `state.json` exists: phase `awaiting_confirm` and own version
= `to_version` → after the first successful `StreamCommands` connect and
`SubmitInventory`, `ReportUpgrade(succeeded)` then phase `confirmed`;
phase `rolled_back` and own version = `from_version` →
`ReportUpgrade(rolled_back, reason)`; phase `confirmed` → cleanup.

## `cmd/agent-release` (release tooling, not shipped in the image)

```text
agent-release keygen  -out-private <file> -key-id <id>      # prints the public key (base64) for internal/agentrelease/keys.go
agent-release sign    -key-env AGENT_RELEASE_SIGNING_KEY -key-id <id> -version <v> -dir dist/agent
                      # writes dist/agent/agent-release.json + agent-release.json.sig
agent-release verify  -dir dist/agent [-version <v>]         # verifies with the compiled keyring; exit 1 on any mismatch
```

Standard library only (`crypto/ed25519`, `crypto/sha256`,
`encoding/json`). The private key is read from the environment variable
named by `-key-env` (base64 seed), never from a flag value or file in CI.

## Service CLI

```text
inventorysvc agent-release import -config deploy/container.yaml <dir>
```

Imports a signed release directory (manifest, signature, artifacts) into
the database after verification (offline sites, other versions); prints
version, key id and artifact list; audit `agent_release_imported`.

## Make targets

- `make agent-release AGENT_VERSION=x.y.z` — builds the 8 artifacts into
  `dist/agent/` (deb/rpm via nfpm, raw Linux binaries, Windows exes, all
  version-stamped).
- `make agent-release-dev` — same + signs with the dev key (build tag
  `agentdevkey`) and copies into `agent-releases/<version>/` for local
  images (freya-stack).
