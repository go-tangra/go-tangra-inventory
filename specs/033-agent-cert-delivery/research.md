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

### Configuration drawer — v3 (go-tangra-deployer `e4f6d0f`) and v4 (US5)

- v3 drawer `frontend/src/views/configuration/configuration-drawer.vue`:
  provider select (display name + description) on create; then a
  Credentials and a Config section. **Required** keys come from the
  backend `ProviderInfo.required_config_fields/required_credential_fields`
  (`protos/deployer/service/v1/target_configuration.proto:39-47`, plain
  string lists) and are marked with `rules: [{required: true}]`;
  **optional** keys are hard-coded in the UI (`optionalConfigFields`,
  `optionalCredentialFields`, `:145-166`); secret masking by a hard-coded
  name list (`password, secret, token, api_key, …`, `:176-183`); labels and
  placeholders from i18n `deployer.providers.<type>.fields.<key>[_placeholder]`
  (`frontend/src/locales/en-US.json:209-290`). Every input is a plain text
  input (no numbers, switches or selects; `skip_tls_verify` typed as
  "true or false"). On edit the credentials map starts empty and is sent
  only when re-entered ("leave blank to keep" for the *whole* map — a
  single re-entered field replaced all stored credentials). No
  test-connection button in the drawer. Backend
  `pkg/deploy/registry/effective_config.go:38-60` checks required config
  keys on the **merged** configuration + target override (an empty
  string counts as missing).
- v3 required fields: aws_acm `region` / `access_key_id`,
  `secret_access_key`; bigip `partition` / `host`, `username`, `password`;
  cloudflare `zone_id` / `api_token`; fortigate `vdom` / `host`,
  `api_token`; webhook `url`; tangra-client none (at least one of
  `client_ids`/`labels` enforced by `ValidateCredentials`); dummy none.
  v3 optional (UI list): bigip `ssl_profile`; fortigate
  `default_ssl_profile`; webhook `verify_url`, `rollback_url`,
  `skip_tls_verify`, `timeout_seconds` / `authorization`, `api_key`,
  `secret`, `token`; tangra-client `client_ids`, `labels`, `cert_name`,
  `require_all_success`.
- v4 drawer `go-tangra-deployer-v4/ui/src/views/configurations/index.vue:96-97`:
  name, description, provider select, then **two raw JSON text areas**
  (config, credentials) validated only as JSON objects
  (`ui/src/schemas/configuration.ts`). Hints read
  `provider.required_config/required_credentials` (`ui/src/api/types.ts:11-18`),
  which the backend never sends — it sends `config_fields` /
  `credential_fields` (`internal/provider/provider.go:50-66`) — so no hint
  is ever shown (F8). "Validate" posts both maps; any provider error is
  reduced to "credentials rejected by the provider"
  (`internal/configs/configs.go` `Validate`).
- v4 `provider.Field{Key, Label, Secret, Required}` only. Declarations:
  aws_acm `region` R, `certificate_arn` / `access_key_id` R,
  `secret_access_key` R S, `session_token` S; bigip `partition` R / `host`
  R, `username` R, `password` R S; cloudflare `zone_id` R / `api_token` R S;
  dummy none; fortigate `vdom` R / `host` R, `api_token` R S; webhook
  `url` R, `verify_url`, `rollback_url` / `token` S, `secret` S.
  Read but **not declared** (F9): webhook `timeout_seconds`,
  `skip_tls_verify`, `headers`, `metadata`, `authorization`, `api_key`;
  fortigate `import_scope`; dummy `fail`; test overrides aws_acm
  `endpoint`, cloudflare `api_base` (F10).
- v4 save path: `configs.Create/Update` check only name and provider
  existence — no required-field check at all; `Update` replaces the whole
  sealed credentials blob when any credential is sent; reads return only
  `has_credentials`. `failSvc` (`internal/httpapi/deps.go:38-43`) writes a
  bare `validation_failed` and drops `ValidationError.Field` (F11), while
  the kit's `useZodForm.setServerError` already maps
  `detail.fields` to inline errors (`go-tangra/ui/kit/src/forms/useZodForm.ts:127-145`).
  `GET /providers` requires `configurations:read`.
