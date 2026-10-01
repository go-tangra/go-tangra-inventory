# Research: Deliver Issued and Renewed Certificates to Inventory-Agent Hosts (033)

Evidence from go-tangra-inventory-v4 (`main` at `baae2c5`, service v4.6.3,
SDK `sdk/v4.3.0`), go-tangra-deployer-v4 (`main` at `e5f8536`, v4.3.2),
go-tangra-lcm-v4 (v4.6.1), go-tangra-docker (stack configs/policies), the v3
deployer provider `go-tangra-deployer/pkg/deploy/providers/tangra_client`
and the v3 client `go-tangra-client` (`internal/storage/certstore.go`,
`internal/hook/hook.go`, `internal/lcm/streamer.go`). Paths without a
prefix are in go-tangra-inventory-v4; others are prefixed with the repo.

## Current state

### v3 reference

- Provider `tangra_client` (`go-tangra-deployer/pkg/deploy/providers/
  tangra_client/tangra_client.go:40-60`): config `client_ids`, `labels`
  (AND), `cert_name` (default CN), `require_all_success`. `Deploy`
  (`:92-196`) resolves clients and publishes; success = publish succeeded
  (no install confirmation). `Verify` (`:198-318`) compares the
  client-reported status (`INSTALLED|FAILED|REMOVED|none`) and the SHA-256
  fingerprint with the certificate; partial success unless
  `require_all_success`. No rollback.
- Transport: Redis pub/sub `lcm.certificate.issued` → v3 lcm
  `StreamCertificateUpdates` → connected clients only. **Offline clients
  never receive the push** (no persistence, no retry).
- Client store (`go-tangra-client/internal/storage/certstore.go:15-160`):
  `<base>/live/<SafeName(name)>/{cert.pem 0600, privkey.pem 0600,
  chain.pem 0644, fullchain.pem 0600}`, metadata
  `<base>/renewal/<name>.json` (`name, common_name, serial_number,
  fingerprint, issued_at, expires_at, last_updated, issuer_name,
  dns_names, ip_addresses, previous_serial, renewal_count,
  last_hook_execution`). Writes are plain `os.WriteFile` (not atomic).
- Hook (`go-tangra-client/internal/hook/hook.go:76-160`): `bash <script>`,
  default timeout 5 min, environment `os.Environ()` + `LCM_CERT_NAME,
  LCM_CERT_PATH, LCM_KEY_PATH, LCM_CHAIN_PATH, LCM_FULLCHAIN_PATH,
  LCM_COMMON_NAME, LCM_DNS_NAMES, LCM_IP_ADDRESSES, LCM_SERIAL_NUMBER,
  LCM_EXPIRES_AT, LCM_IS_RENEWAL`; the hook script path was client config.
- Report: `ReportInstalledCertificate` (INSTALLED/FAILED, serial,
  sha256 fingerprint).

### Deployer (go-tangra-deployer-v4)

- Provider contract `internal/provider/provider.go:28-76`:
  `CertificateData{ID, SerialNumber, CommonName, SANs, CertificatePEM,
  PrivateKeyPEM, CertificateChain, ExpiresAt}` (no tenant, no job id);
  `Field{Key, Label, Secret, Required}` (no type, no help);
  `Capabilities{Type, DisplayName, SupportsVerify, SupportsRollback,
  ConfigFields, CredentialFields}`; registry write-once at `init()`.
- Job execution `internal/jobs/scheduler.go:60-115`: per child/direct job
  `cert.FetchCertificate(ctx, tenant, certID, true)` — **always with the
  key**; a fetch error retries/fails the job before the provider runs.
  `Deploy` runs under `jobTimeout()` (config `jobs.job_timeout_seconds`,
  default 300). `MaxRetries` 3, exponential backoff
  (`retryOrFail :118-131`).
- Verify/Rollback `internal/jobs/jobs.go:188-245`: fetch with key only for
  rollback; provider `Verify(ctx, cert, effectiveConfig, creds)`.
