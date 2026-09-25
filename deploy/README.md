# Inventory service — operations

The **inventory** service is a tenant-scoped IT **asset inventory** platform
module. Endpoint **agents** collect hardware (SMBIOS/DMI), software/OS and
network/storage inventory and report immutable **snapshots** to the central
inventory **server**, which resolves each to a stable **host** identity, keeps
the full time-series history, records what changed between snapshots, and serves
query/diff/statistics/backup over the gateway and service-to-service gRPC, plus a
Module-Federation UI.

Vocabulary note (vs the source project): here the **server** is the `inventory`
module (`inventorysvc`) and the endpoint client is the **agent**
(`inventory-agent`) — one agent per host.

## Two trust planes

- **Query/admin API** — an ordinary Freya mesh module: SPIFFE mTLS
  service-to-service, platform token via the application gateway for the browser
  (`/api/inventory`), gateway-registered (routes/permissions/abilities/nav).
- **Ingest edge** — a separate, network-isolated listener that authenticates
  untrusted off-mesh agents by a per-agent credential obtained through a
  tenant-scoped enrollment token. No shared secrets.

## Running the server

```
inventorysvc -config deploy/container.yaml     # run (applies migrations)
inventorysvc bootstrap -config <cfg>           # apply migrations and exit
```

In the containerized platform stack it comes up with one command; see
`deploy/stack/README.md`. The server:

- enrolls for its mesh SVID (`spiffe://<td>/svc/inventory`) over lcm,
- migrates its TimescaleDB schema (per-tenant RLS; snapshots are a hypertable),
- serves the browser API (via the gateway) and `inventory.v1` gRPC on `:9975`,
  the off-mesh **ingest edge** on `:9977`, and admin health/readiness on `:9810`,
- registers routes/permissions/abilities/nav with the gateway and seeds its API
  permissions into auth,
- runs maintenance workers (mark stale hosts, purge snapshots past retention).

## The agent

`inventory-agent` is a cross-platform (Windows/Linux) endpoint binary:

```
inventory-agent -ingest <host:port> -token <enrollment-token>   # one-shot enroll+collect+submit
inventory-agent -config agent.yaml -daemon                      # enroll once, submit on an interval, hold a refresh stream
inventory-agent -o ./out                                        # collect and write JSON (no submit)
inventory-agent -service install | uninstall                    # Windows service / Linux systemd
```

On first run the agent exchanges an operator-issued **enrollment token** for a
persistent per-agent credential (stored locally, 0600), then submits full
snapshots over authenticated TLS to the ingest edge and holds a reconnecting
command stream for on-demand **refresh**. It collects: hardware via SMBIOS
(BIOS/system/board/chassis/CPU/cache/memory/ports/slots/OEM/BIOS-language),
monitors (EDID, Windows), OS/software/services/users/patches, network interfaces
and disks (gopsutil). Categories unavailable on a platform yield empty sections,
never failures.

## Configuration

