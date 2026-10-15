---
title: Operate inventory
summary: The service and its listeners, configuration keys and defaults, the ingest edge's TLS, service policies, agent releases, and fixes for common problems.
kind: reference
order: 90
tags: [inventory, operations, configuration, policy, troubleshooting]
audience: operator
---
Inventory runs as `inventorysvc` (image `ghcr.io/go-tangra/go-tangra-inventory`).

```sh
inventorysvc -config /app/deploy/container.yaml   # serve; applies migrations
inventorysvc bootstrap -config <file>             # apply migrations and exit
inventorysvc agent-release import -config <file> <release dir>
inventorysvc version
```

At start it enrolls for its SVID `spiffe://<trust domain>/svc/inventory`,
migrates its TimescaleDB schema, imports the agent releases bundled in the
image, registers with the gateway and registers its permissions and roles
with auth. It listens on:

| Listener | Default | Who calls it |
|---|---|---|
| mesh gRPC / HTTP (`server.grpc_addr`, `server.http_addr`) | `:9975` / `:9976` | the gateway and other modules, SPIFFE mTLS |
| ingest edge (`ingest.addr`) | `:9977` | agents, off the mesh, with their own credential |
| admin (`admin.addr`) | `127.0.0.1:9810` | health, readiness and metrics |

Publish only the ingest edge to the networks where agents run. Agents must
reach it directly: enrollment keys check the TCP peer address, so NAT or a
TCP proxy in front of it makes every agent appear with the proxy's address.

## Configuration

The file is YAML. Unknown keys are refused, so a typo stops the service at
start with `config: <file>: ...`. Besides the framework keys (`service_name`,
`trust_domain`, `env`, `identity`, `authz`, `server`, `admin`, `discovery`,
`limits`) inventory reads:

| Key | Default | Notes |
|---|---|---|
| `db.dsn` | — | required; in production `sslmode=verify-full` (or `verify-ca`) |
| `db.migrate_dsn`, `db.max_conns` | —, 16 | owner connection for migrations; pool size |
| `valkey.addresses` | — | required; the event bus and the shared agent registry |
| `valkey.username`, `valkey.password`, `valkey.ca_file` | — | Valkey user `inventory` over TLS |
| `valkey.allow_plaintext` | false | development only; refused in production |
| `kek.source` | `file` | `file` (with `kek.path`) or `env` (with `kek.env`): the key that seals agent credentials and enrollment key secrets |
| `ingest.addr` | — | required |
| `ingest.tls_cert_file`, `ingest.tls_key_file` | — | required unless `ingest.insecure`; PEM chain, leaf first, with a SAN for the name agents dial |
| `ingest.tls_reload_seconds` | 60 | the pair is re-read on this interval; no restart for a renewed certificate |
| `ingest.insecure` | false | plaintext, development only; refused in production |
| `registry.instance_id` | empty | this replica's name in the shared agent registry |
| `retention.days` | 90 | 1–3650; snapshots and finished deliveries older than this are purged |
| `stale.after_seconds` | 86400 | 60–31536000; hosts silent this long become `stale` |
| `jobs.purge_interval_seconds` | 3600 | how often stale marking and purging run |
| `gateway.service` | `gateway` | mesh name of the gateway; the only peer allowed to fetch the help pages |
| `gateway.issuer` | — | required, an `https://` origin: the platform token issuer |
| `mesh_enroll.*` | — | `enabled`, `enroll_url`, `lcm_grpc`, `tenant_id`, `token_file`, `state_file`, `insecure` |
| `limits_inventory.max_snapshot_bytes` | 8 MiB | largest snapshot accepted |
| `host_reports.consumers` | `[ipam, asset]` | mesh services allowed to read host reports |
| `host_reports.max_page_bytes` | 3 MiB | 64 KiB–7 MiB per host report page |
| `agent_releases.*` | see below | agent self-upgrade |
| `cert_delivery.*` | off | certificate delivery for the deployer, see below |
| `module_delivery.*` | off | module bundles from the gateway's add-module wizard |

`registry.heartbeat_seconds`, `jobs.workers`, `jobs.interval_seconds`,
`events.enabled`, `enroll.token_ttl_seconds` and
`limits_inventory.max_request_bytes` are accepted and range-checked, but this
version does not use them: live events are always published, and enrollment
tokens last 24 hours unless the request names another lifetime.

