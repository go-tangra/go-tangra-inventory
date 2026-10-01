# Implementation Plan: Deliver Issued and Renewed Certificates to Inventory-Agent Hosts

**Branch**: `033-agent-cert-delivery` | **Date**: 2026-10-02 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/033-agent-cert-delivery/spec.md`

## Summary

A new deployer provider **`inventory-agent`** selects inventory hosts by id
and/or tags. When a job runs (manual, automatic on `certificate.issued` /
`certificate.renewed` with certificate filters, or a retry), the provider
sends the inventory module a **delivery request by reference** (tenant,
job id as idempotency key, certificate id, name, selection, key policy)
over a new mesh service `inventory.v1.CertificateDeliveryService`; the
deployer never fetches the key for this provider. The inventory resolves
the hosts under RLS, persists one **delivery item** per host (no material),
supersedes older items for the same host and name, and pushes a
`CERTIFICATE` command carrying only the item id — immediately to online
agents and again on every reconnect (the 023 replay pattern), so offline
hosts receive renewals later. The agent pulls the material with
`FetchCertificate(item_id)`; only then does the inventory download the
bundle from lcm (`Certificates/Download`, new policy rule), validate it,
relay it and drop it — the key exists only in that call's memory. The
agent validates the bundle, writes it atomically into its fixed local
directory in the certbot layout (`live/<name>` → generation directory
swap), skips unchanged certificates, runs only a **locally configured**
deploy hook with the v3 `LCM_*` environment, and reports
installed/unchanged/failed/hook-failed with serial and fingerprint. The
deployer job summarises per-host results (queued hosts do not fail it),
retries re-arm failed hosts, Verify compares reported fingerprints, and
revocations forwarded by the deployer flag hosts without deleting files.
The inventory UI gets a per-host Certificates tab and an agent capability
column. The deployer's configuration drawer becomes **schema-driven for
every provider** (US5, amendment): providers declare full field
descriptors (type, required, secret, default, options, help,
placeholder, group, validation), `GET /providers` serves them, one Go
validator enforces them on save/validate/target override (422 with
`detail.fields`), credentials merge per field on edit (secrets
write-only), and the UI renders provider-first, grouped, typed inputs
from the descriptors with "Test connection"; the inventory-agent form
(US6) plugs a host picker into that generic form.

## Technical Context

**Language/Version**: Go 1.26 (inventory service + agent, deployer),
TypeScript/Vue 3 (inventory and deployer UI remotes, `@go-tangra/ui`)

**Primary Dependencies**: go-tangra/v4 framework (mesh client/server,
listquery), pgx/goose, lcm SDK `sdk/v4.1.0` (existing in both modules;
`lcmv1.CertificatesClient`), inventory SDK **`sdk/v4.4.0`** (new
`CertificateDeliveryService`, consumed by the deployer), stdlib
`crypto/x509`, `crypto/sha256`, `encoding/pem`, `os/user`, `syscall`.
**No new third-party dependency.**

**Storage**: inventory PostgreSQL/TimescaleDB with RLS — migration
**0010** (`inventory_cert_deliveries`, `inventory_cert_delivery_items`,
`inventory_host_certificates`; no material columns). Deployer: no
migration (config JSON). Agent: `/etc/inventory-agent/certs` (configurable).

**Testing**: `go test -race` with memstore and fakes (fake lcm
`CertificatesClient`, fake registry, fake inventory client in the
deployer, fake filesystem and exec for the agent store/hook core);
negative security tests (foreign item ids, inactive items, fetch limits,
plaintext ingest, unsafe names/tags, oversized/mismatched bundles,
symlinked store directories, writable hooks, server-supplied paths,
source not allowed); fuzz targets for every new parser (name, tag, PEM
bundle, report, agent config, provider config); testcontainers
integration (migration on populated DB, RLS, partial unique index,
bufconn end-to-end mesh → ingest → fake lcm, **key scan** of DB dump,
logs, audit, Valkey); a privileged container test for real file
ownership/modes and hook execution; vitest for both UIs; contract tests
(`buf breaking`, OpenAPI routes/permissions, provider capability JSON).
Deployer drawer (US5): descriptor validator unit + fuzz tests at 100 %,
shared accept/reject vectors (`api/testdata/provider-field-vectors.json`)
run by Go and by vitest (`fieldsToZod`), golden capability JSON per
provider, secret-echo scan of responses/logs/audit (SC-008), vitest-axe
and Playwright drawer flow.

**Target Platform**: Linux containers (freya-stack / production compose)
for inventory, deployer, lcm; agent on Linux amd64/arm64 (deb, rpm,
binary). Windows agents: capability not announced (research D15).

**Project Type**: multi-repo platform change — go-tangra-inventory-v4
(primary: proto/SDK, relay service, ingest, agent, UI),
go-tangra-deployer-v4 (provider, small core changes, consumer, UI),
go-tangra-lcm-v4 (policy file only), go-tangra-docker (configs/policies,
user-applied).

**Performance Goals**: SC-001 20 online hosts installed and job done
≤ 60 s; SC-002 renewal on online hosts ≤ 2 min, offline hosts ≤ 1 min after
reconnect; fetch handler p95 ≤ 300 ms excluding lcm; ≤ 50 concurrent
fetches per replica.

**Constraints**: additive protos; forward-only migration; old agents
ignore `CERTIFICATE` commands (unsupported state, not errors); server
delivery off by default; no material persisted/cached/logged/published
(SR-002); hook only from local config (decision 2); fixed directory, name
only from server (decision 3); coverage gates — inventory 100 % for
existing security packages + `internal/certmaterial`,
`internal/certdelivery`, `internal/agentcerts`; deployer ≥ 80 % total and
100 % for `internal/providers/inventoryagent` evaluation/config code;
≥ 80 % total in both.

**Scale/Scope**: ≤ 1000 agents per tenant, ≤ 1000 hosts per delivery,
≤ 100 names per host in practice; 3 repos with code changes (inventory,
deployer, lcm policy); 3 new inventory packages, 1 new deployer
provider; 1 migration; 1 new mesh service (5 RPCs), 2 new ingest RPCs, 1
command type, 4 inventory HTTP operations; 0 new permissions. Drawer
(US5): 7 providers × ≤ 12 fields, 0 new deployer routes (3 extended:
`GET /providers`, configuration write/read, validate), 0 migrations.

## Constitution Check

*GATE: checked before Phase 0 and re-checked after Phase 1 design — all PASS.*

- [x] **I. Secure by Default**: `cert_delivery.enabled=false` on the
      server; hook disabled by default; plaintext delivery refused on both
      sides unless named dev opt-outs are set (`allow_plaintext_ingest`
      refused with `env=production`; agent `allow_insecure_transport`),
      both logged at start; restrictive default modes (key 0600, dir
      0750). Agent `certificates.enabled` defaults to true (opt-out) — see
      research Q1; justified because files are confined to a dedicated
      directory and nothing executes without a local hook.
- [x] **II. Zero Trust**: two new mesh edges, each allowed per method by
      SPIFFE policy (deployer → `CertificateDeliveryService/*`; inventory →
      `lcm.v1.Certificates/Download`), plus handler allow-list
      `cert_delivery.sources`; agent edge unchanged (per-agent credential,
      interceptor covers new methods, `auth.go:39-44`); fetch/report bound
      to the verified agent's own item.
- [x] **III. Boundary Validation**: proto bounds documented; provider
      field descriptors enforced server-side on every configuration
      write/validate/target override with unknown keys refused (US5,
      D21/D24); provider `ValidateConfig` (deployer), request validation (inventory mesh),
      `certmaterial.ParseBundle` on lcm material before relaying, agent
      re-validation (name, PEM, key match, validity, sizes), DB CHECKs,
      OpenAPI `additionalProperties: false`; bounds table research D17.
- [x] **IV. Test-First**: tasks list tests first per phase; negative and
      fuzz tests explicit; new security packages at 100 %; key-scan test
      (SC-003).
- [x] **V. Observability**: transactional audit for every transition incl.
      material fetch (no material in detail; `audit.Validate` rejects PEM);
      content-free realtime events; metrics (deliveries by state, fetches,
      lcm latency, refusals); no secrets in logs (agent and server).
- [x] **VI. Supply Chain**: no new dependency; stdlib crypto only; agent
      shipped through the existing signed release pipeline (023).
- [x] **VII. Simplicity**: one relay service, one pure material package
      shared by server and agent, one agent store package; typed config
      sections; complexity recorded below.
- [x] **Threat Model**: STRIDE in [research.md](research.md#stride-threat-model).

## Project Structure

### Documentation (this feature)

```text
specs/033-agent-cert-delivery/
├── spec.md  plan.md  research.md  data-model.md  quickstart.md  tasks.md
├── contracts/{inventory-grpc.md,inventory-http.md,deployer-provider.md,deployer-config-ui.md,agent-config.md,audit-events.md,mesh-policy.md}
└── checklists/requirements.md
```

### Source Code

```text
go-tangra-inventory-v4 (this repo)
  sdk/api/proto/inventory/v1/inventory.proto (+ generated)    # contracts/inventory-grpc.md §1-2
  sdk/pkg/inventoryclient/certdelivery.go                     # SDK v4.4.0 helper (§3)
  internal/certmaterial/                       # NEW pure: name/tag rules, PEM bundle parse, key match, fingerprint (100 %)
  internal/certdelivery/                       # NEW relay service: create/resolve/supersede/deliver/onconnect/fetch/report/sweep/revoke/verify (100 %)
  internal/lcmclient/lcmclient.go              # NEW Certificates.Download wrapper (mesh)
  internal/agentcerts/                         # NEW agent store + idempotency + hook core (100 %); hook_linux.go/fs_linux.go OS glue (excluded)
  internal/store/models.go
  internal/store/migrations/0010_cert_delivery.sql
  internal/repo/repo.go, internal/repo/repodb/certdelivery.go, internal/memstore/certdelivery.go
  internal/registry/registry.go                # CommandCertificate + CertificatePayload (ids only)
  internal/ingest/{certificates.go,ingest.go,upgrade.go}   # Fetch/Report, command mapping, OnConnect replay
  internal/grpcapi/{certdelivery.go,servers.go}            # mesh service + source allow-list
  internal/repo/repodb/db.go, internal/memstore/memstore.go     # DeleteHost / agent revocation cancel active items in the same tx
  internal/audit/audit.go, internal/events/events.go
  internal/httpapi/{certificates.go,handlers.go,deps.go}, api/openapi/inventory.yaml
  internal/config/config.go                    # cert_delivery section; AgentConfig.Certificates
  internal/daemon/daemon.go                    # cert.v1 capability, CERTIFICATE command → agentcerts
  cmd/inventory-agent/main.go                  # wiring (Linux only)
  internal/app/app.go                          # lcm client, service, sweeper, gRPC registration
  pkg/inventorymanifest/manifest.go            # no new permission; routes declared
  packaging/agent.yaml                         # commented certificates section
  deploy/policy.yaml                           # rule deployer-cert-delivery
  ui/src/views/hosts/detail.vue, ui/src/views/agents/index.vue,
  ui/src/stores/certificates.ts, ui/src/api/{types.ts,schema.d.ts}
  Makefile, scripts/coverage-gate.sh, README.md, deploy/README.md, SECURITY.md

go-tangra-deployer-v4
  go-tangra-deployer-v4/go.mod                                  # inventory SDK v4.4.0 (temporary replace during development)
  go-tangra-deployer-v4/internal/provider/provider.go           # Field types, DeliversByReference, JobMeta, ConfigValidator, Previewer
  go-tangra-deployer-v4/internal/jobs/{scheduler.go,jobs.go}    # includeKey per capability; WithJob
  go-tangra-deployer-v4/internal/configs/configs.go, internal/targets/targets.go   # ValidateConfig, preview details
  go-tangra-deployer-v4/internal/providers/inventoryagent/{inventoryagent.go,config.go,evaluate.go,name.go}
  go-tangra-deployer-v4/internal/inventoryclient/inventoryclient.go   # adapter over the SDK (mesh conn)
  go-tangra-deployer-v4/internal/events/consumer.go             # certificate.revoked forwarding
  go-tangra-deployer-v4/internal/audit/audit.go
  go-tangra-deployer-v4/internal/config/config.go, internal/app/app.go
  go-tangra-deployer-v4/internal/httpapi/handlers.go, api/openapi/deployer.yaml   # validate response details, capability fields
  go-tangra-deployer-v4/ui/src/views/jobs/index.vue,
  go-tangra-deployer-v4/ui/src/components/HostPicker.vue,                     # US6 (slot of ProviderConfigForm)
  go-tangra-deployer-v4/ui/src/api/inventory.ts
  go-tangra-deployer-v4/Makefile (fuzz targets), README.md

  # US5 — schema-driven configuration drawer (contracts/deployer-config-ui.md)
  go-tangra-deployer-v4/internal/provider/schema.go             # NEW pure: descriptor checks + ValidateInput (100 %)
  go-tangra-deployer-v4/internal/providers/{awsacm,bigip,cloudflare,dummy,fortigate,webhook}/*.go   # full descriptors; endpoint/api_base → test options
  go-tangra-deployer-v4/internal/configs/configs.go             # schema validation, per-field credential merge, credentials_set/public, validate(configuration_id)
  go-tangra-deployer-v4/internal/targets/targets.go             # merged override validation
  go-tangra-deployer-v4/internal/jobs/scheduler.go              # "configuration incomplete" pre-check
  go-tangra-deployer-v4/internal/httpapi/{deps.go,handlers.go}, api/openapi/deployer.yaml   # 422 detail.fields, schemas
  go-tangra-deployer-v4/api/testdata/provider-field-vectors.json   # shared Go/TS vectors
  go-tangra-deployer-v4/ui/src/components/{ProviderConfigForm.vue,StringListInput.vue}
  go-tangra-deployer-v4/ui/src/schemas/{providerFields.ts,configuration.ts}   # fieldsToZod
  go-tangra-deployer-v4/ui/src/views/configurations/index.vue   # drawer rewrite (provider-first, sections, test connection)
  go-tangra-deployer-v4/ui/src/stores/{providers.ts,configurations.ts}, ui/src/api/{types.ts,schema.d.ts}
  go-tangra-deployer-v4/ui/tests/unit/{provider-form.spec.ts,provider-schema.spec.ts}, ui/tests/e2e/{deployer-flow.spec.ts,a11y.spec.ts}

go-tangra-lcm-v4
  go-tangra-lcm-v4/deploy/policy.yaml          # rule inventory-download (+ policy test)

go-tangra-docker (user-applied)
  go-tangra-docker/policies/{lcm.yaml,inventory.yaml}, go-tangra-docker/configs/{inventory.yaml,deployer.yaml}
```

**Structure Decision**: the deployer owns *when* and *what to whom*
(selection, triggers, retries, verification); the inventory owns the
host channel and delivery state; lcm stays the only store of keys. All
security-relevant logic sits in pure packages (`certmaterial`,
`certdelivery`, `agentcerts` core, deployer `inventoryagent` config and
evaluation) proven by unit + fuzz tests at 100 %; OS effects (chown,
symlink swap on a real FS, process groups) sit behind small interfaces and
are covered by a privileged container test.

## Rollout

1. **inventory `sdk/v4.4.0`** (proto + `inventoryclient` delivery helper).
2. **inventory v4.7.0**: migration 0010, relay service, ingest RPCs, agent
   4.7.0 with `cert.v1`, UI; `cert_delivery.enabled` false by default. The
   tag workflow's signing job waits for the user's `release` environment
   approval. Deploy with a DB backup first.
3. Agents: "Upgrade all" (023 self-upgrade); no manual install.
4. **Policies/configs** (go-tangra-docker, user): lcm `inventory-download`,
   inventory `deployer-cert-delivery`, inventory `cert_delivery` section,
   deployer `inventory` section + discovery. Repo policy files updated in
   inventory and lcm (lcm needs no release: production mounts
   `policies/lcm.yaml`).
5. **deployer v4.4.0** (inventory SDK v4.4.0, provider, schema-driven
   drawer for all providers, UI). Deploy. Existing configurations keep
   working (legacy rows are validated only when edited).
6. Verify on one production host (quickstart manual 1–3) before enabling
   auto-deploy targets.

Tags, merges, pins, production deploys and signing approvals are
user-confirmed steps (tasks.md Phase 10).

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| Inventory fetches from lcm at agent-pull time (second lcm caller) | Key never persisted in inventory (decision 1) and offline hosts must get renewals later | Deployer pushes material to inventory: inventory would have to keep keys until agents connect |
| Provider registered during app wiring instead of `init()` | The provider needs an injected mesh client; absent when inventory is not configured | Global setter (v3 `SetPusher`) is hidden mutable state; init-time registration would list an unusable provider |
| `DeliversByReference` + `JobMeta` in the deployer core | The scheduler always fetched the key (F1) and providers had no tenant/job id (F2) | Ignoring the key would still make the deployer hold it and fail key-less certificates; separate interface duplicates the scheduler |
| Directory-symlink generation swap | Atomic replacement of the certificate *and* key set (FR-015) | Per-file rename exposes cert/key mismatch windows; directory rename over non-empty directory is impossible |
| Persisted delivery items + replay on connect | Registry delivery is fire-and-forget; v3 lost pushes to offline clients | Pub/sub only repeats v3's weakness |
| Deployer-local form renderer instead of extending the kit `FieldDef` (US5) | Needs groups, slots, URL/switch/list types and "stored secret" state now | Kit-first change needs a kit release and re-pin of every remote before the deployer ships; can be upstreamed later |
| Shared `internal/certmaterial` used by agent and service | Same name/bundle rules on both sides (defence in depth) without drift | Two implementations drift; deployer keeps a tested copy of the name rule only (cross-module test vectors) |