Server `container.yaml` sections: `db`, `valkey`, `kek` (envelope key),
`ingest` (`addr` — the off-mesh listener; `tls_cert_file`/`tls_key_file`/
`tls_reload_seconds` — its TLS certificate; `insecure` — plaintext, dev only;
see [Ingest TLS](#ingest-tls)), `registry`
(shared agent-connection registry; Valkey-backed when configured, else
in-memory), `retention` (`days`), `stale` (`after_seconds`), `jobs`, `events`,
`gateway`, `enroll` (`token_ttl_seconds` — minted enrollment-token lifetime),
`mesh_enroll` (the server's own SVID enrollment), and `limits_inventory`
(`max_request_bytes`, `max_snapshot_bytes`). Framework `server`/`admin`/
`discovery` sections supply the mesh gRPC/HTTP and admin listeners.

## Ingest TLS

The ingest edge serves server-authenticated TLS (minimum TLS 1.2; current
agents negotiate 1.3). Agents authenticate with their per-agent credential in
call metadata, so no client certificate is requested.

```yaml
ingest:
  addr: 0.0.0.0:9977
  tls_cert_file: /app/deploy/ingest/tls.crt   # PEM chain, leaf first
  tls_key_file: /app/deploy/ingest/tls.key
  tls_reload_seconds: 60                      # optional; 0 = 60
```

- The certificate must carry a DNS (or IP) SAN matching the name agents dial.
- The pair is re-read every `tls_reload_seconds`; a renewed certificate needs
  no restart. A pair that fails to load on reload is logged and the current
  certificate stays in use.
- Unless `insecure: true` is set, `tls_cert_file` and `tls_key_file` are
  required, and a missing or unloadable pair refuses start.
- `insecure: true` serves plaintext and is refused with `env: production`. It is
  for the development stack only, and it cannot be combined with the TLS keys.

Agent side (`agent.yaml`, or the matching flags):

```yaml
ingest_endpoint: inventory.example.org:9977
token_file: /etc/inventory-agent/token
credential_file: /var/lib/inventory-agent/credential
ca_file: /etc/inventory-agent/ingest-ca.pem   # optional (-ca-file)
server_name: inventory.example.org            # optional (-server-name)
```

With `insecure: false` (the default) the agent always verifies the server
certificate. Without `ca_file` it uses the operating-system roots. With
`ca_file` it trusts **only** the CAs in that PEM bundle (for a private CA), and
a certificate from any other CA is refused. `server_name` overrides the name
checked against the certificate, which is otherwise the endpoint host. The agent
refuses `insecure` combined with `ca_file` or `server_name`. A plaintext
(`-insecure`) agent cannot talk to a TLS ingest edge.

## Hosts, snapshots, change tracking

A submission is resolved to a host by **hardware UUID → machine id → hostname**
within the agent's tenant (created on first sighting, updated thereafter). Each
submission is stored as an immutable snapshot (hypertable, keyed by host +
collected time) with the full payload plus normalized, queryable component tables
(processors, memory modules, disks, network interfaces, software, services,
monitors). On ingest the server diffs the new snapshot against the host's
previous one and records a **change history** (added/removed/modified per
category); a `diff` API compares any two snapshots.

## On-demand refresh & live status

`POST /api/inventory/v1/agents/{host_id}/refresh` delivers a refresh command to
the host's connected agent through the shared registry (Valkey pub/sub, so it
works across horizontally-scaled server instances) and reports whether it was
delivered. agent-online/offline and snapshot-received events publish to the
platform bus (`platform:events:<tenant>`) for the gateway SSE hub, so the UI
updates live.

## Security notes

- Off-mesh agents are untrusted until enrolled; **enrollment tokens** are
  tenant-scoped, single-use and expiring. Per-agent and enrollment credentials
  are **sealed** (envelope encryption + KEK) and never returned in any response,
  log, audit entry or backup.
- The ingest edge binds every accepted submission to the verified agent's
  tenant/host scope — an agent cannot write another tenant's data. It is
  network-isolated from the mesh API and bounds submission size, rejecting
  oversized/malformed payloads with no partial write.
- Per-tenant PostgreSQL row-level security isolates all data; the shared
  connection registry and event bus are tenant-partitioned. Trusted worker paths
  (stale marking, retention purge) run under a scoped system subject.
- Every ingest, enrollment, refresh, deletion and admin action is recorded in the
  append-only, tamper-evident audit trail.

## Backup

`POST /api/inventory/v1/backup/export` exports the tenant's hosts, their latest
snapshots (or full history when requested), tags and change history, versioned by
schema; agent/enrollment secrets are never included. `POST
/api/inventory/v1/backup/import` recreates them (mode `skip` or `overwrite`),
preserving ids.

## UI

The remote under `services/inventory/ui` is built on the shared kit `@freya/ui` (FlyonUI + Zod,
see `docs/frontend.md`): forms validate through Zod schemas in `src/schemas/`, the
shell provides the theme and shared singletons, and `npm run lint` runs
`check-no-legacy`. Rebuild the image after UI changes; the Dockerfile builds `ui/kit`
first.