- v4 target overrides (`internal/targets/targets.go` `Attach`): a map
  `configuration id → config overlay`, stored **unsealed** in the target
  row (`ConfigOverrides`); the only check is `rejectCredentialKeys` (a
  fixed list of credential-shaped key names). The scheduler merges
  `config ⊕ override` (`internal/jobs/scheduler.go:192-220`). The target
  form (`ui/src/views/targets/index.vue:115`) is a raw JSON text area
  "Per-config overrides (JSON: {configId: {...}})". Permissions:
  `targets:manage` vs `configurations:manage` are separate (the built-in
  `operator` grant holds both; the module role "operator" holds neither).
- Kit (`go-tangra/ui/kit` 4.3.1): `UiForm`/`useZodForm`, `zodToFields` +
  `UiFieldRenderer` (types text, number, date, select, textarea,
  checkbox, secret, tags), `UiInput`, `UiTextarea`, `UiSelect`,
  `UiCombobox`, `UiNumberInput` (min/max), `UiSwitch`, `UiSecretField`
  (masked, reveal, autocomplete off, password-manager ignore),
  `UiTagEditor` (key=value map), `UiSection`, `UiDrawer`. No
  list-of-strings chip input.

### v3 profile options (Q5, US8)

- **v3 BIG-IP `ssl_profile`** (`go-tangra-deployer/pkg/deploy/providers/
  bigip/bigip.go:152-181,527-600`): optional config key. After uploading
  `/<partition>/<base>.crt`, `.key` and (best effort) `<base>_chain.crt`,
  when `ssl_profile` is non-empty the provider POSTs
  `ltm/profile/client-ssl` `{name: /<partition>/<ssl_profile>, cert, key,
  chain: "none", ciphers: "DEFAULT"}`; on HTTP 409 (exists) it PATCHes
  `ltm/profile/client-ssl/~<partition>~<name>` with `{cert, key}` only
  (chain and every other profile setting untouched). A missing profile is
  therefore **created** with default ciphers. Details carry
  `ssl_profile`. Verify checks only that the certificate object exists.
  Rollback deletes the client-SSL profile named by `ssl_profile`
  (best effort, errors ignored), then cert, key and chain.
- **v4 BIG-IP today** (`go-tangra-deployer-v4/internal/providers/bigip/
  bigip.go`, `main`): always binds into its own profile
  `/<partition>/<base>_clientssl` (POST, PATCH `{cert, key, chain}` on
  409); no `ssl_profile`; Rollback deletes `<base>_clientssl`, cert, key,
  chain. Certificate objects are reinstalled with `overwrite` under the
  same name, so a renewal keeps the object names stable.
