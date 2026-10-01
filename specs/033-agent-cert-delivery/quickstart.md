# Quickstart: validating certificate delivery to inventory agents (033)

## Automated

- **inventory** (`go-tangra-inventory-v4`):
  - `make test` — `certmaterial` (names, tags, PEM bundles, key match,
    bounds), `certdelivery` service with memstore and fake lcm/registry
    (selection, idempotency, re-arm, supersede, older-than-installed,
    capability → unsupported, fetch authorization, report transitions,
    sweeper, revocation, verify), ingest handlers, mesh handlers incl.
    source allow-list, HTTP handlers/permissions, `agentcerts` store with a
    fake filesystem (atomic swap, idempotency, crash recovery, pruning,
    `certificate_only` key carry-over, unsafe directories) and hook
    checks/environment, daemon command handling.
  - `make cover` — 100 % for `internal/certmaterial`,
    `internal/certdelivery`, `internal/agentcerts` (pure core) in addition
    to the existing security packages.
  - `make fuzz` — `FuzzValidName`, `FuzzParseBundle`, `FuzzHostTag`,
    `FuzzReportCertificate`, `FuzzAgentCertsConfig`.
  - `make test-integration` — migration 0010 on a 0009 database with data;
    RLS isolation of the three tables; partial unique index (one active item
    per host+name); end-to-end over bufconn: mesh `CreateCertificateDelivery`
    → ingest `StreamCommands` (CERTIFICATE command) → `FetchCertificate`
    (fake lcm) → `ReportCertificate`; foreign item ids refused; **key scan**:
    after the run, `pg_dump` of the inventory DB, captured logs, audit rows,
    Valkey keys/streams contain no `PRIVATE KEY` header and none of the test
    key's DER bytes (SC-003).
  - `make test-agent-certs` (Linux, root in a container) — real filesystem:
    owner/group/modes, `O_NOFOLLOW` refusal of a symlinked `live/`, hook as
    root-owned script vs. user-owned script (refused), timeout kills the
    process group, environment exactly as contract.
  - `make proto-check` — `buf lint` + `buf breaking` against `sdk/v4.3.0`.
  - `make vuln`, agent cross-compile matrix, UI lint/unit/build.
- **deployer** (`go-tangra-deployer-v4`): `make test` (provider with a fake
  inventory client: Deploy outcomes for every D9 row, wait clamping,
  idempotency key = job id, Verify, ValidateConfig negatives; scheduler
  fetches without key for `delivers_by_reference`; JobMeta on context;
  configs reject invalid provider config; consumer forwards
  `certificate.revoked`), `make cover`, `make fuzz`
  (`FuzzInventoryAgentConfig`), UI unit tests for the form and host
  picker, the schema-driven drawer (`provider-form.spec.ts`,
  `provider-schema.spec.ts` over the shared Go/TS vectors), descriptor
  validator unit + fuzz tests (`FuzzValidateInput`), `make vuln`.
- **lcm** (`go-tangra-lcm-v4`): policy test that `svc/inventory` may call
  only `Certificates/Download`.

## Manual (freya-stack)

Prerequisites: inventory ≥ 4.7.0 and deployer ≥ 4.4.0 in freya-stack
(`docker compose -p freya-stack …`), stack configs/policies from
[contracts/mesh-policy.md](contracts/mesh-policy.md) applied and services
restarted, an operator signed in with deployer and inventory
administrator rights, an lcm issuer that can issue a server certificate
with a module-generated key, two Linux test hosts (or containers with
systemd) running agent 4.7.0 enrolled to the stack (dev: `insecure: true`
+ `certificates.allow_insecure_transport: true`; inventory
`cert_delivery.allow_plaintext_ingest: true`).

1. **Capability** — Inventory → Agents: both agents show Certificates
   "enabled". On host B set `certificates.enabled: false`, restart: B shows
   "disabled on host". Re-enable.
2. **US1** — Deployer → Configurations → New: provider "Inventory agent",
   pick host A, tag `role=web` (set on host B in Inventory), name `www`.
   Validate → both hosts listed "enabled". Issue `www.test.local` in lcm,
   Deployer → Deploy to this configuration. Job completes "Installed on 2";
   on each host:
   `ls -l /etc/inventory-agent/certs/live/www/` → `cert.pem chain.pem
   fullchain.pem privkey.pem`, `privkey.pem` `-rw------- root root`;
   `readlink /etc/inventory-agent/certs/live/www` →
   `../archive/www/<gen>`; `openssl x509 -noout -fingerprint -sha256 -in
   …/cert.pem` equals the lcm fingerprint. Verify → success.