- lcm client `internal/lcmclient/lcmclient.go:33-59`:
  `Download{tenant_id, certificate_id, include_key}`.
- Events consumer `internal/events/consumer.go:22-23,169-210`: reads
  `platform:events:<tenant>` for `certificate.issued|renewed`, starts at the
  current tail, matches target `CertificateFilters`, creates jobs per
  auto-deploy target. `certificate.revoked` is ignored today.
- Configuration create/update (`internal/configs/configs.go:92-200`)
  checks only name and provider existence; provider config is opaque JSON;
  `Validate` calls `ValidateCredentials(creds, config)`.
- UI `ui/src/views/configurations/index.vue:96-97`: config and credentials
  are JSON textareas; hints list `required_config`/`required_credentials`.
- Mesh: deployer is a client of lcm (`deploy/policy.yaml` allows only the
  gateway inbound); docker config `go-tangra-docker/configs/deployer.yaml`
  discovers `lcm` only.

### lcm (go-tangra-lcm-v4)

- `Certificates/Download` (`internal/grpcapi/servers.go:246-252` →
  `internal/issue/certificates.go:362-376 DownloadForService`): acts for
  the named tenant with role admin for the calling SPIFFE service;
  `include_key` → `DownloadKey` (`:250-266`) which fails with
  `invalid("key", "no stored private key for this certificate")` when
  `KeySealed` is empty — CSR-issued certificates and certificates whose
  key was already handed out (`MarkKeyDelivered`, `:404-…`) have **no
  key**.
- Policy `deploy/policy.yaml` rule `deployer-download` allows only
  `svc/deployer` → `/lcm.v1.Certificates/Download`; production copy in
  `go-tangra-docker/policies/lcm.yaml:32-35`.
- Renewal creates a **new certificate id** (`renewCert`, `superseded_by`
  on the old row); events `certificate.{issued,renewed,revoked,failed}`
  with `{certificate_id, spiffe_id, not_after}`.

### Inventory (this repo)

- Ingest edge `sdk/api/proto/inventory/v1/inventory.proto:769-778`:
  `Enroll`, `SubmitInventory`, `StreamCommands`, `CheckAgentUpdate`,
  `DownloadAgentRelease`, `ReportUpgrade`. All but `Enroll` are
  authenticated by the per-agent credential (`internal/ingest/auth.go:39-44`
  `isUnauthenticatedMethod`) — new methods are covered automatically.
- `CommandType` 0–2 (`REFRESH`, `UPGRADE`); `Command` fields 1–3;
  `StreamRequest.capabilities` (field 4, ≤ 16 of `^[a-z0-9.]{1,32}$`,
  `internal/ingest/upgrade.go:28-29,80-89`); stored on the agent row
  (`SetAgentPlatform`), `store.CapUpgradeV1 = "upgrade.v1"`
  (`internal/store/models.go:583-584`).
- Persisted commands replayed on connect: `StreamCommands`
  (`internal/ingest/ingest.go:156-205`) sends `pendingCommands` after
  `Register`; `upgrades.OnConnect` (`internal/upgrades/service.go:350-366`)
  returns pending/delivered requests and marks them delivered; agents
  deduplicate by id. `registry.Command` travels as JSON through Valkey
  pub/sub (`internal/registry/registry.go:30-45`,
  `registry_valkey.go:36 inv:agent:cmd:<agent>`); delivery is
  fire-and-forget (`Deliver` returns false when not connected).
- Upgrade audit rows are written in the state-change transaction
  (`specs/023-hardware-agent-upgrade/contracts/audit-events.md`); audit
  vocabulary `internal/audit/audit.go:22-82`.
- Mesh surface `internal/grpcapi/servers.go` (host, snapshot, statistics,
  agent services) and `hostreports.go` with an extra handler-level
  consumer allow-list (`host_reports.consumers`,
  `internal/config/config.go:85-92`) on top of the mesh policy.
