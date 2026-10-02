# 033 Security review (T110)

Date: 2026-10-02. Scope: inventory branch `033-agent-cert-delivery`
(service relay `internal/certdelivery`, material rules
`internal/certmaterial`, agent store `internal/agentcerts`, agent daemon,
ingest, HTTP/gRPC surfaces, UI), deployer branch `033-agent-cert-delivery`
(`inventory-agent` provider, descriptor validator, configuration drawer
API, target overrides, BIG-IP/FortiGate profile options) and the lcm
policy rule.

**Result: all STRIDE rows mitigated and covered by tests. The review
findings were fixed in inventory 4a20c05 and deployer db2cb00 (listed
below); no open CRITICAL/HIGH/MEDIUM. Accepted residual risks are listed
at the end.**

## STRIDE rows → tests

Inventory tests are in `go-tangra-inventory-v4`, deployer tests (prefixed
`deployer:`) in `go-tangra-deployer-v4`. Packages are named by their last
path element.

- [x] **S — stolen agent credential fetches other hosts' keys.**
  Fetch only for the caller's own active item, else NotFound + audited
  refusal; revocation cancels items; a host claimed by two non-revoked
  agents gets nothing (`ambiguous_agent`).
  certdelivery `TestFetchOwnItem`, `TestFetchRefusals`,
  `TestFetchAfterDeliveryWindow`, `TestCreateAmbiguousAgent`,
  `TestPreview`; ingest `TestRevokedAgentRefused`,
  `TestWrongCredentialRefused`, `TestFetchAndReportCertificate`;
  app `TestCertificateDeliveryEndToEnd` (integration).
- [x] **S — another mesh service creates deliveries.** Method-specific
  policy for `svc/deployer`; handler re-checks `cert_delivery.sources`;
  no gateway route to the service.
  `TestCertDeliverySourceCheck`, `TestPolicyDeployerCertDelivery`,
  `TestPolicyIPAMHostSync`, `TestCertDeliveryNotGatewayExposed`.
- [x] **S — rogue server pushes certificates.** Commands only on the
  agent's TLS ingest connection; plaintext refused on both sides unless
  the named dev opt-outs are set.
  `TestAgentCertificatesTransport`, `TestCertDeliveryPlaintextIngest`,
  `TestCertificateProtocolGuard`, `TestCertificateEdgeGuards`.
- [x] **T — name with `../` writes outside the directory.** Same name
  rule at deployer, inventory and agent; confined directory opens,
  foreign symlinks replaced, unsafe parents refused.
  certmaterial `TestValidName`, `FuzzValidName`; agentcerts
  `TestInstallRejectsInvalidInput`, `TestOSFSConfinement`,
  `TestUnsafeDirectories`, `TestForeignLiveLinkIsReplaced`;
  deployer: inventoryagent `TestNameRulesMatchInventoryVectors`,
  `FuzzInventoryAgentConfig`.
- [x] **T — server-driven code execution.** No server field reaches
  exec: hook path/owner/modes only from local config; certificate id
  validated before it reaches `LCM_CERTIFICATE_ID`; hook file and every
  ancestor directory root-owned and not group/other writable; relative
  hook path never runs.
  `TestCertificateMessagesCarryNoServerControl`; agentcerts
  `TestHookRefused` (incl. ancestor writable / not root-owned / relative
  path), `TestNoHookConfiguredNeverRuns`, `TestHookEnvironment`,
  `TestHookEnvCleansControlCharacters`, `TestE2EUserOwnedHookRefused`
  (incl. root-owned hook under world-writable `/tmp`); certmaterial
  `TestValidCertificateID`.
- [x] **T — certificate/key mismatch installed.** Key ↔ certificate check
  in inventory and on the agent.
  certmaterial `TestParseBundleNegative`, `FuzzParseBundle`; certdelivery
  `TestFetchInvalidMaterial`; agentcerts `TestInstallRejectsInvalidInput`
  (key mismatch).