- **v3 FortiGate `default_ssl_profile`** (`.../fortigate/fortigate.go:
  41-60`, `ssl_profile.go`, `references.go`, `certificate.go:270-357`):
  part of the default `replace_strategy: ssl_profile` (other strategies
  `rebind`, `delete`; knobs `profile_suffix`, `rebind_references`,
  `prune_old`). Deploy: (1) find a local certificate with the same serial
  (FortiOS rejects re-importing identical content, error −145) and reuse
  it, else import under `<base>_<yyyymmdd>` (`_<nn>` on a same-day
  collision, names ≤ 35 characters) — never delete or overwrite;
  (2) **reference scan** of the certificate family (`<base>`, dated and
  sequence names): SSL/SSH profiles' `server-cert`, `vpn.ssl/settings
  servercert`, `system/global admin-server-cert`, `firewall/vip
  ssl-certificate`; any reference outside the audit profile and the
  default profile → **manual review** result (Success false, nothing
  bound); (3) pre-validate the default profile: must exist and have
  `server-cert-mode` `replace` (or empty), else manual review; (4) create
  (cloned from a replace-mode template) or update the per-certificate
  **audit profile** `<base>_ssl_profile`; (5) update the default profile
  in place: PUT `server-cert` with family entries replaced by the new
  name, other entries kept, appended when no family entry exists
  ("updated", "appended", "no-change"); best-effort list of firewall
  policies bound to the profile. Never deletes certificates or profiles.
  Verify: any family member present. Rollback: deletes unreferenced
  family members.
- **v4 FortiGate today** (`go-tangra-deployer-v4/internal/providers/
  fortigate/fortigate.go`, `main`): single strategy — delete an existing
  certificate with the same name, then import under `<base>` (the package
  comment says it "drops tangra's naming/versioning, reference-rebinding
  and ssl-profile machinery"). FortiOS refuses to delete a certificate
  that a profile references, so **a renewal of a certificate bound into an
  SSL/SSH profile fails** in v4 today (F13).

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
- **F8** (US5) The v4 drawer is two raw JSON text areas and its hints never
  render (UI reads `required_config`, API sends `config_fields`); the
  v4 server enforces no required field at save time — a configuration
  without a Cloudflare zone id saves and fails only at deploy. → generic
  descriptors served by the backend + save-time validation (D21).
- **F9** (US5) Six settings the providers read are undeclared, so a
  schema-driven form generated from today's declarations would *lose*
  v3 webhook options (authorization header, API key, timeout, TLS switch).
  → declare every read key; a test asserts declarations cover reads (D21).
- **F10** (US5, security) `aws_acm.endpoint` and `cloudflare.api_base`
  are test hooks read from the *stored* configuration: anyone with
  `configurations:manage` can point a configuration holding someone
  else's sealed AWS/Cloudflare credentials at an arbitrary host and
  receive the signed request / bearer token on the next deployment
  (credentials are not re-entered on edit). → not declared, refused at
  save, moved to unexported test options (D24).
- **F11** (US5) The 422 response carries no field, so the UI cannot
  highlight anything; webhook custom `headers` are stored unsealed in
  config and may carry auth headers. → `detail.fields`; refuse auth
  header names in `headers` (D24).
- **F12** (US8) v4 BIG-IP cannot bind into an operator-managed client-SSL
  profile; a v3 configuration with `ssl_profile` migrated to v4 silently
  binds into a new `<base>_clientssl` that no virtual server uses, leaving
  production on the old certificate. → `ssl_profile` (D26).
- **F13** (US8) v4 FortiGate deletes and re-imports under the same name;
  for a certificate bound into an SSL/SSH profile FortiOS refuses the
  delete, so renewals of profile-bound certificates fail. → dated import
  names + in-place profile update when `default_ssl_profile` is set
  (D27).
- **F14** (US5, Q7) v4 overrides are an unvalidated, unsealed JSON overlay
  with a name-list guard; making required fields satisfiable by overrides
  needs descriptor-level rules for which keys may be overridden, or a
  target manager could override, e.g., a Webhook URL and receive the
  configuration's sealed token. → `overridable` descriptor flag (D25).
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
4. `provider.Field` becomes the full field descriptor of D21 (`Type`,
   `Help`, `Placeholder`, `Group`, `Options`, `Default`, `Min`, `Max`,
   `MaxLength`, `Pattern`, `MaxItems`) and `Capabilities` gains
   `Description`, `TestConnection`, `SchemaVersion`, `OneOfRequired` — JSON
   additive (`omitempty`); declarations of the existing providers are
   completed in US5. `Field.Overridable` (D25) is part of the descriptor.
5. `provider.Result.Permanent bool` (US8, D27): the scheduler fails a job
   whose provider returns a permanent failure without retries, and stores
   failure `details` in the job result (today only success details are
   kept).

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
default 60)`. UI: the generic schema-driven form (D21/D22) renders the
provider's fields; its `host_selector` slot holds a host picker that calls the **inventory** API
through the gateway with the user's own session
(`GET /api/inventory/v1/hosts?search=&tag=&page=`) — so users see only
hosts they may read; on 401/403/404 the picker shows "enter host ids and
tags manually". "Validate" calls the deployer `Validate` endpoint, whose
response gains `details.matched_hosts` from `PreviewCertificateTargets`.

**Alternative rejected**: deployer proxies inventory host lists — the
deployer acts as a service and would disclose all tenant hosts to users
without inventory read.

### D21 — Provider schema served by the backend (US5)

