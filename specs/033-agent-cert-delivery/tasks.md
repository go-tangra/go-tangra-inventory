---

description: "Task list for 033 Deliver Issued and Renewed Certificates to Inventory-Agent Hosts"
---

# Tasks: Deliver Issued and Renewed Certificates to Inventory-Agent Hosts

**Input**: Design documents from `specs/033-agent-cert-delivery/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/

**Tests**: MANDATORY (Constitution IV). In every phase the tests are listed
first and must be written and seen failing before the implementation tasks
of that phase. Negative security tests and fuzz tests are listed explicitly.

**Paths**: paths without a prefix are in this repository
(go-tangra-inventory-v4: proto/SDK, inventory service, agent, UI). Deployer
paths are prefixed `go-tangra-deployer-v4/…`, lcm paths
`go-tangra-lcm-v4/…`; stack files `go-tangra-docker/…` are edited by the
user (tasks only prepare the change).

**Release tasks** (tags, PR merges, signing approval, stack pins,
production deploys) require explicit user confirmation before they are
executed. Commits carry no Co-Authored-By trailer.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files/repos, no dependency on an unfinished task)
- **[Story]**: US1–US6 from spec.md

---

## Phase 1: Setup

**Purpose**: branches, gates, package skeletons, fixtures.

- [ ] T001 Create branch `033-agent-cert-delivery` in `go-tangra-deployer-v4` and `go-tangra-lcm-v4` from `main` (this repo already on it)
- [ ] T002 [P] Add `internal/certmaterial`, `internal/certdelivery`, `internal/agentcerts` to `SECURITY_PKGS` in `scripts/coverage-gate.sh`; exclude `internal/agentcerts/*_linux.go` OS glue from `COVERPKG` in `Makefile` like `internal/collector`; add `FuzzValidName`, `FuzzParseBundle`, `FuzzHostTag`, `FuzzReportCertificate`, `FuzzAgentCertsConfig` to the `fuzz` target in `Makefile`; add target `test-agent-certs` (privileged container test, `//go:build agentcerts_e2e`)
- [ ] T003 [P] Add a `fuzz` target (with `FuzzInventoryAgentConfig`) and a coverage check for `internal/providers/inventoryagent` (100 %) to `go-tangra-deployer-v4/Makefile`
- [ ] T004 Add a temporary `replace github.com/go-tangra/go-tangra-inventory/sdk/v4 => ../go-tangra-inventory-v4/sdk` to `go-tangra-deployer-v4/go.mod` for development (removed in T098); `GOWORK=off go build ./...`
- [ ] T005 [P] Package skeletons with doc comments stating their security role: `internal/certmaterial/doc.go`, `internal/certdelivery/doc.go`, `internal/agentcerts/doc.go`, `internal/lcmclient/doc.go`, `go-tangra-deployer-v4/internal/providers/inventoryagent/doc.go`, `go-tangra-deployer-v4/internal/inventoryclient/doc.go`
- [ ] T006 [P] Test fixtures generator `internal/certmaterial/testdata_test.go`: RSA-2048, ECDSA P-256 and Ed25519 leaf + key pairs, a 2-level chain, an expired leaf, a not-yet-valid leaf, a mismatched key, oversized chains, PKCS#1/PKCS#8/SEC1 key encodings; also exported as files under `internal/certmaterial/testdata/` for agent and deployer tests (deployer copies the vectors into `go-tangra-deployer-v4/internal/providers/inventoryagent/testdata/`)

---

## Phase 2: Foundational (blocking prerequisites)

**Purpose**: proto/SDK contract, shared material rules, inventory storage
and config, registry command, lcm client, audit vocabulary, deployer core
hooks. **No user story work starts before this phase is complete.**

### Tests first — inventory

- [ ] T007 [P] Proto contract check: `buf breaking --against '.git#tag=sdk/v4.3.0,subdir=sdk'` in `Makefile` (`proto-check`) and `.github/workflows/ci.yaml`; must fail until T016 is additive-clean
- [ ] T008 [P] `internal/certmaterial/name_test.go` (100 %): `ValidName` accepts `www`, `a`, `web-1.example_com`, 64 chars; rejects ``, `.`, `..`, `.hidden`, `a/b`, `a\b`, `../x`, `a..b`, 65 chars, NUL/control/unicode; `DefaultName` maps `*.example.com` → `wildcard.example.com`, `Www.Example.COM` → `www.example.com`, invalid characters → `_`, empty/unfixable CN → `ok=false`; `ValidTag` accepts `role`, `role=web`, `env=prod eu`; rejects `=x`, control characters, > 63-char key, > 255-char value; `FuzzValidName`, `FuzzHostTag` in `internal/certmaterial/name_fuzz_test.go` (accepted names never contain a separator or `..`, `filepath.Base(name) == name`)
- [ ] T009 [P] `internal/certmaterial/bundle_test.go` (100 %): every T006 fixture — valid bundles parse (leaf first, chain ≤ 10, fingerprint = sha256 of leaf DER lowercase hex, SANs split into DNS/IP, fullchain = leaf + chain with single newlines); **negative**: key ↔ leaf mismatch, expired/not-yet-valid (with injectable clock), non-PEM, PEM with trailing garbage, `CERTIFICATE` block in key position, encrypted key, cert > 64 KiB, chain > 256 KiB or > 10 certs, key > 16 KiB, total > 512 KiB, empty key when `RequireKey`; `FuzzParseBundle` in `internal/certmaterial/bundle_fuzz_test.go` (no panic, accepted bundles satisfy all invariants)
- [ ] T010 [P] Migration test `internal/repo/repodb/certdelivery_integration_test.go` (`//go:build integration`): database at 0009 with hosts/agents → 0010 applies; CHECKs reject bad `name` (incl. `..`), `trigger`, `key_policy`, `state`, `reason`, `fingerprint_sha256`, `detail` > 256 bytes, > 1000 host ids; unique `(tenant, source, idempotency_key)`; partial unique index allows one active item per (tenant, host, name) and many terminal ones; RLS isolates all three tables per tenant and admits `app.system`; **no column of any table has a name containing `key_pem`, `private`, `pem` or type `bytea`** (schema assertion)
- [ ] T011 [P] Config tests `internal/config/certdelivery_test.go`: service `cert_delivery` defaults (disabled, sources `[deployer]`, ttl 168, report timeout 15, fetches 50, lcm 10 s) and bounds; `allow_plaintext_ingest` with `env=production` → error, otherwise a startup warning; agent `certificates` defaults and every rule of contracts/agent-config.md §1 (directory under `/home`/`/tmp`/`/proc` refused, relative refused, modes out of range refused, `key_mode` only 0600/0640, `keep_previous` 0–5, hook relative path refused, timeout bounds, unknown key refused); `FuzzAgentCertsConfig` in `internal/config/certdelivery_fuzz_test.go`
- [ ] T012 [P] Registry tests `internal/registry/registry_test.go`: `Command{Type: certificate, Certificate: {ItemID, Name}}` round-trips through the Memory and Valkey JSON encoding; the JSON has exactly the keys `id,type,certificate.item_id,certificate.name` (guard against material fields)
- [ ] T013 [P] lcm client tests `internal/lcmclient/lcmclient_test.go` with a fake `lcmv1.CertificatesClient`: `include_key` passed through; NotFound → `ErrNotFound`; lcm `InvalidArgument` "no stored private key" → `ErrNoKey`; status REVOKED/EXPIRED surfaced; deadline applied; errors never contain PEM
- [ ] T014 [P] Audit tests `internal/audit/audit_test.go`: every type of contracts/audit-events.md known; subject kind `cert_delivery`; `audit.Validate` rejects a detail value containing `-----BEGIN` or longer than the existing cap
- [ ] T015 [P] Repo contract tests in `internal/memstore/certdelivery_test.go` (mirrored by the integration suite): insert delivery+items atomically with audit rows; supersede active item for host+name in the same tx; list active items per agent (oldest first, limit); conditional transition `UpdateCertItem(id, fn)` under row lock; upsert host certificate; list host certificates; paged item list with 032 `listquery` (filters/sort per contracts/inventory-http.md); cancel active items by host / agent / certificate id

### Implementation — inventory

- [ ] T016 Proto per contracts/inventory-grpc.md §1–§2 in `sdk/api/proto/inventory/v1/inventory.proto` (`CertificateDeliveryService` + messages, `DeliveryState`, `COMMAND_TYPE_CERTIFICATE = 3`, `Command.certificate = 4`, `CertificateCommand`, `FetchCertificate`, `ReportCertificate`, `CertificateBundle`); `make generate`
- [ ] T017 [P] `internal/certmaterial/{name.go,bundle.go}` per data-model §1.2 (stdlib only)
- [ ] T018 [P] Domain structs, states, reasons, bounds, `CapCertV1` in `internal/store/models.go` per data-model §1.1
- [ ] T019 [P] Migration `internal/store/migrations/0010_cert_delivery.sql` per data-model §1.3
- [ ] T020 Repo contract in `internal/repo/repo.go`; `internal/repo/repodb/certdelivery.go`; `internal/memstore/certdelivery.go`; purge of terminal items older than `retention.days` in the existing purge job (`internal/repo/repodb/db.go`)
- [ ] T021 [P] `CertDelivery` service config and `AgentConfig.Certificates` in `internal/config/config.go` (validation, warnings list)
- [ ] T022 [P] `CommandCertificate` + `CertificatePayload{ItemID, Name}` in `internal/registry/registry.go`
- [ ] T023 [P] `internal/lcmclient/lcmclient.go` (Download wrapper, error mapping)
- [ ] T024 [P] Audit vocabulary, subject kind and PEM guard in `internal/audit/audit.go`; realtime event `inventory.certificate.delivery` in `internal/events/events.go`

### Tests first — deployer core

- [ ] T025 [P] `go-tangra-deployer-v4/internal/provider/provider_test.go`: `Field` JSON includes `type/help/options/default/min/max` only when set (existing providers' JSON unchanged — golden); `Capabilities.DeliversByReference` serialised; `WithJob`/`JobFrom` round-trip, absent → `ok=false`
- [ ] T026 [P] `go-tangra-deployer-v4/internal/jobs/scheduler_errors_test.go` + `us1_test.go`: a fake provider with `DeliversByReference` gets `FetchCertificate(includeKey=false)` and `PrivateKeyPEM == ""`; other providers still `true`; `JobFrom(ctx)` inside Deploy/Verify carries tenant, job id, configuration id, parent target id, trigger; Verify of a reference provider fetches without key
- [ ] T027 [P] `go-tangra-deployer-v4/internal/configs/configs_test.go`: a provider implementing `ConfigValidator` rejects bad config on Create/Update (422 field); providers without it unchanged; `Validate` returns provider preview details when the provider implements `Previewer`

### Implementation — deployer core

- [ ] T028 `go-tangra-deployer-v4/internal/provider/provider.go`: `Field` additions, `DeliversByReference`, `JobMeta`/`WithJob`/`JobFrom`, `ConfigValidator`, `Previewer`
- [ ] T029 `go-tangra-deployer-v4/internal/jobs/scheduler.go` and `go-tangra-deployer-v4/internal/jobs/jobs.go`: `includeKey = !caps.DeliversByReference` (rollback unchanged for others), `provider.WithJob` around Deploy/Verify/Rollback
- [ ] T030 `go-tangra-deployer-v4/internal/configs/configs.go` (ValidateConfig on create/update, preview details on validate), `go-tangra-deployer-v4/internal/targets/targets.go` (validate merged overrides), `go-tangra-deployer-v4/internal/httpapi/handlers.go` + `go-tangra-deployer-v4/api/openapi/deployer.yaml` (validate response `details`, capability field schema)

**Checkpoint**: contracts compile, storage and rules exist, deployer core
can host a reference provider. Nothing is delivered yet.

---

## Phase 3: User Story 1 — Deploy a certificate to selected hosts (Priority: P1) 🎯 MVP

**Goal**: manual deployment from the deployer installs cert/chain/fullchain/key on online selected hosts atomically; idempotent; Verify works; key never persisted.

**Independent Test**: quickstart manual 1–3 + 9–10; integration end-to-end with key scan (SC-003).

### Tests for User Story 1 (MANDATORY) ⚠️

- [ ] T031 [P] [US1] Relay service tests `internal/certdelivery/create_test.go` (100 %): validation of every request field; selection = ids ∪ all-tag matches, deduplicated, retired hosts excluded from tag matches, explicit retired host → `unsupported/host_retired`; host without agent → `unsupported/no_agent`; agent known without `cert.v1` and online → `unsupported/no_capability`; Windows agent → `unsupported/platform`; > 1000 → `too_many_hosts`; foreign/unknown ids → `unknown_host_ids` only; lcm metadata NotFound/revoked/expired → FailedPrecondition; idempotent replay returns the same delivery (`created=false`); online agents get one `registry.Deliver` with ids only; audit rows in the same tx; `cert_delivery.enabled=false` → FailedPrecondition
- [ ] T032 [P] [US1] Fetch/report tests `internal/certdelivery/fetch_test.go` (100 %): own active item → bundle from fake lcm, item `fetched`, `fetches+1`, audit `cert_delivery_fetched` without material; **negative**: another agent's item, another tenant's item, terminal item, `fetches == 5` → NotFound + `cert_delivery_refused`; `key_policy=require` + lcm no key → `failed/key_unavailable`; `certificate_only` → `include_key=false`, `has_key=false`; lcm unavailable → Unavailable, item unchanged; invalid/oversized lcm material → `failed/invalid_bundle|bundle_too_large`; report `installed` with a fingerprint ≠ served → `failed/fingerprint_mismatch`; reports for terminal items → `accepted=false`; every terminal report upserts `inventory_host_certificates`; `is_renewal` true iff a different fingerprint was installed under the name
- [ ] T033 [P] [US1] Verify/preview tests `internal/certdelivery/verify_test.go` (100 %): match/mismatch/pending/failed/missing/revoked per contracts/inventory-grpc.md §1; preview capabilities per data-model §1.5
- [ ] T034 [P] [US1] Mesh handler tests `internal/grpcapi/certdelivery_test.go`: SPIFFE peer service not in `cert_delivery.sources` → PermissionDenied + audit; missing/invalid tenant → InvalidArgument; mapping of every message; no handler path returns PEM; the HTTP router (`internal/httpapi`) exposes no route to the service
- [ ] T035 [P] [US1] Ingest tests `internal/ingest/certificates_test.go`: `FetchCertificate`/`ReportCertificate` require the agent credential (interceptor, no credential → Unauthenticated); plaintext ingest without `allow_plaintext_ingest` → FailedPrecondition; per-agent and global fetch caps → ResourceExhausted; `commandToPB` maps `certificate` commands (ids only); **gRPC logging/recovery interceptors never log request/response payloads of `FetchCertificate`** (captured logger contains no PEM); `FuzzReportCertificate` in `internal/ingest/certificates_fuzz_test.go`
- [ ] T036 [P] [US1] Agent store tests `internal/agentcerts/store_test.go` (100 %, fake FS + clock): install creates `archive/<name>/<gen>` with 4 files, modes/owner from config, `live/<name>` symlink swapped via temp+rename, metadata with v3 fields + ids, `previous_serial`/`renewal_count`; `certificate_only` carries over a matching key, `key_mismatch` on a non-matching one (nothing changed), installs without key when none exists; **negative**: invalid name, `live` or `archive` is a symlink or not root-owned → `write_failed/unsafe_directory`; disk-full simulation → `disk_full`, previous `live/<name>` intact; failure after staging leaves `live/<name>` untouched and removes the staged generation; crash recovery removes unreferenced newer generations and `.tmp` entries; pruning keeps `keep_previous` generations and never the live target
- [ ] T037 [P] [US1] Idempotency tests `internal/agentcerts/idempotent_test.go` (100 %): same fingerprint + intact files → `unchanged`, zero writes (fake FS write counter); tampered `cert.pem` or missing `privkey.pem` → reinstall; owner/mode drift → fixed in place, `unchanged`
- [ ] T038 [P] [US1] Daemon tests `internal/daemon/certificates_test.go`: `cert.v1` announced iff enabled + linux + (TLS or `allow_insecure_transport`); CERTIFICATE command → fetch → install → report; duplicate item ids while one is running are ignored; `enabled=false` → `failed/disabled_locally` without fetch; fetch `NotFound` → nothing written, no report; fetch `Unavailable` → retry with backoff up to the stream's lifetime
- [ ] T039 [P] [US1] Deployer provider tests `go-tangra-deployer-v4/internal/providers/inventoryagent/inventoryagent_test.go` (100 %, fake inventory client): config parse/validate per contracts/deployer-provider.md §2 incl. negatives (no selector, bad uuid, 17 tags, `../x`, unknown key, wait 241); default name from CN uses the shared test vectors; Deploy sends idempotency key = job id and `rearm_failed=true`; every row of research D9 with and without `require_all_success`; 0 items → failure "no hosts matched"; wait clamped to deadline − 10 s; progress monotonic; details capped at 200 hosts; Verify success/failure matrix; Rollback → `ErrUnsupported`; `FuzzInventoryAgentConfig` in `go-tangra-deployer-v4/internal/providers/inventoryagent/config_fuzz_test.go`
- [ ] T040 [P] [US1] Policy tests: extend `internal/app/policy_test.go` — `svc/deployer` allowed exactly the five `CertificateDeliveryService` methods + health, nothing else on inventory; new `go-tangra-lcm-v4/internal/app/policy_test.go` (pattern of the inventory test) — `svc/inventory` allowed exactly `/lcm.v1.Certificates/Download`
- [ ] T041 [US1] Integration end-to-end `internal/app/certdelivery_integration_test.go` (`//go:build integration`, testcontainers): real repo + Valkey registry + ingest gRPC (bufconn, real agent credential) + fake lcm; mesh Create → agent stream receives CERTIFICATE → Fetch → Report installed → Get shows installed → Verify match; **key scan** of `pg_dump`, captured service logs, `inventory_audit_events`, Valkey keys and `platform:events:<tenant>` for `PRIVATE KEY` and the test key's DER (SC-003)

### Implementation for User Story 1

- [ ] T042 [US1] `internal/certdelivery/service.go` (Create, resolution, capability → unsupported, push), `fetch.go` (AuthorizeFetch/Fetch with lcm, bundle validation, key zeroing), `report.go` (transitions, host certificate upsert), `verify.go` (Verify, Preview)
- [ ] T043 [US1] Mesh service `internal/grpcapi/certdelivery.go` + registration in `internal/grpcapi/servers.go` (source allow-list from the SPIFFE peer, like `hostreports.go`)
- [ ] T044 [US1] Ingest `internal/ingest/certificates.go` (`FetchCertificate`, `ReportCertificate`, caps, plaintext guard), `commandToPB` certificate mapping in `internal/ingest/upgrade.go`, `WithCertDelivery` wiring in `internal/ingest/ingest.go`
- [ ] T045 [US1] Wiring in `internal/app/app.go`: lcm `CertificatesClient` over the mesh (`cert_delivery.lcm_service`), service, ingest + gRPC registration only when `cert_delivery.enabled`; metrics (deliveries by state, fetches, lcm latency, refusals)
- [ ] T046 [P] [US1] Agent store `internal/agentcerts/{store.go,layout.go,idempotent.go,metadata.go,recover.go}` (pure core over an `FS` interface) and `internal/agentcerts/fs_linux.go` (O_NOFOLLOW opens, fchown, fsync, symlink+rename), `fs_other.go` (unsupported)
- [ ] T047 [US1] Daemon `internal/daemon/daemon.go`: `cert.v1` in `streamRequest`, CERTIFICATE handling (serialised worker, dedup by item id, fetch/install/report via the sender), `internal/sender` Fetch/Report calls; wiring in `cmd/inventory-agent/main.go` (Linux only)
- [ ] T048 [P] [US1] `packaging/agent.yaml`: commented `certificates` section with defaults and the security note; `packaging/packaging_test.go` asserts the unit has no `ProtectSystem` without `ReadWritePaths=/etc/inventory-agent/certs`
- [ ] T049 [P] [US1] SDK helper `sdk/pkg/inventoryclient/certdelivery.go` per contracts/inventory-grpc.md §3 with tests against the in-process fake server in `sdk/pkg/inventoryclient/certdelivery_test.go`
- [ ] T050 [P] [US1] Policies: rule `deployer-cert-delivery` in `deploy/policy.yaml`; rule `inventory-download` in `go-tangra-lcm-v4/deploy/policy.yaml`; prepared diffs for `go-tangra-docker/policies/{inventory.yaml,lcm.yaml}` and `go-tangra-docker/configs/{inventory.yaml,deployer.yaml}` in `deploy/README.md` (user applies)
- [ ] T051 [US1] Deployer adapter `go-tangra-deployer-v4/internal/inventoryclient/inventoryclient.go` (SDK over the Freya mesh conn) and provider `go-tangra-deployer-v4/internal/providers/inventoryagent/{inventoryagent.go,config.go,evaluate.go,name.go}`
- [ ] T052 [US1] Deployer config `inventory.service` in `go-tangra-deployer-v4/internal/config/config.go`; registration during wiring in `go-tangra-deployer-v4/internal/app/app.go` (before workers/HTTP start; absent when not configured); `go-tangra-deployer-v4/internal/providers/all/all_test.go` updated (catalogue with/without inventory)
- [ ] T053 [US1] Deployer integration `go-tangra-deployer-v4/internal/app/inventoryagent_integration_test.go` (`//go:build integration`): job with the provider against an in-process inventory `CertificateDeliveryService` fake → completed; job history and audit carry `delivery_id` and counts; no key fetched from the fake lcm (`include_key=false` asserted)

**Checkpoint**: a manual deployment reaches online hosts; Verify works;
quickstart 1–3 and 9–10 pass in freya-stack with locally built images.

---

## Phase 4: User Story 2 — Renewals and offline hosts (Priority: P1)

**Goal**: automatic deployment on issued/renewed reaches all selected hosts, offline ones on reconnect; newest wins; retries re-arm only failed hosts.

**Independent Test**: quickstart manual 4; SC-002.

### Tests for User Story 2 (MANDATORY) ⚠️

- [ ] T054 [P] [US2] `internal/certdelivery/replay_test.go` (100 %): `OnConnect` returns commands for the agent's active items oldest first, ≤ 50, marks pending → delivered (audit once); agent without `cert.v1` on connect → its pending items `unsupported/no_capability`; delivered/fetched items are re-sent (agent dedups)
- [ ] T055 [P] [US2] `internal/certdelivery/supersede_test.go` (100 %): new item for the same host+name supersedes the active one in the same tx (`superseded_by` in audit); concurrent creates for the same host+name → exactly one active (integration variant in `internal/repo/repodb/certdelivery_integration_test.go`); non-manual trigger with a later-expiring installed certificate → `superseded/older_than_installed`; manual trigger installs it (audited)
- [ ] T056 [P] [US2] `internal/certdelivery/sweep_test.go` (100 %): pending/delivered past `expires_at` → `expired`; `expires_at = min(created + ttl, not_after)`; fetched without report after `report_timeout` → `failed/no_report`; sweeper idempotent across two replicas (row locks)
- [ ] T057 [P] [US2] `internal/certdelivery/rearm_test.go` (100 %): replay with `rearm_failed` re-arms `failed|hook_failed|expired` (attempts+1, `rerun_hook` only from hook_failed, ≤ 5 then left failed), leaves `installed|unchanged|pending|…` untouched, pushes to online agents
- [ ] T058 [P] [US2] Host/agent removal tests in `internal/repo/repodb/certdelivery_integration_test.go` and `internal/memstore/certdelivery_test.go`: `DeleteHost` cancels active items (`host_deleted`) and deletes host certificates; agent revocation cancels its active items (`agent_revoked`), both with audit in the same tx
- [ ] T059 [P] [US2] Ingest test `internal/ingest/ingest_test.go`: `StreamCommands` sends upgrade replay then certificate replay, then live commands
- [ ] T060 [P] [US2] Agent restart test `internal/daemon/certificates_test.go`: an item fetched before a crash is re-fetched after reconnect (fetch ≤ 5) and completes; a renewed bundle for the same name yields a new generation and `previous_serial`
- [ ] T061 [P] [US2] Deployer auto-deploy test `go-tangra-deployer-v4/internal/jobs/us3_test.go` (or new `inventoryagent_auto_test.go`): `certificate.renewed` with a matching filter creates a job whose provider call carries the **new** certificate id, trigger `auto_deploy`; job retry calls Create with the same idempotency key; queued hosts → job completed with "queued for N"

### Implementation for User Story 2

- [ ] T062 [US2] `internal/certdelivery/replay.go` (`OnConnect`), `supersede.go`, `sweep.go`, `rearm.go`
- [ ] T063 [US2] `StreamCommands` certificate replay in `internal/ingest/ingest.go` (after `pendingCommands`), `CertReplayer` interface wiring
- [ ] T064 [US2] Sweeper registration in the jobs loop (`internal/app/app.go`), config `pending_ttl_hours`/`report_timeout_minutes`
- [ ] T065 [US2] Cancellation on host deletion and agent revocation in `internal/repo/repodb/db.go` and `internal/memstore/memstore.go` (same transaction, audit rows)
- [ ] T066 [US2] Deployer trigger mapping: `JobMeta.Trigger` = `manual|auto_deploy|retry` from the job (`go-tangra-deployer-v4/internal/jobs/scheduler.go`), provider passes it through

**Checkpoint**: renewals reach online and later-reconnecting hosts;
quickstart 4 passes.

---

## Phase 5: User Story 3 — Local deploy hook (Priority: P1)

**Goal**: optional, locally configured hook with v3 environment after new/replaced installs; safe execution; result reported.

**Independent Test**: quickstart manual 5.

### Tests for User Story 3 (MANDATORY) ⚠️

- [ ] T067 [P] [US3] `internal/agentcerts/hook_test.go` (100 %, fake exec + fake stat): no hook configured → never executed; environment exactly the contract table (no inherited variables; values from the agent's own parse; `LCM_KEY_PATH` empty without key; `LCM_IS_RENEWAL` from local metadata); hook not run for `unchanged` unless `rerun_hook`; exit 0 → `installed`; exit 3 → `hook_failed` code 3; timeout → `hook_failed/hook_timeout` code 256; **negative**: symlink, not root-owned, group/other-writable file, writable parent directory, non-executable → `hook_refused`, code −1, files still installed; output > 4 KiB truncated in the local log and never present in the report `detail`
- [ ] T068 [P] [US3] Privileged container test `internal/agentcerts/hook_linux_e2e_test.go` (`//go:build agentcerts_e2e`, `make test-agent-certs`): real root-owned script writes its env; user-owned script refused; sleeping script killed with its child processes at timeout; real owner/group/modes on the store (e.g. `owner: root`, `group: www-data`, `key_mode: 0640`)
- [ ] T069 [P] [US3] Protocol guard test `internal/ingest/certificates_test.go` + `internal/daemon/certificates_test.go`: `CertificateCommand` and `CertificateBundle` contain no path/command/env/owner/mode fields (reflection over the generated message descriptors); daemon ignores unknown fields

### Implementation for User Story 3

- [ ] T070 [US3] `internal/agentcerts/hook.go` (checks, environment builder, result mapping) and `internal/agentcerts/hook_linux.go` (exec without shell, process group, SIGTERM→SIGKILL, stdin /dev/null, bounded output capture)
- [ ] T071 [US3] Install → hook → report sequencing and `rerun_hook` handling in `internal/agentcerts/store.go` and `internal/daemon/daemon.go`; metadata `last_hook_execution`, `hook_exit_code`
- [ ] T072 [P] [US3] Document the hook (variables, checks, sandbox of the systemd unit: no `/home`, private `/tmp`, `NoNewPrivileges` — `systemctl reload` works) in `README.md` and `packaging/agent.yaml` comments

**Checkpoint**: all P1 stories complete — releasable MVP (with JSON
configuration in the deployer).

---

## Phase 6: User Story 4 — Delivered certificates per host in the inventory UI (Priority: P2)

**Goal**: Certificates tab, delivery history, cancel; agent capability column.

**Independent Test**: quickstart manual 7; no PEM in any response.

### Tests for User Story 4 (MANDATORY) ⚠️

- [ ] T073 [P] [US4] HTTP tests `internal/httpapi/certificates_test.go`: routes and permissions (`inventory:read` for reads, `agents:manage` for cancel; missing permission → 403); tenant isolation (404); list paging/sorting/filters per 032 (`422` on bad params); cancel terminal → 409; disabled → 503 for cancel, empty lists for reads; responses contain no `-----BEGIN`; OpenAPI contract test covers the four operations
- [ ] T074 [P] [US4] Agent list test `internal/httpapi/api_test.go`: `certificate_capability` per data-model §1.5 for each case
- [ ] T075 [P] [US4] UI tests `ui/tests/unit/certificates.spec.ts`: Certificates tab renders rows/badges (installed, unchanged, failed+reason, hook exit, revoked), fingerprint shortened with copy, deployer link, history paging, cancel visible only with `manage InventoryAgent`; agents column badges; strings rendered as text

### Implementation for User Story 4

- [ ] T076 [US4] `internal/httpapi/certificates.go` (+ routes in `internal/httpapi/handlers.go`, deps in `internal/httpapi/deps.go`), list specs in `internal/store/lists.go`, `api/openapi/inventory.yaml`, manifest route check `pkg/inventorymanifest/manifest.go`
- [ ] T077 [US4] Agent list capability in `internal/httpapi/handlers.go` (fleet view mapping)
- [ ] T078 [P] [US4] UI: `ui/src/stores/certificates.ts`, `ui/src/api/types.ts` (+ regenerated `ui/src/api/schema.d.ts`), Certificates tab in `ui/src/views/hosts/detail.vue`, capability column in `ui/src/views/agents/index.vue`

---

## Phase 7: User Story 5 — Host picker in the deployer UI (Priority: P2)

**Goal**: dedicated form with inventory host picker, preview, manual fallback; per-host job results.

**Independent Test**: form output equals the JSON contract; preview equals inventory resolution; fallback without inventory read (SC-006).

### Tests for User Story 5 (MANDATORY) ⚠️

- [ ] T079 [P] [US5] `go-tangra-deployer-v4/ui/tests/unit/inventory-agent-config.spec.ts`: form ↔ config JSON round-trip (incl. overrides), zod validation mirrors contracts/deployer-provider.md §2 (bad name, 17 tags, no selector), JSON editor still used for other providers, credentials field hidden
- [ ] T080 [P] [US5] `go-tangra-deployer-v4/ui/tests/unit/host-picker.spec.ts`: server-paged search against a mocked `/api/inventory/v1/hosts`, selection chips, capability/online badges from `/api/inventory/v1/agents`, 401/403/404 → manual-entry notice and textarea; Validate shows `matched_hosts`
- [ ] T081 [P] [US5] `go-tangra-deployer-v4/ui/tests/unit/jobs.spec.ts`: job drawer renders `details.counts` and `details.hosts` for `inventory-agent`

### Implementation for User Story 5

- [ ] T082 [P] [US5] `go-tangra-deployer-v4/ui/src/api/inventory.ts` (gateway calls with the user's session), `go-tangra-deployer-v4/ui/src/components/HostPicker.vue`
- [ ] T083 [US5] `go-tangra-deployer-v4/ui/src/components/InventoryAgentConfig.vue`, integration in `go-tangra-deployer-v4/ui/src/views/configurations/index.vue`, schema in `go-tangra-deployer-v4/ui/src/schemas/configuration.ts`, types in `go-tangra-deployer-v4/ui/src/api/types.ts`
- [ ] T084 [US5] Per-host results in `go-tangra-deployer-v4/ui/src/views/jobs/index.vue`

---

## Phase 8: User Story 6 — Revocation flagged, never auto-removed (Priority: P3)

**Goal**: forwarded `certificate.revoked` cancels queued deliveries and flags hosts; fetch refuses revoked/expired certificates.

**Independent Test**: quickstart manual 8.

### Tests for User Story 6 (MANDATORY) ⚠️

- [ ] T085 [P] [US6] `internal/certdelivery/revoke_test.go` (100 %): `MarkCertificateRevoked` cancels active items of that certificate (`cancelled/certificate_revoked`), sets `revoked_at` on host certificates holding it, idempotent, audit `host_certificate_revoked`; a later install of another certificate under the name clears `revoked_at`; fetch of a revoked/expired certificate → `failed/certificate_revoked|certificate_expired` (covered with T032 cases)
- [ ] T086 [P] [US6] `go-tangra-deployer-v4/internal/events/consumer_test.go`: `certificate.revoked` → `MarkCertificateRevoked` only when the tenant has an active `inventory-agent` configuration; errors logged and audited `certificate_revocation_forwarded/failed`, never block issued/renewed handling; loop guard unchanged; extend the consumer fuzz test

### Implementation for User Story 6

- [ ] T087 [US6] `internal/certdelivery/revoke.go`, mesh handler method in `internal/grpcapi/certdelivery.go`
- [ ] T088 [US6] `go-tangra-deployer-v4/internal/events/consumer.go` (`certificate.revoked`), audit type in `go-tangra-deployer-v4/internal/audit/audit.go`, wiring in `go-tangra-deployer-v4/internal/app/app.go`
- [ ] T089 [P] [US6] Revoked badge and filter in `ui/src/views/hosts/detail.vue` (uses T078 store)

---

## Phase 9: Polish, security review & release

- [ ] T090 [P] Security review checklist `specs/033-agent-cert-delivery/checklists/security-review.md` (STRIDE rows → test ids; key-scan evidence; policy diffs)
- [ ] T091 [P] `govulncheck` (`make vuln`) in inventory and deployer; `buf lint`; `gosec` clean for new packages (G304/G302 file modes justified inline)
- [ ] T092 [P] Coverage gates: `make cover` inventory (100 % new security packages, ≥ 80 % total) and deployer (100 % `inventoryagent`, ≥ 80 % total)
- [ ] T093 [P] Docs: `README.md` (feature overview, agent config, layout, hook), `deploy/README.md` (service config, policies, stack diffs), `SECURITY.md` (key handling statement), `go-tangra-deployer-v4/README.md` (provider)
- [ ] T094 Local freya-stack validation with locally built images (quickstart manual 1–10); record results in `specs/033-agent-cert-delivery/checklists/security-review.md`
- [ ] T095 Run `/speckit-analyze` consistency check across spec/plan/tasks; fix drift
- [ ] T096 Release inventory SDK: PR merge, tag `sdk/v4.4.0` (**user confirmation**)
- [ ] T097 Release inventory v4.7.0: PR merge, tag; approve the `release` environment signing job (**user approval in GitHub**); verify the GitHub release carries the signed agent artifacts (**user confirmation**)
- [ ] T098 Deployer: replace the temporary `replace` with `github.com/go-tangra/go-tangra-inventory/sdk/v4 v4.4.0` in `go-tangra-deployer-v4/go.mod`; `GOWORK=off go build ./... && go test ./...`
- [ ] T099 Release deployer v4.4.0 and push lcm policy change (`go-tangra-lcm-v4` PR, no release needed) (**user confirmation**)
- [ ] T100 Production (**user confirmation**, backups first): pin inventory 4.7.0 and deployer 4.4.0 in go-tangra-docker, apply policies/configs from contracts/mesh-policy.md, deploy, migration 0010, "Upgrade all" agents, quickstart manual 1–3 on one production host before enabling auto-deploy targets

---

## Dependencies & Execution Order

### Phase dependencies

- Phase 1 → Phase 2 → user stories.
- US1 (Phase 3) is the MVP and a prerequisite of every other story
  (relay, ingest, agent store, provider).
- US2 (Phase 4) and US3 (Phase 5) depend on US1 only and can run in
  parallel (different packages: `certdelivery` replay/sweep vs.
  `agentcerts` hook).
- US4 (Phase 6) depends on US1 (data) — parallel to US2/US3.
- US5 (Phase 7) depends on US1 (provider, preview) — parallel to US4.
- US6 (Phase 8) depends on US1 (and the T078 store for its UI task).
- Phase 9 after the stories chosen for the release (P1 minimum).

### Within each story

Tests → pure packages → service/handlers → wiring → UI.

### Cross-repo order

inventory proto + SDK (T016, T049) → deployer provider (T051+) using the
temporary `replace` (T004) → SDK tag (T096) → `go.mod` pin (T098).

## Parallel Example: Phase 2

```bash
Task: "certmaterial name tests + fuzz in internal/certmaterial/name_test.go"
Task: "certmaterial bundle tests + fuzz in internal/certmaterial/bundle_test.go"
Task: "Migration test in internal/repo/repodb/certdelivery_integration_test.go"
Task: "Config tests in internal/config/certdelivery_test.go"
Task: "Deployer provider package tests in go-tangra-deployer-v4/internal/provider/provider_test.go"
```

## Parallel Example: User Story 1

```bash
Task: "Relay create tests in internal/certdelivery/create_test.go"
Task: "Agent store tests in internal/agentcerts/store_test.go"
Task: "Provider tests in go-tangra-deployer-v4/internal/providers/inventoryagent/inventoryagent_test.go"
Task: "Policy tests in internal/app/policy_test.go and go-tangra-lcm-v4/internal/app/policy_test.go"
```

---

## Implementation Strategy

### MVP First

1. Phase 1 + Phase 2.
2. **US1** — manual deployment to online hosts with JSON configuration.
3. **STOP and VALIDATE**: quickstart manual 1–3, 9–10; key scan green.

### Incremental Delivery

1. US2 (renewals/offline) and US3 (hook) → release inventory 4.7.0 +
   deployer 4.4.0 (P1 complete; parity with v3 plus offline delivery).
2. US4 + US5 (UIs) — can ship in the same releases or a minor later.
3. US6 (revocation flag) — minor release.

### Parallel Team Strategy

- Developer A: inventory relay (`certdelivery`, mesh, ingest, storage).
- Developer B: agent (`agentcerts`, hook, daemon, packaging).
- Developer C: deployer (core changes, provider, consumer, UI).
- Developer D: inventory UI (after US1 data exists).

---

## Notes

- [P] tasks touch different files and have no dependency on an unfinished task.
- The private key must never be written by the inventory or the deployer —
  T010 (schema), T012 (registry), T035 (log capture), T041 (key scan) and
  T053 (no key fetched) guard it; keep them green.
- The server never chooses directories, owners, modes or hooks; T069 guards
  the protocol surface.
- Every delivery transition must appear in `inventory_audit_events` in the
  same transaction; tests assert audit rows next to data rows.
- Commit after each task or logical group; stop at any checkpoint to
  validate the story on its own.