- [x] **T — replay / downgrade to an older certificate.** Supersession per
  (host, name); expired/revoked refused at fetch; idempotency key reuse
  with another payload refused; re-armed items carry a new attempt.
  certdelivery `TestSupersede`, `TestCertDeliveryConcurrentSupersede`,
  `TestCreateIdempotentReplay` (key reuse), `TestCreateLCMRefusals`,
  `TestMarkRevoked`; `TestCertDeliveryRepoContract` (repodb, integration:
  revocation never cleared by a stale report);
  `TestCertificateRearmedItemRunsAgain`.
- [x] **R — who delivered what where.** Audit rows for request, fetch,
  result, supersession, cancellation, refusal (throttled, suppressed count
  carried, non-uuid subjects dropped).
  `TestSafeRowDropsPEMAndGuards`; certdelivery `TestFetchRefusals`,
  `TestCancelAndList`, `TestRearm`; deployer: jobs
  `TestJobMetaForTargetAndRetry`.
- [x] **I — key leaks through DB, Valkey, logs, audit, events.** Key only
  in memory during one fetch; registry commands carry ids (+ attempt)
  only; gRPC payload logging off for ingest.
  certdelivery `TestNoMaterialOutsideTheFetch`;
  `TestFetchCertificateOverGRPCNeverLogged`, `TestCertDeliveryPEMGuard`,
  `TestCommandToPBCertificate`; agentcerts `TestHookOutputOnlyInLocalLog`;
  app `TestCertificateDeliveryEndToEnd` (pg_dump, Valkey, events, audit,
  logs scanned — see key-scan evidence).
- [x] **I — cross-tenant host selection.** Selection resolved inside the
  request tenant; foreign ids → `unknown_host`.
  certdelivery `TestCreateSelectionAndCapabilities`;
  `TestMemoryListConnectedTenantIsolation`.
- [x] **I — key readable by other local users.** `key_mode` 0600 root,
  dir 0750, staging 0700, atomic generation switch.
  agentcerts `TestOSStoreInstall`, `TestOSStoreSwapIsAtomic`,
  `TestE2EOwnershipAndHookEnvironment`, `TestOwnerResolution`;
  packaging `TestUnitKeepsCertificateDirectoryWritable`.
- [x] **D — mass deployments flood agents/lcm.** Selection, queue, fetch
  and replay bounds; lcm timeout.
  certdelivery `TestCreateTooManyHostsAndEmpty`;
  `TestCertificateFetchCaps`, `TestOnConnectReplayLimit`,
  `TestCertificateQueueFull`, `TestCertStateMemoryBounded`;
  lcmclient `TestDownloadErrors`; tests/loadgen.
- [x] **D — hook hangs.** Timeout + process-group kill, bounded output.
  agentcerts `TestExecRunnerTimeoutKillsGroup`,
  `TestExecRunnerBoundsOutput`, `TestE2EHookTimeoutKillsChildren`.
- [x] **E — hook replaced by an unprivileged user.** Ownership/permission
  checks of the file and all ancestors before each run (see T row).
  agentcerts `TestHookRefused`, `TestE2EUserOwnedHookRefused`.
- [x] **I (US5) — secret echoed by drawer or API.** Write-only secrets,
  descriptor-built errors.
  deployer: configs `TestCredentialRedaction`,
  `TestViewCredentialProjection`; httpapi
  `TestConfigurationsCRUDAndRedaction`, `TestValidateCredentialsRejected`;
  provider `FuzzValidateInput`; awsacm
  `TestDeployFailureDoesNotLeakSecret`; fortigate
  `TestDeployFailureDoesNotLeakToken`.
