# Feature Specification: Teleport Join Delivery Through the Inventory Agent

**Feature Branch**: `038-teleport-join-delivery`
**Created**: 2026-11-20
**Status**: Implemented (inventory side)
**Input**: the teleport module enrolls inventory hosts as Teleport SSH nodes
through their enrolled agents.

Repositories: go-tangra-inventory-v4 (this spec: SDK contract, mesh service,
ingest edge, agent) and go-tangra-teleport (the requesting module and the
join source: `specs/002-agent-ssh-enrollment`, which owns the user
stories, the token minting and the console). Mirrors 037 (module delivery:
reference-only commands, material pulled at fetch time from the source,
never stored) and the 033 rules it defers to (fetch budget, plaintext
ingest refusal, refusal throttle, audit in the same transaction).

## Context

Joining a host to Teleport means copying a short-lived provision token,
installing the right Teleport release and writing its configuration by
hand. The inventory agent already holds a per-host credential and an
authenticated command stream, so the platform trusts it to identify the
host. The teleport module asks inventory to deliver a join; the agent does
the rest.

## Requirements

### Contract (done, `sdk/api/proto/inventory/v1/inventory.proto`)

- `TeleportJoinDeliveryService` (mesh, called by the teleport module):
  `CreateTeleportJoin`, `GetTeleportJoins`, `CancelTeleportJoin`,
  `ListTeleportJoinTargets`. References only; no response carries a token.
- `TeleportJoinSource/RenderTeleportJoin` (served by the teleport module,
  called by inventory when an agent fetches).
- Ingest edge: `FetchTeleportJoin` (unary, returns `TeleportJoinMaterial`)
  and `ReportTeleportJoin`; command `COMMAND_TYPE_TELEPORT_JOIN` carrying
  the item id and attempt; capability `teleport.v1`.

### Server

- **FR-001** Config `teleport_join`: `enabled` (false), `sources`
  (`[teleport]`, at most 32 service names), `source_service` (`teleport`),
  `report_timeout_minutes` (30, 15-180), `max_concurrent_fetches` (20,
  1-500), `source_timeout_seconds` (10, 2-120), `allow_plaintext_ingest`
  (false; refused in production). Disabled: every mesh method answers
  `FailedPrecondition` `disabled_on_server`.
- **FR-002** Every mesh RPC requires a SPIFFE service peer listed in
  `sources` (refusal audited `teleport_join_refused` /
  `source_not_allowed` when the tenant is a uuid), on top of the policy
  rule `teleport-join` (svc/teleport → `TeleportJoinDeliveryService/*` and
  health).
- **FR-003** Create: tenant, enrollment id, host id, cluster id (uuids),
  expiry (bounded to 7 days). The host must be eligible: one non-revoked
  agent on Linux announcing `teleport.v1`; otherwise `FailedPrecondition`
  with the reason token (`upgrade_required`, `not_supported_platform`,
  `no_agent`, `ambiguous_agent`, `retired`), nothing stored. Idempotent by
  (tenant, source, enrollment id): a replay returns the item with
  `created=false`. A new enrollment for the same (host, cluster)
  supersedes the active one.
- **FR-004** GetTeleportJoins: 1-200 enrollment ids; unknown or malformed
  ids omitted. CancelTeleportJoin: active → `cancelled`; terminal items
  returned unchanged. ListTeleportJoinTargets: by query or host ids, with
  host facts (hostname, OS name/version/arch, manufacturer, model, tags),
  agent online and capability.
- **FR-005** Push `TELEPORT_JOIN` to the online agent; replay active
  unfetched items on connect (at most 50); the sweeper expires unfetched
  items past `expires_at` and fails fetched/installing items without a
  report after `report_timeout_minutes` (`report_timeout`).
- **FR-006** FetchTeleportJoin: only the caller's own active item; over a
  plaintext edge refused unless `allow_plaintext_ingest`; at most 5
  fetches, each counted. Material from `RenderTeleportJoin` is relayed
  with `item_id` set and never stored; `NotFound` from the source fails the
  item (`source_refused`); a spent budget fails it (`fetch_budget`); other
  source errors answer `Unavailable`.