- Policy `deploy/policy.yaml`: gateway, asset, ipam only. Inventory already
  depends on the lcm SDK (`go.mod:12`, SVID enrollment) but has **no
  `Certificates` client** and no `lcm` discovery peer for module calls
  besides `mesh_enroll.lcm_grpc` (`go-tangra-docker/configs/inventory.yaml`).
- Agent: `internal/daemon/daemon.go:97-135` announces `upgrade.v1` and
  handles REFRESH/UPGRADE; config `internal/config/config.go:435-500`
  (`AgentConfig`, strict YAML); packaged `packaging/agent.yaml`.
  systemd unit `packaging/inventory-agent.service`: `User=root`,
  `ProtectHome=yes`, `PrivateTmp=yes`, `NoNewPrivileges=yes`,
  **no `ProtectSystem`** → `/etc/inventory-agent/certs` is writable without
  `ReadWritePaths`. A hook started by the agent inherits this sandbox
  (no `/home`, private `/tmp`, no privilege gain) — `systemctl reload
  nginx` over D-Bus still works.
- Ingest TLS: `ingest.insecure` is development only and refused in
  production (`internal/config/config.go:349-359`); the dev stack runs the
  ingest edge in plaintext (`go-tangra-docker/configs/inventory.yaml`
  `ingest: {insecure: true}`).
- Windows agent: `internal/winsvc` (service); no symlink-based layout
  tested there.
- Latest migration `0009_list_sort_indexes.sql` → next **0010**. Latest SDK
  tag `sdk/v4.3.0` → next **`sdk/v4.4.0`**. Host has `tags map<string,string>`
  and `ListHostsRequest.tag` (`"key"` or `"key=value"`).

## Findings that change the plan

- **F1** The deployer always fetches the key before calling a provider
  (`scheduler.go:83`). For a key-less certificate the job fails before the
  provider runs, and for this provider the deployer would hold a key it
  does not need. → the provider must declare that it delivers by
  reference (D4).
- **F2** Providers have no tenant or job id (`provider.go:72-76`). The
  inventory needs both (tenant scope, idempotency). → job metadata on the
  context (D4).
- **F3** Registry pub/sub carries command JSON through Valkey; putting the
  key in a command would place it in Valkey. → commands carry only ids; the
  agent pulls material (D6).
- **F4** Persisting the key for offline agents contradicts decision 1. →
  the inventory persists only the reference and fetches from lcm at the
  moment the agent pulls (D5).
- **F5** lcm renewal changes the certificate id; delivery identity is
  therefore (host, name), not certificate id (D8).
- **F6** Provider registration happens in `init()`; the new provider needs
  an injected inventory client. → register it during app wiring when the
  inventory peer is configured (D3).
- **F7** The deployer consumer starts at the stream tail: events while the
  deployer is down are lost (existing behaviour, affects renewal
  delivery). Out of scope; noted as risk with the manual "deploy" as
  remedy.

## Decisions

### D1 — Architecture: deployer decides, inventory relays by reference

**Decision**: The deployer provider sends the inventory module a
**delivery request** (tenant, job id, configuration id, certificate id,
name, selection, key policy). The inventory resolves hosts, persists one
**delivery item** per host (no material), pushes a command carrying only
the item id, and when the agent calls `FetchCertificate(item_id)` the
inventory downloads the bundle from lcm (`include_key` per key policy),
validates it, returns it on that call and drops it. The agent writes the
files, runs the local hook and reports.