- [x] **I (US5) — stored config redirects sealed credentials.** Undeclared
  keys refused at save and ignored at deploy; auth headers refused;
  destination change (url or credential host) on update **or validate**
  needs every stored secret re-entered/cleared; no redirect following on
  credential-bearing clients.
  deployer: all `TestStoredConfigCannotRedirectCredentials`,
  `TestRedirectSettingsStayRefused`; configs
  `TestDestinationChangeNeedsStoredSecrets`; httpapi
  `TestRedirectKeysRefusedOnSave`, `TestWebhookCredentialHeadersRefused`,
  `TestWebhookDestinationChangeNeedsCredentials`; provider
  `TestAuthHeaders`, `TestCredentialDestinationChange`, `TestNoRedirect`;
  webhook `TestRedirectNotFollowed`; awsacm
  `TestStoredEndpointCannotRedirect`; cloudflare
  `TestStoredAPIBaseCannotRedirect`.
- [x] **T (US5) — client-side validation bypassed.** Same descriptors
  server-side on create/update/validate/override.
  deployer: configs `TestCreateRequiredFieldsPerProvider`,
  `TestCreateUnknownKeyAndProviderValidator`; provider
  `TestValidatorVectors`.
- [x] **T/I (US5, Q7) — target override redirects or smuggles secrets.**
  Only declared overridable fields at attach; stored overrides filtered
  again at job start (url, TLS switch, headers, credentials dropped).
  deployer: targets `TestAttachOverrideRules`,
  `TestAttachRejectsCredentialOverrides`; httpapi
  `TestOverrideCannotMoveDestination`; provider `TestFilterOverride`,
  `TestCheckCapabilitiesRejects`; jobs `TestLegacyOverrideRedirectKeysIgnored`,
  `TestLegacyOverrideDestinationIgnoredWithCredentials`; all
  `TestOverridableSet`.
- [x] **D (US8) — deployment breaks an appliance profile.** Pre-checks
  before the first write, in-place family update, no delete/recreate;
  FortiGate reuses only the identical certificate; profile names cannot
  start with a dot.
  deployer: bigip `TestDeployExistingProfileMissing`,
  `TestRollbackExistingProfile`, `TestSSLProfileDescriptorRefusesBadPatterns`;
  fortigate `TestProfileDeployManualReview`, `TestProfileRollback`,
  `TestProfileDeploySameSerialOtherCertificate`,
  `TestDefaultProfileDescriptor`.
- [x] **E — deployer user gains host access.** Only a file in the
  dedicated directory; execution needs a local hook.
  `TestCertificateMessagesCarryNoServerControl`; agentcerts
  `TestNoHookConfiguredNeverRuns`.

## Key-scan evidence (SC-003)

- [x] Unit: `TestNoMaterialOutsideTheFetch` scans audit rows, events,
  registry commands and captured logs for `PRIVATE KEY` and the raw key
  bytes after create/fetch/report — pass.
- [x] Integration (`make test-integration`, testcontainers PostgreSQL +
  Valkey): `TestCertificateDeliveryEndToEnd` scans `pg_dump`, every Valkey
  key, published events, audit rows and service logs — pass.
- [x] Privileged agent store (`make test-agent-certs`): ownership, modes,
  hook environment, timeout kill — pass.
- [x] gRPC: `TestFetchCertificateOverGRPCNeverLogged` (interceptor logs
  contain neither PEM nor key).

## Mesh policy diffs

- [x] inventory `deploy/policy.yaml`: new rule `deployer-cert-delivery`
  (`svc/deployer` → the five `CertificateDeliveryService` methods + health
  check); covered by `TestPolicyDeployerCertDelivery`. No other rule
  changed.
- [x] lcm `deploy/policy.yaml` (branch `033-agent-cert-delivery`, e1600cd):
  new rule `inventory-download` (`svc/inventory` →
  `/lcm.v1.Certificates/Download` only). Not yet pushed (T119).
- [x] deployer: no inbound policy change.
- [ ] Production copies in `go-tangra-docker/policies/` — applied by the
  user at T120.

## Static analysis (T111)

- inventory: `go vet`, `staticcheck`, `gosec` (`make lint`) clean;
  `govulncheck` service + SDK: no reachable vulnerabilities; `buf lint`
  and `buf breaking` against `sdk/v4.3.0` clean.