### Agent releases

```yaml
agent_releases:
  bundle_dir: /app/agent-releases   # signed releases shipped in the image
  keep_versions: 5                  # 2-50 kept in PostgreSQL (about 80 MB each)
  max_concurrent_downloads: 20      # 1-500 per instance
  chunk_bytes: 1048576              # 64 KiB-1 MiB
  request_ttl_hours: 168            # pending requests expire
  progress_timeout_minutes: 15      # 5-120; a silent download or install fails
```

Releases are verified against the release keys built into the binary before
they are stored; a build without keys trusts no release. For an offline site
or another version, import a verified release directory with
`inventorysvc agent-release import`.

### Certificate delivery

```yaml
cert_delivery:
  enabled: true
  sources: [deployer]          # mesh services allowed to create deliveries
  lcm_service: lcm             # discovery name of lcm
  pending_ttl_hours: 168       # 1-720
  report_timeout_minutes: 15   # 5-120; keep above the agents' hook_timeout_seconds
  max_concurrent_fetches: 50   # 1-500 per replica
  lcm_timeout_seconds: 10      # 2-60
```

Inventory also needs `discovery.static.lcm`. The deployer needs
`discovery.static.inventory` and `inventory: { service: inventory }`.

## Service policies

Inventory's own policy:

```yaml
rules:
  - id: gateway-forwards
    from: ["spiffe://<trust domain>/svc/gateway"]
    to: ["inventory"]
    operations: ["*"]
    effect: allow
  - id: deployer-cert-delivery
    from: ["spiffe://<trust domain>/svc/deployer"]
    to: ["inventory"]
    operations: ["/inventory.v1.CertificateDeliveryService/CreateCertificateDelivery",
                 "/inventory.v1.CertificateDeliveryService/GetCertificateDelivery",
                 "/inventory.v1.CertificateDeliveryService/PreviewCertificateTargets",
                 "/inventory.v1.CertificateDeliveryService/VerifyHostCertificates",
                 "/inventory.v1.CertificateDeliveryService/MarkCertificateRevoked",
                 "/grpc.health.v1.Health/Check"]
    effect: allow
```

`gateway-forwards` covers the browser API, the module delivery service and
the help pages (`GET /docs/v1/bundle`). The shipped policy also has
`ipam-hostsync` and `asset-sync` for the host report readers.

Other modules must admit inventory:

| Module | Operation | Why |
|---|---|---|
| lcm | `/lcm.v1.Certificates/Download` (rule `inventory-download`) | fetch the certificate when an agent pulls a delivery |
| gateway | `/inventory.v1.ModuleBundleSource/RenderModuleBundle` (rule `inventory-module-bundle`) | module delivery |

## Troubleshooting

**The service refuses to start with `config: ...`.** The message names the
key. Common ones: `ingest.tls_cert_file is required unless ingest.insecure
is set`, `ingest.insecure is not permitted in production`,
`kek.path is required for kek.source file`, `gateway.issuer must be an https
origin`.

**The service refuses to start with `help docs: ...`.** The help pages built
into the binary are invalid. This is caught by `tangra-docs lint` in CI; a
build that skips CI must be rebuilt.

**Agents cannot connect: TLS errors.** The ingest certificate must carry a
SAN for the name agents dial and chain to a CA the agent trusts (system
roots, or `ca_file` in `agent.yaml`). A plaintext agent cannot talk to a TLS
ingest edge.

**Every automatic enrollment is refused.** The refusal is the same for every
cause; the audit entry `auto_enroll_refused` names it: `address_not_allowed`
(often NAT or a proxy), `stale_timestamp` (clock), `tenant_disabled`,
`key_disabled`, `key_expired`, `key_exhausted`, `rate_limited`, `replay`,
`bad_signature`.

**Upgrades fail with `unsupported_install`.** The host has no systemd;
upgrade it by hand. `start_timeout` means the new version did not report
within the agent's confirm timeout and was rolled back.

**Certificate deliveries end `unsupported` with `ambiguous_agent`.** More
than one non-revoked agent claims the host. Revoke the stale one.

**IPAM's host sync reports `degraded`.** IPAM is missing from the
`ipam-hostsync` rule or from `host_reports.consumers`.