3. **Idempotency** — Deploy the same certificate again: job "unchanged on
   2"; `stat` mtimes of the files unchanged; hook (step 5) not run.
4. **US2** — Mark the deployer target (with this configuration, filter CN
   `*.test.local`) auto-deploy. Stop the agent on host B. Renew the
   certificate in lcm. Host A receives it within a minute (new `<gen>`,
   `renewal/www.json` `renewal_count` +1, `previous_serial` set); the job
   says "queued for 1". Start host B's agent → within a minute host B holds
   the renewed certificate; Inventory → host B → Certificates shows it
   "installed". Renew twice while B is stopped → only the newest is
   delivered, the older item shows "superseded".
5. **US3** — On host A create `/usr/local/sbin/www-hook.sh` (root, 0755)
   that writes `env | grep ^LCM_` to `/var/tmp/hook.env`; set
   `certificates.deploy_hook` to it; restart. Renew → `/var/tmp/hook.env`
   has all v3 variables with correct values, `LCM_IS_RENEWAL=true`; job
   shows hook exit 0. `chmod 0777` the script → next renewal reports "hook
   refused", files still installed. Make it `exit 3` → "hook failed (3)";
   deployer retry re-runs the hook only.
6. **Key policy** — Issue a certificate from a CSR in lcm (no key
   retained) and deploy it with `key_policy: require` → host shows "failed:
   key unavailable". Switch to `certificate_only` with a matching
   `privkey.pem` already in place → installed, key carried over; with a
   non-matching key → "failed: key mismatch", previous files intact.
7. **US4** — Inventory → host A → Certificates tab: name, CN, serial,
   fingerprint, expiry, state, last delivered, hook exit, link to the
   deployer configuration; no PEM anywhere in the page or the API responses
   (DevTools network). Cancel a queued item (host B stopped) as operator.
8. **US7** — Revoke the certificate in lcm → within a minute host A's row
   shows "revoked"; files untouched; queued deliveries of it cancelled.
9. **Negative** — Configuration with `cert_name: "../x"` → 422. A forged
   `FetchCertificate` with host B's item id using host A's credential
   (grpcurl) → NotFound + `cert_delivery_refused` in inventory audit.
10. **Key hygiene** — `docker compose -p freya-stack logs inventory
    deployer | grep -c "PRIVATE KEY"` → 0; `docker compose -p freya-stack
    exec timescaledb pg_dump -U postgres inventory | grep -c "PRIVATE
    KEY"` → 0; `valkey-cli --scan` + `XRANGE platform:events:<tenant>`
    contain no material.
11. **US5 — configuration drawer** — Deployer → Configurations → New: no
    provider fields until a provider is chosen. Choose "F5 BIG-IP":
    Connection (host, partition pre-filled `Common`), Credentials
    (username, masked password), each with help and placeholder. Save with
    the password empty → password highlighted "required", no request in
    DevTools. `curl` the same POST without `password` → 422
    `{"reason":"validation_failed","detail":{"fields":{"credentials.password":"required"}}}`.
    Fill it, Test connection → "valid" (or "credentials rejected" with a
    wrong password, the password not shown anywhere). Save, reopen: host,
    partition, username pre-filled, password empty "Stored — leave blank
    to keep"; change the partition, save, Test connection still succeeds
    (stored password kept). Switch provider on a new configuration with
    values entered → confirm dialog. Webhook: add header `Authorization` →
    refused. Repeat a create for every provider (AWS ACM, Cloudflare,
    FortiGate, Webhook, Dummy, Inventory agent) without reading docs.
    `GET /configurations/{id}` and `docker compose -p freya-stack logs
    deployer` contain none of the entered secrets.

## Production rollout (user-confirmed steps)

1. Release inventory `sdk/v4.4.0` and inventory v4.7.0 (approve the
   `release` environment signing job); back up the inventory DB; deploy;
   migration 0010 applies; `cert_delivery.enabled` stays false until step 3.
2. Upgrade agents through Inventory → Agents → "Upgrade all" (023).
3. Apply policies (lcm, inventory) and configs (inventory
   `cert_delivery`, deployer `inventory` + discovery) in go-tangra-docker;
   restart lcm (policy reload), inventory, deployer.
4. Release and deploy deployer v4.4.0.
5. Run manual steps 1–3 against one production host before enabling
   auto-deploy targets.