**Rationale**: decision 1 (key only in inventory memory, never persisted);
offline agents are served later from fresh lcm data (fixes v3's loss);
lcm remains the only key store; Valkey/registry never see material.

**Alternatives rejected**: deployer pushes material to inventory, inventory
caches until delivery (persists or holds keys indefinitely); deployer
streams to agents directly (agents are off-mesh; second edge); inventory
seals keys at rest with its KEK (violates decision 1).

### D2 — Mesh contract: `inventory.v1.CertificateDeliveryService`

**Decision**: New service on the inventory mesh gRPC surface (not
gateway-proxied), callable only by `svc/deployer` (policy) and
additionally checked against `cert_delivery.sources` (handler allow-list,
same pattern as `host_reports.consumers`):
`CreateCertificateDelivery`, `GetCertificateDelivery`,
`PreviewCertificateTargets`, `VerifyHostCertificates`,
`MarkCertificateRevoked`. Tenant from the request (service acting for a
tenant, like host reports), actor = the SPIFFE service. Contract in
[contracts/inventory-grpc.md](contracts/inventory-grpc.md) §1.

**Alternatives**: HTTP through the gateway (user tokens; deployer acts as a
service, not a user); reuse `InventoryAgentService` (mixes concerns,
broader policy).

### D3 — Deployer provider `inventory-agent`

**Decision**: `internal/providers/inventoryagent` in the deployer:
- Registered during app wiring (`provider.Register(inventoryagent.New(
  client, cfg))`) only when `inventory.service` is configured, before
  workers and HTTP start — still write-once before serving (documented in
  Complexity Tracking). When not configured the provider is absent from
  the catalogue.
- `Capabilities`: `SupportsVerify: true`, `SupportsRollback: false`,
  `DeliversByReference: true` (D4), no credential fields; typed config
  fields (D13).
- `Deploy`: parse config → `CreateCertificateDelivery` with idempotency key
  = job id → poll `GetCertificateDelivery` every 2 s until all items are
  terminal-or-queued or `wait_seconds` (clamped to the context deadline
  minus 10 s) → evaluate (D9) → `Result{Success, Message, Details{delivery_id,
  counts, hosts[≤200]}}`. Progress: 10 % created, then share of settled
  hosts.
- `Verify`: `VerifyHostCertificates(selection, name,
  expected_fingerprint = sha256(cert DER))`.
- `ValidateCredentials` (the UI's Validate): parse config +
  `PreviewCertificateTargets` → error when no host matches; the matched
  hosts are returned in the validation detail (D13).

**Rationale**: decision 1; parity with v3 config names where possible
(`cert_name`, `require_all_success`); v3 `client_ids/labels` become
`host_ids/host_tags`.

### D4 — Deployer core changes (minimal, additive)

**Decision**:
1. `provider.Capabilities.DeliversByReference bool` — the scheduler and
   `runAction` call `FetchCertificate(..., includeKey = !DeliversByReference)`;
   for this provider the deployer never holds a key (FR-006), and key-less
   certificates are not rejected by the deployer (the inventory applies
   the key policy).
2. `provider.JobMeta{TenantID, JobID, ConfigurationID, TargetID,
   Trigger}` with `provider.WithJob(ctx, m)` / `provider.JobFrom(ctx)`; set
   in `jobs.process` and `jobs.runAction`. Existing providers ignore it.
3. Optional interface `provider.ConfigValidator{ValidateConfig(map[string]any)
   error}` called by `configs.Create/Update` and when a target override
   changes the effective config (FR-003).
4. `provider.Field` gains `Type` (`string|text|int|bool|enum|string_list|
   host_selector`), `Help`, `Options`, `Default`, `Min`, `Max` — JSON
   additive; existing providers keep `Type` empty (= `string`).

**Alternatives**: put tenant/job into `CertificateData` (it is "material",
and every provider would see job internals); a separate provider
interface for reference providers (duplicates scheduler code).

### D5 — Key handling and key-less certificates

**Decision**: config `key_policy`:
- `require` (default): inventory calls lcm `Download(include_key=true)`;
  lcm "no stored private key" → item `failed`, reason `key_unavailable`.
- `certificate_only`: inventory calls `Download(include_key=false)` and
  relays certificate + chain only. The agent writes `cert.pem`,
  `chain.pem`, `fullchain.pem`; if `privkey.pem` exists in the current
  generation and matches the certificate's public key it is carried into
  the new generation, otherwise the item fails `key_mismatch` and nothing
  changes; if no key exists the set is installed without `privkey.pem`
  (state `installed`, detail `no_key`). Use case: the host generated the
  CSR itself.

The key lives in a `[]byte` in the fetch handler, is never logged (the
gRPC logging interceptor does not log payloads — asserted by a test), is
zeroed after `Send`, and the handler never stores the response. Message
size: bundle ≤ 512 KiB.

**Alternatives**: always fail key-less certificates (blocks the CSR use
case); always deliver without key silently (risk of broken services).

### D6 — Agent protocol (ingest edge)

**Decision** (contract in [contracts/inventory-grpc.md](contracts/inventory-grpc.md) §2):
- `CommandType COMMAND_TYPE_CERTIFICATE = 3`; `Command.certificate = 4`
  `CertificateCommand{item_id, name}` — no material, no paths.
- `FetchCertificate(FetchCertificateRequest{item_id}) →
  CertificateBundle` (unary): authorized only for the calling agent's own
  item in state `pending|delivered|fetched` (re-fetch allowed for crash
  recovery, ≤ 5 fetches per item); transitions to `fetched`.
- `ReportCertificate(ReportCertificateRequest{item_id, state, serial,
  fingerprint_sha256, reason, hook_exit_code, detail}) → {accepted}`;
  states `installed|unchanged|failed|hook_failed`; closed reason codes.
- Capability `cert.v1` in `StreamRequest.capabilities`.
- Registry `Command.Certificate *CertificatePayload{ItemID, Name}`.

**Rationale**: same shape as upgrades (023 D9): command = id, pull over the
authenticated channel, report; replay on connect. **Alternative**: stream
material in the command (puts keys into Valkey, F3).

### D7 — Persisted deliveries, replay and expiry

**Decision**: tables `inventory_cert_deliveries` (request) and
`inventory_cert_delivery_items` (per host), `inventory_host_certificates`
(per host+name current state) — migration 0010, RLS, no key column
([data-model.md](data-model.md)). On item creation the service calls
`registry.Deliver` when the agent is online; `StreamCommands` adds
`certdelivery.OnConnect(agent)` next to `upgrades.OnConnect`, returning
commands for every active item of the agent (≤ 50 per connect, oldest
first; the rest after the agent reports). Sweeper (existing jobs loop):
active items past `expires_at` → `expired`; `fetched` without report for
`report_timeout_minutes` (15) → `failed/no_report`.
`expires_at = min(created + pending_ttl (168 h), certificate not_after)`.

### D8 — Identity of a delivery on a host: (host, name)

**Decision**: one active item per `(tenant, host_id, name)` (partial unique
index). Creating a new item for the same host and name marks the previous
active one `superseded` (audited). `inventory_host_certificates` is keyed
by `(tenant, host_id, name)` and updated on every terminal report; it
stores `configuration_id` of the last delivery, so the UI can show which
configuration owns a name and flag conflicts (two configurations writing
the same name — last writer wins, both audited).

`LCM_IS_RENEWAL` = the host already had a different certificate (different
fingerprint) installed under the name (computed by the inventory and sent
in the bundle; the agent also derives it locally and the local value wins
when the metadata exists).

### D9 — Job outcome semantics

**Decision** (evaluated by the provider at the end of the wait):

| Item state at the end of the wait | Counted as |
|---|---|
| `installed`, `unchanged` | done |
| `pending`, `delivered`, `fetched` (agent offline or slow) | queued |
| `failed`, `hook_failed`, `expired`, `cancelled` | failed |
| `unsupported` | unsupported |
| `superseded` | ignored |

- 0 items → fail "no hosts matched".
- `require_all_success = true`: success iff `failed + unsupported == 0`
  (queued hosts are not failures, US2 AS4 — see Q2).
- `require_all_success = false`: success iff `done + queued ≥ 1`.
- Message: "Installed on N, unchanged on U, queued for Q, failed on F,
  unsupported on X"; `Details.hosts` lists up to 200 non-done hosts first.
- `hook_failed` counts as failed. Re-arming a `hook_failed` item sets
  `rerun_hook` in the next bundle, so the agent runs the hook again even
  though the files are unchanged (otherwise D11 would skip it).
- Deployer retry → `CreateCertificateDelivery` with the same idempotency
  key and `rearm_failed = true` re-arms `failed|hook_failed|expired` items
  (state `pending`, attempts + 1, ≤ 5) and leaves done/queued items alone
  (US2 AS5).
- Automatic deployments never install an older certificate: the inventory
  skips (state `superseded`, reason `older_than_installed`) a host whose
  installed certificate under that name expires later than the requested
  one, unless the job trigger is `manual` (recorded in audit).

### D10 — Agent store layout and atomicity

**Decision** (contract in [contracts/agent-config.md](contracts/agent-config.md)):

```text
<dir>/                                  0750 (dir_mode)
  archive/<name>/<generation>/          generation = UTC yyyymmddThhmmssZ-<serial[:16]>
      cert.pem chain.pem fullchain.pem  cert_mode (0644)
      privkey.pem                       key_mode (0600)
  live/<name>  -> ../archive/<name>/<generation>   (directory symlink)
  renewal/<name>.json                   metadata (v3 field names + item/certificate ids)
```

Write order: create the generation directory (0700 while staging), write
each file with `O_CREAT|O_EXCL`, `fchown`/`fchmod`, `fsync`; fsync the
directory; set its final mode; create `live/.<name>.tmp` symlink and
`rename(2)` it over `live/<name>` (atomic swap of the whole set); fsync
`live/`; write metadata via temp + rename; prune generations beyond
`keep_previous`. Paths seen by consumers are exactly the certbot ones
(`live/<name>/fullchain.pem`). Crash recovery: on start, generation
directories not referenced by `live/<name>` and newer than it are
removed.

**Rationale**: per-file rename (v3, certbot's archive file symlinks) can
expose a new certificate with the old key between renames; a directory
symlink swap is atomic for the set. **Alternative**: renaming a directory
over a non-empty directory is not possible on Linux; `RENAME_EXCHANGE`
is Linux-only and filesystem-dependent.

Name rule (agent and server, shared package `internal/certmaterial`):
`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, not containing `..`; default name
from CN: lowercase, `*.` → `wildcard.`, other invalid characters → `_`,
truncated to 64; if still invalid the configuration must set `cert_name`.

Ownership: `owner`/`group` names resolved on every install (`os/user`);
unknown → `failed/owner_unknown`. Linux only.

### D11 — Idempotency on the host

**Decision**: before writing, the agent reads `renewal/<name>.json` and the
current `live/<name>/cert.pem`; if the stored fingerprint equals the
bundle's fingerprint, the file's actual SHA-256 matches it, `privkey.pem`
matches (when delivered) and modes/owner are as configured → report
`unchanged`, no write, no hook. If files were tampered with or are
missing → reinstall (and run the hook). Modes/owner drift only → fix in
place, no hook.

### D12 — Deploy hook

**Decision**: agent config `certificates.deploy_hook` (absolute path,
empty = disabled, default) and `hook_timeout_seconds` (default 300,
30–1800). Execution: `exec` of the file itself (no `bash`, no shell, no
arguments), working directory `live/<name>`, environment **only**
`PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`,
`LANG=C.UTF-8` and the v3 `LCM_*` variables (+ `LCM_CERT_DIR`,
`LCM_CERTIFICATE_ID`); new process group, killed on timeout; stdout/stderr
captured up to 4 KiB into the agent log only. Checked before every run:
regular file, owned by uid 0, not group/other-writable, executable,
parent directory not group/other-writable (`hook_refused` otherwise). One
hook run at a time per agent. Server never sends hook data (SR-005/006).

**Alternatives**: v3 `bash script` with inherited environment (shell
semantics, environment leakage); systemd-run per hook (unnecessary).

### D13 — Configuration schema and deployer UI

**Decision**: provider config (JSON, validated by `ValidateConfig`):
`host_ids[] (uuid, ≤1000)`, `host_tags[] (≤16, ^[A-Za-z0-9_.:/-]{1,63}(=[^\x00-\x1f]{0,255})?$)`,
`cert_name`, `key_policy`, `require_all_success`, `wait_seconds (0–240,
default 60)`. UI: a dedicated component for `provider_type ===
'inventory-agent'` with a host picker that calls the **inventory** API
through the gateway with the user's own session
(`GET /api/inventory/v1/hosts?search=&tag=&page=`) — so users see only
hosts they may read; on 401/403/404 the picker shows "enter host ids and
tags manually". "Validate" calls the deployer `Validate` endpoint, whose
response gains `details.matched_hosts` from `PreviewCertificateTargets`.

**Alternative rejected**: deployer proxies inventory host lists — the
deployer acts as a service and would disclose all tenant hosts to users
without inventory read.

### D14 — Revocation

**Decision**: report only. The deployer consumer additionally handles
`certificate.revoked`: when the tenant has at least one active
`inventory-agent` configuration, it calls `MarkCertificateRevoked(tenant,
certificate_id)`; the inventory cancels active items of that certificate
(`cancelled/certificate_revoked`) and sets `revoked_at` on host
certificates holding it; audited. At fetch time the inventory also refuses
revoked or expired certificates (lcm status) → `failed/certificate_revoked|
certificate_expired`. Files are never deleted.

**Alternatives**: delete files on hosts (can break services, and a revoked
certificate is usually replaced by a renewal right away); inventory
consumes lcm events itself (second consumer of lifecycle events with
tenant discovery; the deployer already has it).

### D15 — Platform scope: Linux first

**Decision**: the capability is announced only by Linux agents
(`runtime.GOOS == "linux"`). Windows agents → `unsupported/platform`.
Reason: Windows certificate consumers (IIS, RDP) use the certificate
store, not PEM files; directory symlinks need privileges; ACLs replace
modes. Follow-up feature.

### D16 — Transport safety and enablement

**Decision**: server `cert_delivery.enabled` (default **false**);
`cert_delivery.allow_plaintext_ingest` (dev only, refused when
`env=production`, warning at start). Agent `certificates.enabled`
(default **true**: files are confined to a dedicated directory and nothing
is executed without a local hook; a host owner can opt out) and
`certificates.allow_insecure_transport` (default false; required to
announce `cert.v1` when `insecure: true`). See open question Q1.

### D17 — Bounds and rate limits

| Bound | Value |
|---|---|
| hosts per delivery (ids + tag matches) | 1000 |
| host_ids / host_tags in config | 1000 / 16 |
| cert PEM / chain PEM / key PEM / bundle | 64 KiB / 256 KiB / 16 KiB / 512 KiB |
| chain certificates | 10 |
| concurrent fetches per agent / global per replica | 1 / 50 (`max_concurrent_fetches`) |
| lcm Download timeout | 10 s |
| fetches per item | 5 |
| commands replayed per connect | 50 |
| attempts per item (re-arm) | 5 |
| report `detail` | 256 bytes, sanitised |
| delivery list page | listquery (032) |

### D18 — Permissions

**Decision**: no new permission. Inventory read endpoints use
`inventory:read`; cancelling an item uses `agents:manage`. Deployer
permissions unchanged (configuration manage/deploy grants). The mesh
methods are authorized by SPIFFE policy + `cert_delivery.sources`.

### D19 — Audit and events

Inventory: transactional audit rows for each transition (see
[contracts/audit-events.md](contracts/audit-events.md)); realtime event
`inventory.certificate.delivery {host_id, item_id, state}` (content-free).
Deployer: existing `deployment_*` rows; job result details carry the
delivery id and counts; new `certificate_revocation_forwarded`.

### D20 — Release and rollout order

1. inventory SDK `sdk/v4.4.0` (proto + `inventoryclient` delivery helper).
2. inventory v4.7.0 (migration 0010, service, agent 4.7.0 with `cert.v1`,
   UI); signed agent release (user approves the `release` environment);
   agents upgrade through 023 self-upgrade.
3. Policies: lcm rule `inventory-download`, inventory rule
   `deployer-cert-delivery` (repo `deploy/policy.yaml` + go-tangra-docker
   `policies/*.yaml`, user-applied); inventory config `cert_delivery` +
   discovery `lcm`; deployer config `inventory` + discovery `inventory`.
4. deployer v4.4.0 (provider, core changes, UI) on inventory SDK v4.4.0.
5. lcm: no code change; a release only if its image embeds the policy
   used in production (it does not: production mounts
   `go-tangra-docker/policies/lcm.yaml`).

## Constitution check notes

- I: server delivery off by default; plaintext opt-outs named and refused
  in production; hook off by default.
- II: two new mesh edges, each allowed per method; agent edge unchanged.
- III: proto bounds, `ValidateConfig`, inventory validation of lcm
  material (D17), agent re-validation, DB CHECKs.
- IV: tests first per phase; negative tests and fuzz targets for name,
  PEM bundle, config parsing, report states.
- V: transactional audit; no key in logs (test scan, SC-003).
- VI: no new dependency (stdlib `crypto/x509`, `os/user`, `syscall`).
- VII: one relay service, one agent store package, typed config sections.

## STRIDE threat model

| Threat | Scenario | Mitigation |
|---|---|---|
| **S**poofing | Stolen agent credential fetches keys of other hosts | `FetchCertificate` only for the caller's own item (tenant + agent id + active state); other ids → `NotFound` + `cert_delivery_refused` audit; revocation of the agent cancels its items. |
| **S** | Another mesh service creates deliveries | Policy allows only `svc/deployer`; handler re-checks `cert_delivery.sources`. |
| **S** | Rogue server pushes certificates | Agent accepts commands only on its TLS ingest connection (CA pin optional); plaintext refused unless locally opted out. |
| **T**ampering | Name with `../` writes outside the directory | Name regex at deployer, inventory and agent; agent joins only validated single components and refuses symlinked `live/`/`archive/` parents not created by itself (`O_NOFOLLOW` on directory opens). |
| **T** | Server-driven code execution | No server field reaches exec; hook path, owner, modes only from local config; hook file ownership/permission checks. |
| **T** | Certificate/key mismatch installed | Agent verifies key ↔ certificate public key; inventory validates before relaying. |
| **T** | Replay of an older certificate (downgrade) | Items superseded per (host, name); automatic deployments skip hosts holding a later-expiring certificate under the name (D9); revoked/expired certificates refused at fetch (D14). Manual deployments of an older certificate are allowed and audited. |
| **R**epudiation | Who delivered what where | Audit rows for request (service + deployer job id), fetch (agent), result, supersession, cancellation; deployer job history. |
| **I**nformation disclosure | Key leaks through DB, Valkey, logs, audit, events | Key never persisted; registry commands carry ids only; tests scan DB dumps, logs and streams for key PEM headers (SC-003); gRPC payload logging off for ingest. |
| **I** | Cross-tenant host selection | Selection resolved inside the request tenant under RLS; foreign ids reported as `unknown_host` without detail. |
| **I** | Key readable by other local users | `key_mode` default 0600, owner root; dir 0750; staging generation 0700. |
| **D**enial of service | Mass deployments flood agents/lcm | Bounds D17, one fetch per agent, global fetch cap, 50 commands per connect, lcm timeout. |
| **D** | Hook hangs | Timeout + process-group kill; one hook at a time. |
| **E**levation of privilege | Hook replaced by an unprivileged user | Root ownership and permission checks of the file and its directory before each run. |
| **E** | Deployer user gains host access | Deployer users can place a certificate into the dedicated directory only; executing anything requires a locally configured hook. |

## Resolved questions (user, 2026-10-02)

- **Q1** Agent default `certificates.enabled`: **true** — every Linux agent
  accepts deliveries once the server switch is on; a host opts out locally.
- **Q2** Queued (offline) hosts **do not** fail a job, even with
  `require_all_success`; only real failures (hook failed, key mismatch,
  unsupported agent) do.
- **Q3** **Linux first**; Windows agents report `unsupported` (D15).
- **Q4** Directory-symlink layout (`live/<name>` → `archive/<name>/<gen>`,
  atomic switch) **accepted**.