- **FR-007** ReportTeleportJoin: states `installing` (after a fetch),
  `joined` (with the node's lowercase host uuid) and `failed` (with a
  reason from the closed set); a report for a terminal item or a repeat
  answers `accepted=false`, except a repeated `installing`, which answers
  `accepted=true` without a change while the item is installing (the
  agent's "is this item still active?" check; the report timeout still
  runs from the first one); `detail` is sanitised and at most 256 bytes.
- **FR-008** Every transition and refusal writes an audit row in the same
  transaction (`teleport_join_created`, `_superseded`, `_delivered`,
  `_fetched`, `_installing`, `_joined`, `_failed`, `_unsupported`,
  `_expired`, `_cancelled`, `_refused`) with ids, state and reason only.
  Storage (`inventory_teleport_join_items`, migration 0013, RLS by tenant)
  holds no token material.

### Agent

- **FR-010** Config `teleport`: `enabled` (true), `timeout_minutes` (15,
  5-60), `allow_insecure_transport` (false). A Linux agent announces
  `teleport.v1` regardless of `enabled`; disabled (or plaintext without the
  opt-out) → `failed/disabled_locally`.
- **FR-011** Not Linux, no systemd, or not amd64/arm64 →
  `unsupported_platform`. One join at a time; another → `busy`.
  `/etc/teleport.yaml` present (other than a token-free one the agent left
  after a failure), a packaged teleport unit, or the unit active or
  enabled → `already_installed`. These checks run before fetching.
- **FR-012** Material validation (`invalid_material`): version
  `^\d{1,4}\.\d{1,4}\.\d{1,4}$`; proxy `host:port` with a DNS name or IP,
  port 1-65535, not loopback/link-local/multicast/unspecified; 1-4 CA pins
  `^sha256:[0-9a-f]{64}$`; token printable ASCII without whitespace, at
  most 256 bytes; at most 64 labels, keys `^tangra\.io/[A-Za-z0-9._/-]{1,54}$`,
  values printable UTF-8 at most 255 bytes.
- **FR-013** Report `installing`; a not-accepted report aborts the join.
  Download `https://cdn.teleport.dev/teleport-v<version>-linux-<arch>-bin.tar.gz`
  and its `.sha256` (fixed origin; injectable only in tests; at most
  512 MiB) → `download_failed` / `checksum_mismatch`. Extract only the
  regular file `teleport/teleport` and install it atomically to
  `/usr/local/bin/teleport` (0755) → `install_failed`.
- **FR-014** Write `/etc/teleport.yaml` (0600, yaml.v3, `version: v3`,
  nodename = hostname, `data_dir: /var/lib/teleport`, `join_params`
  token, `proxy_server`, `ca_pin`, `diag_addr: 127.0.0.1:3434` (loopback
  diagnostics), auth/proxy services off, ssh_service on with the labels)
  and `/etc/systemd/system/teleport.service`
  (`ExecStart=/usr/local/bin/teleport start --config=/etc/teleport.yaml`,
  `Restart=on-failure`); `systemctl daemon-reload`, `systemctl enable
  --now teleport` (fixed argv and environment, no shell).
- **FR-015** Wait until the unit is active, Teleport's
  `http://127.0.0.1:3434/readyz` answers 200 (the node registered and is
  connected; the host uuid file is written before that) and
  `/var/lib/teleport/host_uuid` holds a uuid → `joined` with
  `node_host_uuid`; otherwise `timeout` after `timeout_minutes`;
  systemctl failures → `join_failed`. Every 30 s of the wait the agent
  reports `installing` again; `accepted=false` (the item was cancelled or
  failed meanwhile) rolls the join back as in FR-016 and nothing more is
  reported.
- **FR-016** Any failure after the configuration was written rewrites it
  with an empty `token_name` (or removes it) and stops and disables the
  unit. The token is never logged, reported or left in a report detail.

## Success Criteria

- **SC-001** No token material in the inventory database, audit, logs or
  metrics; the agent leaves no token on disk after a failed join.
- **SC-002** Every agent failure reason is covered by a unit test over the
  injectable filesystem root, systemctl runner, HTTP client and clock.
- **SC-003** `internal/teleportjoin` and `internal/agentteleport` are at
  100 % in the coverage gate (OS glue `*_linux.go` excluded).