**Decision**: each provider declares complete field descriptors in
`Capabilities()` (contracts/deployer-config-ui.md §2); `GET /providers`
serves them; one pure Go validator `provider.ValidateInput` applies them
on create, update, validate and target overrides; the UI generates its
form and zod schema from the same JSON. Shared test vectors
(`api/testdata/provider-field-vectors.json`: descriptor + input →
accepted / field errors) are run by the Go validator test and by the UI
`fieldsToZod` test, so client and server cannot drift. Every key a
provider reads is declared (test), undeclared keys are refused.

**Rationale**: v3 split the truth (required keys from the backend,
optional keys, secret names, labels and placeholders in the UI); adding a
provider meant editing two repos and the UI was silently stale (F8, F9).
Backend-served descriptors make a new provider (inventory-agent) appear
in the drawer with no UI change except custom widgets.

**Alternatives rejected**: (a) hard-coded per-provider forms/zod schemas
in the UI (v3 style) — drift, two places to change, no server-side
enforcement of optional-field rules; (b) JSON Schema documents per
provider plus a JSON-Schema form library — a new dependency (VI), weak
mapping to kit components and to "secret / group / help"; the descriptor
list is a small, typed subset and the inventory-agent JSON Schema in
deployer-provider.md §2 stays the documentation of the same rules;
(c) gRPC `GetProviderSchema` per provider — the HTTP catalogue already
exists and is read once per drawer open (≤ 10 providers).

### D22 — Mapping descriptors to kit components (US5)

**Decision**: a deployer-local `ProviderConfigForm.vue` renders sections
with `UiSection` and fields with kit inputs (`UiInput`, `UiTextarea`,
`UiNumberInput`, `UiSwitch`, `UiSelect`/`UiCombobox`, `UiSecretField`,
`UiTagEditor`); a deployer-local `StringListInput.vue` covers
`string_list`; `host_selector` is a named slot (US6 fills it with
`HostPicker`). `fieldsToZod(caps)` builds the zod object for
`config` and `credentials` (nested), used by `useZodForm`, so the kit's
error focus and `setServerError` (`detail.fields` with
`config.<key>` paths) work unchanged.

**Rationale**: kit `zodToFields`/`UiRecordForm` derive fields from a zod
schema but have no group, help-from-schema, URL, switch or slot per
field, and cannot express "secret stored, leave blank"; a local renderer
over the kit inputs keeps the kit unchanged (no kit release on the
critical path) while using the same components and a11y wiring.

**Alternatives rejected**: extend kit `FieldDef`/`UiFieldRenderer` first
(new kit release and pin in every remote before the deployer can ship;
can be upstreamed later); keep JSON editors with a schema hint (does not
meet the request).

### D23 — Secrets on edit (US5)

**Decision**: credentials stay one sealed blob (AD = configuration id);
reads return `credentials_set` (key names) and, to managers only,
`credentials_public` (values of non-secret credential fields such as
host, username, access key id); updates merge per field — empty keeps,
value replaces, `clear_credentials` removes optional keys — and the
merged result is validated before resealing. Validate accepts
`configuration_id` to merge stored secrets so "Test connection" works on
edit without re-entering them.

**Rationale**: v3 and current v4 replace the whole blob when any
credential is sent, so changing the BIG-IP password silently drops the
host and username unless all are re-entered; per-field merge is what the
"leave blank to keep" hint promises.

**Alternatives rejected**: separate sealed column per secret (migration,
more envelopes, no benefit); return secrets masked (`****`) and treat
the mask as "keep" (masks leak length/existence semantics and risk
storing the mask); move host/username out of credentials into config
(changes the provider contract and existing rows).

### D24 — Strict keys, legacy rows and credential-redirect hardening (US5)