- deployer: `go vet` (+ integration tag), `gosec` on the 033 packages,
  `buf lint`, `govulncheck` clean. `staticcheck` reports two findings
  that predate 033 (on `main`): `internal/events/matcher.go:58` SA4006,
  `internal/httpapi/jsonraw.go:7` U1000 — not touched here.
- G402 (`InsecureSkipVerify`) on the BIG-IP/FortiGate management clients
  is pre-033 behaviour, justified inline.

## Findings fixed in this review

| # | Area | Finding | Fix |
|---|---|---|---|
| 1 | agent hook | Only the hook's own directory was checked; a writable ancestor allowed a rename swap of the root hook | every ancestor up to `/` checked; relative paths refused |
| 2 | agent hook env | `LCM_CERTIFICATE_ID` taken unvalidated from the bundle | `ValidCertificateID` at inventory create and agent install |
| 3 | relay | Two non-revoked agents on one host: newest won (impostor could attract the key) | host becomes `ambiguous_agent`, nothing delivered |
| 4 | relay | Idempotency key replay answered for another certificate/name | refused as `invalid idempotency_key` |
| 5 | relay | Expired delivery served until the sweeper ran | refused at fetch |
| 6 | audit | Throttled refusals lost; attacker-chosen subject ids recorded | suppressed count carried; non-uuid subjects dropped; idle entries pruned |
| 7 | agent | Re-armed item deduplicated away by the agent | command carries `attempt` |
| 8 | agent recovery | Without a live link all generations were discarded (possible loss of the only operator key) | generations kept |
| 9 | repodb | Stale report could clear a host certificate revocation | revocation of the same certificate kept |
| 10 | deployer configs | Credential host change or validate against another endpoint sent stored secrets | all stored secrets must be re-entered/cleared |
| 11 | deployer jobs | Legacy overrides only stripped destination keys | full descriptor filter at job start |
| 12 | deployer providers | Redirects followed (POST body with key replayed on 307/308) | `NoRedirect` on all credential-bearing clients |
| 13 | FortiGate | Reuse by serial only | identical DER required; dot-leading profile names refused |
| 14 | deployer events | Revoked-event certificate id unbounded | validated before forwarding |

## Accepted residual risks

- Q8 default kept: `host_ids`/`host_tags` are overridable (v3 parity); a
  holder of `targets:manage` can direct a shared configuration's
  deliveries to other `cert.v1` hosts of the tenant. Every delivery is
  audited.
- BIG-IP/FortiGate management TLS is not verified (pre-033); follow-up:
  CA pin option.
- An agent binds to a host by reported identity; ambiguity is detected
  (finding 3) but a sole impostor on a host without its real agent is
  only caught by the operator (enrollment controls apply).

## Local freya-stack validation (T114, 2026-10-02)

Stack: freya-stack with locally built inventory 4.7.0-dev033 (agent built
as 4.7.0), deployer 4.4.0-dev033, lcm 4.4.1-dev033 (inventory-download
rule), auth 4.7.0 / portal 4.6.0 (production pair; the deployer remote
needs `@go-tangra/ui` ≥ 4.3), configs from contracts/mesh-policy.md;
inventory migrated to 11 (pg_dumpall backup taken first). Two Debian 12
containers run the agent as root (`cert-host-a`, `cert-host-b` tagged
`role=web`), dev plaintext opt-outs on both sides; a self-signed test issuer
`test-web` (trust domain `test.local`) issues the certificates.

- [x] 1 Capability — both `enabled`; B with `certificates.enabled: false`
  → `disabled_on_host`; re-enabled. (A dev agent version `4.7.0-dev033`
  shows `upgrade_required`: semver prerelease < 4.7.0, correct.)
- [x] 2 US1 — Preview lists both hosts `enabled`; deploy → "Installed on
  2"; certbot layout, `privkey.pem` `-rw------- root root`, dirs 0750,
  `live/www → ../archive/www/<gen>`, fingerprint = lcm, key matches
  certificate; Verify "present on 2 of 2 hosts".
- [x] 3 Idempotency — redeploy "unchanged on 2", mtimes and generation
  unchanged.
- [x] 4 US2 — auto-deploy target (`*.test.local`), B stopped: renewal
  installed on A within seconds (`renewal_count` 1, `previous_serial`),
  job "queued for 1"; two more renewals → older items `superseded`; B
  started → only the newest installed; host B Certificates `installed`.
- [x] 5 US3 — root 0755 hook: all `LCM_*` variables, `LCM_IS_RENEWAL=true`,
  exit 0; `chmod 0777` → `hook_failed/hook_refused`, hook not run, files
  installed; `exit 3` → `hook_failed` (3); deployer retries re-armed the
  same item (attempts 2, 3) and re-ran only the hook (generation and
  mtimes unchanged) until it succeeded.
- [x] 6 Key policy — CSR certificate (`has_key: false`) with `require` →
  `failed/key_unavailable`, files intact; `certificate_only` with the
  matching key in place → installed, key carried over; other key →
  `failed/key_mismatch`, previous certificate and key intact.
- [x] 7 US4 — host Certificates tab and API: name, CN, serial,
  fingerprint, expiry, state, hook exit, last delivered, deployer link,
  delivery history with reasons; no PEM in page or responses; operator
  cancel of a queued item → `cancelled/cancelled_by_user`.
- [x] 8 US7 — revoke in lcm → host rows `revoked` within seconds, files
  untouched, queued item `cancelled/certificate_revoked`.
- [x] 9 Negative — `cert_name: "../x"` → 422 `pattern` (value not echoed);
  forged `FetchCertificate` of B's item with A's credential and of an
  unknown id → identical `NotFound`, one `cert_delivery_refused/not_found`
  audit row (second throttled), B's item untouched.
- [x] 10 Key hygiene — `PRIVATE KEY` in inventory/deployer/gateway logs,
  agent logs, `pg_dump` inventory and deployer, all 46 Valkey keys
  (strings, hashes, lists, sets, zsets, streams): 0.
- [x] 11 US5 drawer — API: BIG-IP without password → 422
  `credentials.password: required`; a marker password never appears in
  create/get/list/validate responses, deployer logs or the DB dump. UI:
  deployer e2e `configuration drawer (credentials write-only), target
  drawer, jobs` passes at phone, tablet and desktop widths.
- [x] 12 US5 target-supplied — Cloudflare without zone id saved with
  `target_supplied: [zone_id]`; direct deploy fails "configuration
  incomplete: Zone ID must be provided by the target" without retry;
  attach without zone → 422 `config_overrides.zone_id: required`;
  `api_token` / webhook `url` overrides → `not_overridable`; clearing the
  zone of a relied-on configuration → 422 `required_by_targets` naming the
  target; e2e "Cloudflare … target-supplied zone id, end to end" passes.
- [ ] 13 US8 profiles — **not run live** (no lab BIG-IP/FortiGate);
  covered only by the fake iControl/FortiOS provider tests (T106/T107).

Fixed during the validation: deployer d926457 (an empty `zone_id`/`region`
sent by an API caller was refused by the provider check instead of being
left to the targets), 413e400 (e2e API calls send `Origin`), 5ee7310
(image build: UI stage carries the test vectors).

Observations (not fixed, outside 033 or design questions):
- Item failures that a retry cannot fix (`key_unavailable`,
  `key_mismatch`) are re-armed by every deployer retry (attempts reached
  4); the spec does not say whether they should be permanent.
- BIG-IP "Test connection" reports `valid` for an unreachable host
  (`ValidateCredentials` treats a transport error as success; spec 008).
- `@go-tangra/ui` forms: the first field is focused when a drawer opens
  and blur validation adds messages that shift the layout, so the first
  click elsewhere (e.g. a target's configuration checkbox) is lost.
- axe `scrollable-region-focusable` (serious) on a `.rounded-box` table
  container on the deployer dashboard (kit table, not 033 code); the two
  deployer a11y specs fail on this and on the lost first click.