**Decision**: undeclared keys → 422 `unknown_field` on create/update;
rows stored before the feature are not rewritten — they deploy as before
and the drawer shows undeclared keys as "removed when you save". The
`endpoint`/`api_base` overrides move to unexported provider options used
only by tests; a stored config value is ignored. Webhook `headers`
refuse `Authorization`, `Proxy-Authorization`, `Cookie`, `X-API-Key`,
`X-Webhook-Secret`. A deployment whose effective config misses a
required field fails before the provider runs ("configuration
incomplete: <label>").

**Delivery split (2026-10-02)**: the exfiltration fix itself (`endpoint`/
`api_base` no longer read from stored config, authentication header
names refused in webhook `headers`) ships first as the deployer hotfix
`fix/provider-endpoint-exfil`; 033 does not re-implement it. 033 adds the
descriptor-level guard (undeclared keys → `unknown_field`, the hotfix's
forbidden-header list reused by `ValidateInput` → `forbidden_header`) and
keeps regression tests for both, so the hole cannot reopen through the
new validator or through target overrides (D25).

**Rationale**: F10 is a credential-exfiltration path; strict keys make
the descriptor list authoritative. Not rewriting legacy rows avoids a
data migration of opaque JSON.

**Alternatives rejected**: silently drop unknown keys on save (hides
mistakes); data migration that strips unknown keys (risky, needs a
backup, gains nothing for rows nobody edits).

### D25 — Where "required" is enforced: target-supplied fields (US5, Q7 — revised 2026-10-02)

**Context**: the user decided (Q7) that a required field may be supplied
only by a deployment target override, as the v3 backend allowed
(`effective_config.go:38-60` checked required keys on the merged
configuration). The original default (configuration must be complete) is
withdrawn.

**Decision**:
1. **Descriptor flag** `overridable: bool` (default false). Allowed only
   on `config_fields` and never with `secret` (`CheckCapabilities` panics
   at registration otherwise). Per-provider choice (contract §7):
   overridable = non-secret configuration values that select *what* or
   *where inside the same account/appliance* to deploy — aws_acm
   `region`, `certificate_arn`; bigip `partition`, `ssl_profile`;
   cloudflare `zone_id`; fortigate `vdom`, `import_scope`,
   `default_ssl_profile`; dummy `fail`; webhook `timeout_seconds`,
   `metadata`; inventory-agent all six fields. **Not** overridable:
   every credential field (secret or not: BIG-IP/FortiGate `host`,
   `username`, AWS `access_key_id` — they are sealed with AD = the
   configuration id, and `host` decides where the sealed password/token
   is sent), webhook `url`, `verify_url`, `rollback_url` (receive the
   sealed token/secret), `skip_tls_verify` (weakens the TLS that protects
   those credentials) and `headers` (authentication surface).
2. **Secrets in overrides are forbidden, not sealed.** `ConfigOverrides`
   is stored unsealed in the target row; a secret can never be
   overridable, an override key that is a credential field or not
   overridable is refused (`not_overridable`), and the existing
   `rejectCredentialKeys` name guard stays as defence in depth.
3. **Configuration save** (`ValidateInput` mode `configuration`): a
   missing required field — or a fully empty `one_of_required` group — is
   accepted iff every field concerned is overridable; the view returns
   `target_supplied: [keys]` (computed, not stored). Non-overridable
   missing fields → 422 `required` as before. All other rules apply to
   the values that are present.
4. **Target attach** (`targets.Attach`, used by the target form on create
   and edit; replaces the override of every listed configuration):
   `ValidateOverride(caps, config, override)` per configuration — keys
   must be declared and overridable, values follow the descriptor rules,
   and the merged config (mode `effective`) must satisfy every required
   field and group. Errors → 422 `detail.fields` with
   `config_overrides.<key>` and `detail.configuration_id`; the request is
   atomic (validated completely before any write). Attaching a
   configuration with `target_supplied` fields without an override that
   supplies them is refused the same way.
5. **Configuration update** re-checks every target the configuration is
   attached to; if clearing a value would leave a target without a
   required value → 422 `config.<key>: required_by_targets` and
   `detail.targets: [{id, name}]` (≤ 20).
6. **Job start**: `MissingRequired(caps, merged)` before the lcm fetch and
   before the provider runs; message "configuration incomplete: <label>
   must be provided by the target" (labels only). A direct deployment of
   a configuration with `target_supplied` fields fails the same way (and
   the UI disables it).
7. **Validate / Test connection** on a configuration with
   `target_supplied` fields uses the descriptor default for an empty field
   when one exists (FortiGate `vdom` → `root`), otherwise runs only the
   input checks; the response says `checked: "partial"` and lists
   `deferred` keys.
8. **UI**: the drawer labels an empty overridable required field "To be
   provided by each target" (hint on the input: "Required — or leave
   empty and let each target provide it"), shows a non-blocking warning
   on save and a list badge "Needs target values"; the target form
   renders, per attached configuration, `ProviderConfigForm` in
   `override` mode (only overridable fields; `target_supplied` ones
   required; others show the inherited value as placeholder; empty =
   inherit), replacing the JSON text area.

**Rationale**: v3 parity for shared configurations (one Cloudflare token
for many zones, one AWS key for many regions/ARNs, one appliance with
several partitions/VDOMs, one inventory-agent configuration for many host
groups) without reopening F10/F14: what a target may change is declared
per field and never includes where credentials go. Validating on attach
and on configuration update makes incompleteness visible when it is
created, and the job-start check covers rows that predate the rules.

**Alternatives rejected**:
- *Configuration must be complete* (previous default) — rejected by the
  user (Q7); forces one configuration (and one copy of the sealed
  credentials) per zone/region/VDOM.
- *Any config key overridable, required checked only at job start* (v3
  backend exactly) — the operator learns about a missing value only when a
  renewal fails, and a target manager could override Webhook URLs and
  receive sealed tokens (F14).
- *Seal overrides (per-target sealed blob) so credentials could be
  overridden too* — needs a new sealed column, AD binding to target +
  configuration, a second write-only merge UX in the target form and a
  migration; and a per-target host for BIG-IP/FortiGate would still let a
  target manager redirect the configuration's password. A target that
  needs different credentials uses its own configuration.
- *Overridable flag chosen by the operator per configuration* — moves a
  security decision (which fields may move credentials) into user data;
  the provider author knows which fields are safe.
- *Allow target-supplied fields only through a separate "template
  configuration" type* — new entity and UI for the same effect.

**Note on inventory-agent**: making `host_ids`/`host_tags` overridable
lets a holder of `targets:manage` choose which tenant hosts receive a
certificate *with its key* through a shared configuration. That is the
v3 behaviour (v3 overrides were unrestricted) and the receiving hosts are
still limited to the tenant's enrolled `cert.v1` agents, with every
delivery audited; recorded as open question Q8.

### D26 — BIG-IP `ssl_profile` (US8, Q5)

**Decision**: optional config field `ssl_profile` (string, group
Options, overridable, pattern
`^(/[A-Za-z0-9_.-]{1,64}/)?[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`; a bare
name is resolved in the configured `partition`, a full path is used as
is). When set:
1. **Pre-check** (before any upload): `GET ltm/profile/client-ssl/~P~name`;
   404 → `Result{Success: false, Permanent: true, Message: "client-SSL
   profile <path> not found"}` (D27 §6), nothing uploaded. Other errors →
   error (job retry).
2. Upload `<base>.crt`, `<base>.key`, `<base>_chain.crt` exactly as
   without the option (same names, `overwrite` on renewal).
3. `PATCH ltm/profile/client-ssl/~P~name` with `cert`, `key` and — only
   when a chain object was installed — `chain` (v3 PATCHed `{cert, key}`;
   v4's own profile binding already sets the chain). Every other profile
   setting is untouched. No POST, no `<base>_clientssl`.
4. Details: `ssl_profile` (full path), `ssl_profile_mode: "existing"`.
- **Verify**: certificate object exists **and** the profile's `cert` /
  `key` equal the deployed object paths; otherwise `Success: false`
  "profile <path> is bound to a different certificate".
- **Rollback**: never deletes or PATCHes the operator's profile (v3
  deleted it — would take down every virtual server using it). If the
  profile still references the deployed cert/key (BIG-IP refuses to
  delete referenced objects anyway) → `Success: false` "certificate is
  bound to client-SSL profile <path>; bind another certificate before
  rolling back"; nothing deleted. If the profile no longer references
  them, cert/key/chain are deleted as today.
- **Test connection**: after the credential probe, when `ssl_profile` is
  set, the profile GET; 404 → 422 `config.ssl_profile:
  not_found_on_endpoint`.

**Deviation from v3 (deliberate)**: v3 *created* a missing profile
(POST with default ciphers and no chain); 033 refuses instead — the user's
answer says "existing", and a typo would otherwise create an unused
profile while production keeps the old certificate (F12 again).

**Alternatives rejected**: `certKeyChain` array manipulation (multi-cert
profiles: RSA + ECDSA) — not v3 behaviour; the top-level `cert`/`key`/
`chain` attributes update the default entry, which is what v3 relied on;
recorded as Q10 if dual-key profiles are needed.

### D27 — FortiGate `default_ssl_profile` (US8, Q5)

**Decision**: optional config field `default_ssl_profile` (string, group
Options, overridable, pattern `^[^\x00-\x1f"\\/]{1,35}$`, help "Existing
SSL/SSH inspection profile in server-certificate mode replace (protecting
an SSL server); its server certificate list is updated in place"). When
set, Deploy follows the v3 `ssl_profile` strategy **without** the audit
profile:
1. Leaf only (FortiOS rejects chains, −145); `base` from the leaf subject
   (v4 `certName`). Reuse a local certificate with the same serial
   (`findLocalCertBySerial`); otherwise resolve a free name
   `<base>_<yyyymmdd>` / `<base>_<yyyymmdd>_<nn>` (nn 01–99, ≤ 35 chars,
   v3 `resolveFreeImportName`) and import; verify presence. Never delete
   or overwrite a certificate.
2. Reference scan (v3 `scanReferences`) of the family (`familyMatcher`:
   `<base>`, dated, dated+sequence names): SSL/SSH profiles other than
   `default_ssl_profile`, VIPs, SSL-VPN `servercert`, admin GUI
   certificate → any hit → manual review. A scan error → manual review.
3. Pre-validate the profile: exists, `server-cert-mode` is `replace` or
   empty → else manual review.
4. `PUT firewall/ssl-ssh-profile/<name>` with only `server-cert`: family
   entries replaced by the new name (order kept, duplicates removed),
   others kept; append if no family entry; skip the PUT when already
   current. Actions `updated`/`appended`/`unchanged`.
5. Best effort: firewall policies bound to the profile (`bound_policies`);
   failure ignored.
6. Manual review = `Result{Success: false, Message: "MANUAL REVIEW
   REQUIRED: …"}` with `details.manual_review_required`, `reason`,
   `foreign_references`, `imported`. Today the scheduler retries every
   failed `Result` and stores `details` only on success
   (`internal/jobs/scheduler.go:97-105`); 033 adds `Result.Permanent bool`
   — a permanent failure goes to `fail` without retries — and stores the
   failure `details` in the job result, so the manual-review reason and
   references are visible in the job drawer. BIG-IP "profile not found"
   (D26) is permanent too.
- **Verify**: a local certificate with the deployed serial exists **and**
  its name is in the profile's `server-cert` list.
- **Rollback**: find the newest other family member on the device (dated
  names sort chronologically; `<base>` oldest); PUT the profile list with
  it in place of the deployed name; then delete the deployed certificate
  (left in place and reported if FortiOS says it is still referenced). No
  other family member → `Success: false` "no previous certificate to
  restore; profile unchanged".
- Without `default_ssl_profile`: unchanged v4 behaviour (delete +
  import under `<base>`).

**Not carried over** (recorded as Q9): v3 per-certificate audit profile
`<base>_ssl_profile` cloned from a replace-mode template, the
`replace_strategy` alternatives `rebind`/`delete` and the knobs
`profile_suffix`, `rebind_references`, `prune_old`; v3 also excused
references from the audit profile in the scan — without it, only
`default_ssl_profile` is excused. "Server SSL profile" in the user's answer
is read as this SSL/SSH inspection profile in "protecting SSL server"
(replace) mode, which is the only profile type v3 updated; FortiOS
`firewall ssl-server` (SSL offload) objects are out of scope (Q9).

**Rationale**: fixes F13 (renewal of a profile-bound certificate) and
restores v3's production-safe behaviour; the code is ported from v3 with
its tests (`naming_test.go`, `deploy_test.go` vectors) onto the v4 client.

**Alternatives rejected**: keep delete+import and temporarily unbind the
profile (outage window, fails for VIPs); always use dated names even
without the option (changes object names for existing v4 users).

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
| **I** (US5) | Secret echoed by the configuration drawer or API | Write-only secrets (`credentials_set` names only), `credentials_public` only for non-secret fields and only to managers, error codes built from descriptors only (fuzz test), SC-008 scan of responses/logs/audit for submitted test secrets. |
| **I** (US5) | Stored config redirects sealed credentials to an attacker host (`endpoint`, `api_base`, auth headers in `headers`) | Undeclared keys refused at save, test overrides not read from stored config, auth header names refused in `headers` (D24). |
| **T** (US5) | Client-side validation bypassed by a direct API call | Same descriptors enforced server-side on create/update/validate/target override (D21); API tests per required field (SC-007). |
| **T/I** (US5, Q7) | Target manager redirects a configuration's sealed credentials through a target override (Webhook URL, appliance host) or smuggles a secret into the unsealed override JSON | Only descriptor-declared `overridable` fields accepted in overrides (never credentials, secrets, URLs, TLS switch, headers), enforced at attach and re-checked at job start; `rejectCredentialKeys` kept; `CheckCapabilities` refuses `overridable` on credential/secret fields (D25). |
| **D** (US8) | Deployment breaks a production appliance profile (wrong profile, profile shared by other domains, certificate referenced elsewhere) | Pre-checks before the first write (profile exists, FortiGate mode `replace`, reference scan → manual review), in-place update of only the family entry, never delete/recreate profiles or certificates during deploy, Rollback never touches the operator's BIG-IP profile (D26, D27). |
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

- **Q5** v3 fields missing in v4 (BIG-IP `ssl_profile`, FortiGate
  `default_ssl_profile`): **include in 033** with v3 behaviour → US8,
  D26, D27 (the earlier default "out of scope" is withdrawn).
- **Q6** Non-secret credential values (host, username, AWS access key id)
  shown to managers on edit: **yes** — the default is kept (D23, SR-011).
- **Q7** A required field may be supplied only by a deployment target
  override: **yes, allowed** (v3 backend behaviour) → D25 rewritten
  (`overridable` descriptor flag, target-supplied fields, validation on
  attach, configuration update and job start).
- The credential-exfiltration issue (aws_acm `endpoint`, cloudflare
  `api_base`, webhook authentication headers) is fixed by the separate
  deployer hotfix `fix/provider-endpoint-exfil`; 033 keeps SR-014 and the
  regression tests through the shared validator (D24).

## Open questions (raised by the Q5/Q7 redesign, defaults chosen)

- **Q8** Inventory-agent `host_ids`/`host_tags` overridable? It lets a
  holder of `targets:manage` direct certificates *with keys* to any
  enrolled `cert.v1` host of the tenant through a shared configuration
  (v3 allowed any override). **Default**: overridable (v3 parity; all
  deliveries audited). Alternative: not overridable — one inventory-agent
  configuration per host group.
- **Q9** v3 FortiGate extras not carried over: per-certificate audit
  profile `<base>_ssl_profile` cloned from a template, `replace_strategy`
  `rebind`/`delete`, `profile_suffix`, `rebind_references`, `prune_old`,
  and FortiOS `firewall ssl-server` (SSL offload) objects. **Default**: out
  of scope for 033 (the user named only `default_ssl_profile`); follow-up
  if v3 configurations use them.
- **Q10** BIG-IP dual-certificate profiles (RSA + ECDSA in
  `certKeyChain`): **Default**: not supported — `ssl_profile` updates the
  profile's default certificate/key/chain like v3.
- **Q11** BIG-IP `ssl_profile` naming a profile that does not exist: v3
  created it; **Default**: refuse ("existing" in the user's answer; a typo
  must not create an unused profile). Alternative: a "create if missing"
  switch.
