# Feature Specification: Deliver Issued and Renewed Certificates to Inventory-Agent Hosts

**Feature Branch**: `033-agent-cert-delivery`

**Created**: 2026-10-02

**Status**: Draft

**Spans**: go-tangra-inventory-v4 (agent, inventory service, SDK, UI — primary),
go-tangra-deployer-v4 (new deployment provider `inventory-agent`,
schema-driven configuration drawer for all providers, UI),
go-tangra-lcm-v4 (mesh policy only), go-tangra-docker (stack configuration
and policies, user-applied)

**Input**: User description: "go-tangra-client had an option when a
certificate is issued or renewed to be sent to a specific client. I'd like
the same functionality for the inventory-agent."

**Amendment** (2026-10-02, user): "In this spec include a deployer
configuration drawer change: make it more user-friendly, add required
fields per provider just like v3 does." → User Story 5, FR-023–FR-031,
SR-011–SR-014, SC-007–SC-008.

**Binding user decisions** (2026-10-02):

1. Delivery is configured as a **new deployer provider `inventory-agent`**:
   a target configuration selects hosts by host ids and/or host tags, and
   the deployer's existing automation applies unchanged — automatic
   deployment on issued/renewed certificates with certificate filters,
   manual deployment, job retries and Verify. The inventory module relays
   the certificate to the agent; the private key exists only in the
   inventory module's memory while it is relayed and is never persisted
   there.
2. After writing the files the agent may run **only a deploy hook script
   named in the agent's local configuration** (never sent by the server),
   disabled by default. There is no built-in reload of nginx, systemd units
   or anything else.
3. Files go to **one fixed directory from the agent's local configuration**
   (default `/etc/inventory-agent/certs`) in the certbot layout
   `live/<name>/{cert.pem,privkey.pem,chain.pem,fullchain.pem}` plus
   metadata; writes are atomic; owner, group and modes come from the local
   configuration; the server only chooses `<name>`, which is validated as a
   safe file name (no path traversal).

## Context

In v3, a deployment target of type "Tangra Client" named go-tangra-client
agents (by client id or by labels). When lcm issued or renewed a matching
certificate, the deployer published it, the v3 lcm streamed it to every
connected client, the client wrote it in the certbot layout
(`live/<name>/cert.pem`, `privkey.pem` 0600, `chain.pem`, `fullchain.pem`,
metadata in `renewal/<name>.json`), ran an optional deploy hook with
`LCM_*` environment variables (5 min timeout) and reported the installed
serial and SHA-256 fingerprint, which the deployer's Verify compared.

Weakness carried over from v3 that v4 must not repeat: the push was
publish/subscribe — a client that was offline at that moment never received
the certificate (or its renewal) and nothing retried.

In v4 the host agent is the inventory agent. It already holds an
authenticated, reconnecting command stream to the inventory module and a
pattern for persisted, replay-on-connect commands (agent self-upgrade,
feature 023). The deployer already reacts to lcm `certificate.issued` /
`certificate.renewed` events with certificate filters, creates jobs, retries
them and verifies deployments through providers.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deploy a certificate to selected hosts (Priority: P1)

A platform administrator creates a deployer target configuration of type
"Inventory agent" that selects the hosts `web-1` and `web-2` (by host id)
and every host tagged `role=edge`, with certificate name `www`. She deploys
the certificate `www.example.com` to it. Within a minute each online
selected host has `/etc/inventory-agent/certs/live/www/cert.pem`,
`privkey.pem` (mode 0600), `chain.pem` and `fullchain.pem`; the deployment
job shows per host "installed" with serial and fingerprint, and Verify
confirms that every host holds the expected certificate.

**Why this priority**: This is the requested capability; everything else
builds on a working, secure delivery path.

**Independent Test**: With one online agent and one deployer target
configuration naming its host, a manual deployment produces the four files
with the configured owner and modes, the job completes with the host
"installed", and Verify succeeds; the private key never appears in any
inventory table, log or event.

**Acceptance Scenarios**:

1. **Given** a target configuration selecting hosts by id and/or tag,
   **When** a certificate is deployed to it, **Then** every selected host
   whose agent supports certificate delivery receives the certificate,
   chain, full chain and private key under `live/<name>/` in its configured
   directory, and reports "installed" with serial and fingerprint.
2. **Given** a host selected both by id and by tag, **Then** it receives the
   certificate once.
3. **Given** the deployment completed, **When** the administrator runs
   Verify, **Then** it succeeds when every selected host reports the
   expected certificate fingerprint under that name (without "require all
   hosts": at least one does and none holds a different or revoked
   certificate), and lists the hosts that do not.
4. **Given** a host already holding exactly this certificate under this
   name, **When** it is deployed again, **Then** no file is rewritten, the
   hook is not run, and the host reports "unchanged".
5. **Given** a selection that matches no host, **Then** the deployment fails
   with "no hosts matched" and nothing is delivered.
6. **Given** a name containing `/`, `..`, a leading dot, a control character
   or more than 64 characters, **Then** the configuration is rejected when
   saved and, as a second line of defence, the agent refuses it.

---

### User Story 2 - Renewals and offline hosts are delivered automatically (Priority: P1)

The administrator marks the deployer target (with that configuration and a
certificate filter `*.example.com`) for automatic deployment. When lcm
renews the certificate, the renewed certificate reaches every selected
host without anyone acting. A host that was switched off during the renewal
receives it when its agent reconnects the next morning.

**Why this priority**: Automatic renewal is the reason to deliver through
the platform; v3 lost renewals for offline clients.

**Independent Test**: With automatic deployment on, issuing a renewal of a
matching certificate while one agent is offline delivers it to the online
agent at once and to the offline agent within one minute of its reconnect;
the deployment job reports the offline host as "queued", not failed.

**Acceptance Scenarios**:

1. **Given** automatic deployment with certificate filters, **When** a
   matching certificate is issued or renewed, **Then** it is delivered to
   every selected host under the configured name, replacing the previous
   certificate in place (same paths).
2. **Given** a selected host whose agent is offline, **Then** the delivery
   stays queued and is delivered when the agent reconnects, unless it has
   expired (default 7 days, never later than the certificate's expiry).
3. **Given** a renewal arrives while an older certificate for the same host
   and name is still queued, **Then** only the newest is delivered; the
   older one is marked "superseded".
4. **Given** a deployment job whose hosts are partly queued, **Then** the job
   completes with "delivered to N, queued for M" and the per-host states
   are visible in the job and in the inventory.
5. **Given** a host that failed (for example disk full), **When** the
   deployer retries the job, **Then** the failed host is attempted again;
   hosts that already installed it are not touched again.

---

### User Story 3 - Local deploy hook after installation (Priority: P1)

The host owner of `web-1` sets, in the agent's local configuration, a deploy
hook `/usr/local/sbin/reload-nginx.sh`. After a certificate is installed or
renewed on that host, the agent runs the script with the same `LCM_*`
environment variables as the v3 client, and the job shows "installed, hook
exit 0". On a host without a configured hook nothing is executed.

**Why this priority**: Without a reload the new certificate is not used;
v3 parity; the user decided the hook is local-only and off by default.

**Independent Test**: With a local hook that writes its environment to a
file, delivering a certificate produces the file with the v3 variable
names and values; delivering the same certificate again does not run the
hook; a hook exiting non-zero or exceeding its timeout is reported as
"hook failed" with the exit code, while the certificate files stay
installed.

**Acceptance Scenarios**:

1. **Given** no hook in the local configuration (default), **Then** the
   agent never executes anything after writing the files.
2. **Given** a configured hook, **When** a certificate is newly installed or
   replaced, **Then** the agent runs exactly that executable (no shell, no
   arguments from the server) with `LCM_CERT_NAME`, `LCM_CERT_PATH`,
   `LCM_KEY_PATH`, `LCM_CHAIN_PATH`, `LCM_FULLCHAIN_PATH`,
   `LCM_COMMON_NAME`, `LCM_DNS_NAMES`, `LCM_IP_ADDRESSES`,
   `LCM_SERIAL_NUMBER`, `LCM_EXPIRES_AT`, `LCM_IS_RENEWAL` and a bounded
   timeout (default 5 minutes).
3. **Given** the hook fails or times out, **Then** the host reports
   "hook failed" with the exit code (or "timeout"); hook output stays in
   the host's local log and is never sent to the platform.
4. **Given** a hook file that is not an absolute path, not a regular file,
   not owned by root, or writable by group or others, **Then** the agent
   refuses to run it and reports "hook refused".
5. **Given** the server sends anything resembling a command, script or path
   other than the certificate name, **Then** the agent has no way to act on
   it (the protocol carries no such field).

---

### User Story 4 - See delivered certificates per host (Priority: P2)

In the inventory, the host page of `web-1` has a **Certificates** tab:
name `www`, common name, serial, fingerprint, expiry, state "installed",
last delivered, hook result, and the delivery history. The agent list shows
whether each agent can receive certificates (enabled, disabled locally,
not supported by its version or platform).

**Why this priority**: Operators need to see what is on a host without
logging in to it; the deployer job view already covers the essentials.

**Independent Test**: After deliveries in several states, the host page
lists one row per name with the latest state, never shows key material,
and the agent list shows the certificate capability per agent.

**Acceptance Scenarios**:

1. **Given** a host with delivered certificates, **Then** its Certificates
   tab shows per name the certificate identity, state, last delivery time,
   failure reason or hook exit code, and a link to the deployer
   configuration that delivered it.
2. **Given** a user with inventory read permission only, **Then** the tab is
   visible read-only; cancelling a queued delivery requires the
   agent-management permission.
3. **Given** an agent older than this feature, a Windows agent, or an agent
   with certificates disabled locally, **Then** the agent list shows the
   reason and deliveries to it end as "unsupported".

---

### User Story 5 - Configure any deployment provider with a guided form (Priority: P2)

An administrator opens Deployer → Configurations → New. She first picks
the provider ("F5 BIG-IP"); the drawer then shows only that provider's
fields, grouped into **Connection** (host, partition — pre-filled
`Common`), **Credentials** (username, password — masked) and **Options**,
each with a label, a short help text and an example placeholder; required
fields are marked. She presses **Save** without a password: the password
field is highlighted "required" and nothing is sent. She fills it in,
presses **Test connection** (the appliance answers), and saves. A week
later she edits the configuration: host, partition and username are
pre-filled, the password field is empty with "Stored — leave blank to
keep", and saving a new partition keeps the stored password. The same
guided form serves every provider (AWS ACM, F5 BIG-IP, Cloudflare,
FortiGate, Webhook, Dummy and the new Inventory agent) — like the v3
drawer, which showed per-provider required fields — instead of today's two
raw JSON text areas.

**Why this priority**: Usability and correctness of every provider's
configuration; the inventory-agent form (US6) is built on it. P2, not P1:
the P1 stories are releasable with the existing JSON editor, and this
story touches only the deployer. It is independent of US1–US4 (it needs
only the foundational field descriptors) and should ship in the same
deployer release as US6.

**Independent Test**: For each of the seven providers, a configuration
created through the drawer equals the configuration its JSON contract
describes; saving with any one required field empty is refused in the
browser with that field highlighted, and the same request sent directly
to the API is refused with HTTP 422 naming that field; no API response,
validation message or log line ever contains a secret value that was
entered.

**Acceptance Scenarios**:

1. **Given** the New configuration drawer, **Then** the provider is the
   first choice and no provider-specific field is shown until a provider is
   chosen; **When** a provider is chosen, **Then** exactly that provider's
   fields appear with label, help text, placeholder and default value.
2. **Given** a provider's fields, **Then** they are grouped into
   Connection, Credentials and Options (empty sections hidden), and each
   field uses an input fitting its kind: text, multi-line text, URL,
   number with bounds, on/off switch, choice list, list of values,
   key/value pairs, masked secret, or host picker.
3. **Given** a required field left empty or a value violating its rule
   (pattern, range, length, choice), **When** the administrator saves,
   **Then** the save is refused in the browser with the field highlighted
   and focused and a message naming the rule; **and** the server
   independently refuses the same input with HTTP 422 naming the field
   (`config.<key>` / `credentials.<key>`), so a direct API call cannot
   bypass the rule.
4. **Given** a secret field (password, token, secret key), **Then** it is
   masked with an optional reveal, never pre-filled, and never returned by
   any read; **when** editing a configuration whose secret is stored,
   **then** the field shows "Stored — leave blank to keep", a blank field
   keeps the stored value, a new value replaces only that secret, and an
   optional secret can be cleared explicitly.
5. **Given** an existing configuration, **When** it is edited, **Then** its
   non-secret settings (including non-secret credential values such as
   host and user name, for users allowed to manage the configuration) are
   pre-filled and the provider cannot be changed.
6. **Given** values already entered for one provider, **When** the
   administrator switches to another provider while creating, **Then** the
   drawer asks to discard them; confirming clears all provider fields,
   cancelling keeps the previous provider and values.
7. **Given** a provider that can contact its endpoint (BIG-IP, FortiGate,
   Webhook), **Then** a **Test connection** action validates the entered
   values (and, when editing, the stored secrets) without saving and shows
   "valid" or "credentials rejected" without echoing any value; other
   providers offer **Check settings** (input rules only) and Inventory
   agent **Preview hosts** (US6).
8. **Given** a configuration saved before this feature with a setting the
   provider does not declare or a missing required field, **Then** it
   still opens and deploys as before, the drawer lists the unrecognised
   settings ("removed when you save") and highlights missing required
   fields, and a deployment of a configuration that lacks a required field
   fails before contacting the endpoint with "configuration incomplete".
9. **Given** a keyboard or screen-reader user, **Then** every field is
   reachable and labelled, required state, help text and errors are
   announced, and the sections are headed.

---

### User Story 6 - Pick hosts in the deployer UI (Priority: P2)

When creating an "Inventory agent" configuration in the guided form (US5),
the administrator picks hosts from a searchable list of inventory hosts
(hostname, OS, tags, agent online, certificate support) and/or enters tag
selectors, sees a preview of the matching hosts, and sets name, key
handling, "require all hosts" and the wait time — instead of typing JSON.

**Why this priority**: Usability; the JSON configuration works without it.
Depends on US5 (the generic form renders this provider's fields; this story
adds the host picker and the preview).

**Independent Test**: The form saves a configuration equal to the JSON
contract; the preview matches the hosts the inventory resolves; a user
without inventory read permission can still enter host ids and tags
manually.

**Acceptance Scenarios**:

1. **Given** the provider "Inventory agent" is selected, **Then** the
   guided form (US5) shows its fields and renders the host selection with
   the host picker; the Credentials section is hidden (the provider has no
   credentials).
2. **Given** host ids and tag selectors, **When** the administrator presses
   Preview hosts, **Then** the matched hosts are listed with their
   certificate support state.
3. **Given** the user cannot read inventory hosts, **Then** the picker shows
   a notice and accepts manual entry.
4. **Given** neither hosts nor tags, **When** saving, **Then** both fields
   are highlighted "select hosts or enter tags" (client and server).

---

### User Story 7 - Revoked certificates are flagged, never auto-removed (Priority: P3)

When lcm revokes a certificate that was delivered to hosts, the inventory
marks it "revoked" on those hosts and cancels queued deliveries of it. The
files stay on the hosts (removal could break running services); the
administrator sees which hosts still hold a revoked certificate.

**Why this priority**: Safety net; revocation is rare and the renewal path
(US2) is the normal remedy.

**Independent Test**: Revoking a delivered certificate in lcm flags it on
every host holding it within one minute, cancels queued deliveries of it,
audits both, and leaves the host files untouched.

**Acceptance Scenarios**:

1. **Given** a revoked certificate installed on hosts, **Then** those hosts'
   Certificates tabs show it as revoked and the inventory audit records it.
2. **Given** a queued delivery of a revoked or expired certificate,
   **Then** it is never delivered (cancelled with reason "certificate
   revoked"/"certificate expired").

### Edge Cases

- **Certificate without a key in lcm** (issued from a CSR, or the key was
  already handed out): by default the delivery fails with "key
  unavailable"; a configuration may opt into "certificate only", in which
  case the agent writes `cert.pem`, `chain.pem`, `fullchain.pem`, keeps an
  existing `privkey.pem` only if it matches the certificate, and fails with
  "key mismatch" otherwise (nothing changed).
- **Agent cannot receive certificates** (older version, Windows, or disabled
  locally): the host's delivery ends "unsupported" with the reason; with
  "require all hosts" the job fails, otherwise it completes partially.
- **Selected host without an agent** (manual/imported host) or retired
  host: "unsupported" (no agent) / excluded from tag selection.
- **Host deleted or agent revoked** while a delivery is queued: the
  delivery is cancelled.
- **Two configurations deliver different certificates under the same name
  to the same host**: the newest delivery wins and the older is
  "superseded"; the host page shows which configuration owns the name.
- **Agent crashes between writing and reporting**: on restart the agent
  completes or discards the half-written generation and reports again; the
  inventory marks a fetched delivery without report after 15 minutes as
  failed ("no report"), so a deployer retry re-sends it.
- **Disk full / directory not writable / owner or group unknown on the
  host**: failed with a specific reason; the previous certificate stays in
  place and in use.
- **Expired certificate**: never delivered.
- **Oversized material** (chain > 256 KiB, key > 16 KiB): refused by the
  inventory before relaying and by the agent.
- **lcm unreachable** when the agent fetches: the agent retries with backoff
  while the delivery stays active; after the delivery expires it fails.
- **Duplicate commands** (reconnect, several inventory replicas): the agent
  deduplicates by delivery id; fetching an already finished delivery is
  refused.
- **Plaintext ingest edge** (development stack): delivery is refused by
  both sides unless explicitly allowed for development.
- **Large fleets**: one configuration may select up to 1000 hosts; delivery
  load is bounded by concurrent fetch limits.
- **Configuration drawer — legacy rows**: configurations saved before the
  guided form may hold undeclared settings or miss a required field; they
  keep working until edited (US5 scenario 8); a deployment that lacks a
  required field fails before contacting the endpoint.
- **Configuration drawer — required value supplied by a target override**:
  v3 validated required config fields on the merged (configuration +
  target override) config; here the configuration itself must be complete
  and overrides may only change declared values (research D25).
- **Configuration drawer — secret typed into a non-secret field** (for
  example an `Authorization` header in webhook custom headers): refused;
  such values belong in the sealed credential fields.
- **Provider removed or renamed** after configurations were saved: the
  drawer shows the stored values read-only with "provider not available";
  saving is refused.

## Requirements *(mandatory)*

### Functional Requirements

**Deployer provider**

- **FR-001**: The deployer MUST offer a provider `inventory-agent` whose
  configuration selects hosts by explicit host ids (≤ 1000) and/or host tag
  selectors (`key` or `key=value`, ≤ 16, all must match), names the
  certificate (`<name>`, default derived from the certificate's common
  name), chooses key handling (`require` default, or `certificate_only`),
  "require all hosts" (default off), and how long a deployment waits for
  host results (default 60 s, bounded by the job timeout).
- **FR-002**: The provider MUST work with every existing deployer mechanism:
  manual deployment to a configuration or target, automatic deployment on
  `certificate.issued`/`certificate.renewed` with certificate filters,
  per-target configuration overrides, retries with backoff, and Verify.
  Rollback is not supported (as in v3).
- **FR-003**: The provider configuration MUST be validated when saved
  (schema, bounds, name rules), and Validate MUST show the hosts the
  selection currently matches with their delivery capability.
- **FR-004**: A deployment job MUST complete when no selected host failed
  and at least one host installed, already had, or has queued the
  certificate; it MUST fail when no host matched, when every host failed,
  or — with "require all hosts" — when any host failed or is unsupported.
  Otherwise (some hosts failed, "require all hosts" off) it completes as a
  partial success. The job result MUST list per-host states and counts.
- **FR-005**: Verify MUST compare, for every currently selected host, the
  fingerprint the host last reported for `<name>` with the certificate's
  fingerprint and report matching, mismatching, failed and pending hosts.
- **FR-006**: The deployer MUST NOT need, fetch or hold the private key for
  this provider; it passes only references (tenant, certificate id, name,
  selection) to the inventory module.

**Inventory relay**

- **FR-007**: The inventory module MUST accept delivery requests only from
  the deployer's service identity, resolve the selection within the
  request's tenant at request time, and create one delivery per selected
  host; a repeated request for the same deployment job MUST return the
  existing delivery (idempotent) and re-arm only hosts that failed.
- **FR-008**: The inventory module MUST push a delivery notice to the host's
  agent when it is connected and again on every reconnect while the
  delivery is active; deliveries MUST persist across inventory restarts and
  replicas and expire after a configurable time (default 7 days, never
  beyond the certificate's expiry).
- **FR-009**: The agent MUST obtain the certificate material over its own
  authenticated connection by delivery id; the inventory module MUST fetch
  the material from lcm at that moment, check that the certificate is not
  revoked or expired, validate it, relay it, and discard it — the private
  key MUST NOT be written to the inventory database, cache, event bus,
  logs or audit.
- **FR-010**: Per host and name, only the newest delivery is active; older
  active deliveries for the same host and name MUST be marked superseded.
- **FR-011**: The inventory module MUST record per delivery: host, agent,
  name, certificate id, serial, fingerprint, state (pending, delivered,
  fetched, installed, unchanged, failed, hook failed, unsupported,
  superseded, expired, cancelled), reason, hook exit code, attempts and
  timestamps; and per host and name the currently installed certificate.
- **FR-012**: The inventory module MUST answer the deployer's status and
  verification queries for a delivery or a selection.
- **FR-013**: When the deployer forwards an lcm `certificate.revoked`
  event, the inventory module MUST cancel active deliveries of that
  certificate and flag hosts holding it as revoked; files on hosts are not
  removed.

**Agent**

- **FR-014**: The agent MUST announce the capability `cert.v1` only when
  certificate delivery is enabled in its local configuration, its platform
  is supported (Linux in this feature), and the transport is TLS (or
  plaintext explicitly allowed for development).
- **FR-015**: The agent MUST write certificates only below its configured
  directory (default `/etc/inventory-agent/certs`) as
  `live/<name>/{cert.pem,chain.pem,fullchain.pem,privkey.pem}` plus metadata
  `renewal/<name>.json` (v3 layout), replacing the set atomically so that a
  reader never sees a certificate with a non-matching key, with owner,
  group and modes from the local configuration (defaults root:root, key
  0600, certificates 0644, directories 0750).
- **FR-016**: Before writing, the agent MUST validate the name, parse the
  PEM material, check that the private key matches the certificate, that
  the certificate is currently valid, and the size bounds; if the
  installed certificate under that name already has the same fingerprint
  and its files are intact, it MUST NOT rewrite anything or run the hook,
  and reports "unchanged".
- **FR-017**: After a new or replaced installation, the agent MUST run the
  locally configured deploy hook if one is set (US3, SR-006), and report
  installed/unchanged/failed/hook-failed with serial, fingerprint, reason
  and hook exit code.
- **FR-018**: The agent MUST keep the previous generation of each name on
  disk (configurable 0–5, default 1) so an operator can restore it by hand.

**User interfaces**

- **FR-019**: The inventory UI MUST show per host the delivered
  certificates (name, common name, serial, fingerprint, expiry, state,
  last delivered, reason/hook exit code, revoked flag, delivering
  configuration) and the delivery history; and per agent its certificate
  capability.
- **FR-020**: Users with the agent-management permission MUST be able to
  cancel a queued delivery; all other actions start in the deployer.
- **FR-021**: The deployer UI MUST render the `inventory-agent`
  configuration in the guided form (FR-027) with a host picker (search,
  tags, online, capability), a matched-hosts preview, and manual entry
  when the user cannot read inventory hosts; job details MUST show
  per-host results.

**Audit**

- **FR-022**: Every delivery request, push, fetch of material, result,
  supersession, expiry, cancellation, refusal and revocation flag MUST be
  audited in the inventory module (actor, host, agent, certificate id,
  serial, name, state, reason); the deployer audits jobs and configuration
  changes as today, with delivery counts in the job details.

**Deployer configuration drawer (all providers)**

- **FR-023**: Every deployer provider MUST describe each configuration and
  credential field it accepts with: key, label, kind (text, multi-line
  text, URL, integer, boolean, choice, list of values, key/value pairs,
  host selection), required, secret, default, choices, help text,
  placeholder, display group (Connection, Credentials, Options) and
  validation rules (pattern, minimum/maximum, maximum length, maximum
  items), plus "at least one of" groups; the deployer MUST serve these
  descriptions with the provider catalogue.
- **FR-024**: The descriptions MUST be the single source of truth: the UI
  builds its form and client-side validation from them, and the deployer
  MUST validate every configuration create, update, validate request and
  target override (merged effective configuration) against the same
  descriptions, refusing invalid input with HTTP 422 that names each
  offending field (`config.<key>` / `credentials.<key>`) and the rule
  violated; settings a provider does not declare MUST be refused.
- **FR-025**: Every setting the existing providers read MUST be declared
  (contracts/deployer-config-ui.md §7), including the webhook settings the
  v4 drawer omits today (authorization header, API key, timeout, TLS
  verification switch, custom headers, metadata) and FortiGate's import
  scope; test-only endpoint overrides MUST NOT be accepted from stored
  configurations.
- **FR-026**: The required fields per provider MUST at least match v3
  (AWS ACM: region, access key id, secret access key; BIG-IP: host,
  username, password, partition; Cloudflare: zone id, API token;
  FortiGate: host, API token, VDOM; Webhook: URL; Inventory agent: hosts
  or host tags — the v3 "Tangra client" equivalent).
- **FR-027**: The configuration drawer MUST ask for the provider first, then
  show only that provider's fields grouped by display group, with labels,
  help, placeholders, defaults (pre-filled on create), required markers
  and an input suited to each kind; switching provider with entered values
  MUST ask before discarding them.
- **FR-028**: On edit, the drawer MUST pre-fill non-secret settings and
  non-secret credential values, leave secret inputs empty with "Stored —
  leave blank to keep", and the deployer MUST merge credentials per field
  (blank keeps, a value replaces that field only, an explicit clear
  removes an optional field); the provider of an existing configuration
  cannot be changed.
- **FR-029**: The drawer MUST offer a validate action per provider —
  "Test connection" when the provider contacts its endpoint, "Check
  settings" otherwise, "Preview hosts" for Inventory agent — which runs
  without saving and, when editing, uses the stored secrets for blank
  secret inputs.
- **FR-030**: Server-side field errors MUST be shown inline on the matching
  inputs with focus on the first; provider rejections MUST be shown as a
  form-level message.
- **FR-031**: Configurations saved before this feature MUST stay readable
  and deployable; the drawer MUST list undeclared settings (removed on
  save) and missing required fields; a deployment whose effective
  configuration lacks a required field MUST fail before the provider runs
  with "configuration incomplete" naming the field's label.

### Security Requirements

- **SR-001**: Trust boundaries: deployer → inventory and inventory → lcm are
  mesh calls (SPIFFE mTLS) authorized by explicit allow rules per method
  (deployer → inventory delivery methods only; inventory → lcm
  `Certificates/Download` only); agent → inventory uses the existing
  per-agent credential over TLS on the ingest edge.
- **SR-002**: Data classification: the private key is **secret** — it
  exists in the inventory process memory only for the duration of one
  fetch, is never persisted, cached, logged, audited, published or placed
  in the connection registry; on the host it is written only to
  `privkey.pem` with the configured restrictive mode. Certificates, chains,
  serials and fingerprints are internal, non-secret data.
- **SR-003**: The inventory module MUST serve material only for a delivery
  that belongs to the requesting agent (same tenant, same agent, active
  state); any other id → not found, audited as refused.
- **SR-004**: The inventory module MUST call lcm only for certificate ids
  recorded in a delivery request from the deployer, and the deployer's
  requests are accepted only for the tenant they name; a delivery can never
  select hosts of another tenant.
- **SR-005**: The server controls only the certificate material and
  `<name>`; the agent MUST reject any name that is not a safe single path
  component and MUST NOT accept directories, owners, modes, hooks, commands
  or environment from the server.
- **SR-006**: The deploy hook is configured only locally, disabled by
  default, executed without a shell with a minimal environment, a timeout
  and only if the file is root-owned and not writable by group or others.
- **SR-007**: Delivery over a plaintext ingest edge MUST be refused by the
  agent and the inventory module unless each side explicitly opts out for
  development (named settings, warning at start, refused in production).
- **SR-008**: Material size, request counts, selection size, concurrent
  fetches per agent and globally, and lcm call time MUST be bounded.
- **SR-009**: Threat scenarios (STRIDE in research.md): stolen agent
  credential fetching foreign keys, compromised deployer selecting foreign
  hosts, path traversal via name, server-driven code execution via hook,
  key leakage via logs/audit/registry, replay of old certificates
  (downgrade to an older certificate), DoS by mass deliveries.
- **SR-010**: Server-side certificate delivery is off by default in the
  inventory module (`cert_delivery.enabled`), and the agent's
  `certificates.enabled` lets a host owner refuse deliveries.
- **SR-011**: Secret credential values MUST be write-only: sealed at rest as
  today, never returned by any read, list, validate or error response,
  never logged or audited, and never pre-filled in the browser; secret
  inputs MUST use masked, autocomplete-off inputs. Non-secret credential
  values MUST be returned only to callers allowed to manage that
  configuration.
- **SR-012**: Validation and provider error messages MUST be built from the
  field description only and MUST NOT echo any submitted value (secret or
  not); provider error text is logged server-side with values redacted and
  replaced by a fixed message in responses.
- **SR-013**: The provider catalogue with field descriptions MUST require
  the deployer configuration read permission; the validate action and
  configuration writes keep requiring the configuration manage permission
  (for validate with a stored configuration, on that configuration).
- **SR-014**: Settings that can redirect where credentials are sent (test
  endpoint overrides) MUST NOT be accepted from user input, and custom
  HTTP headers MUST NOT carry authentication headers (those belong in
  sealed credential fields).

### Key Entities

- **Inventory-agent target configuration** (deployer): selection (host ids,
  tag selectors), name, key policy, require-all, wait time.
- **Certificate delivery** (inventory): one request from a deployer job —
  tenant, job/configuration reference, certificate id, name, key policy,
  selection, requested by, expiry.
- **Delivery item** (inventory): one host's delivery — host, agent, state,
  reason, serial, fingerprint, hook exit code, attempts, timestamps.
- **Host certificate** (inventory): per host and name, the certificate
  currently installed (or last attempted), revoked flag, delivering
  configuration.
- **Agent certificate store** (host): the fixed directory with `live/`,
  generations and `renewal/<name>.json` metadata.
- **Provider field description** (deployer): per provider, the declared
  configuration and credential fields with kind, required, secret,
  default, choices, help, placeholder, group and validation rules; served
  with the provider catalogue and used for save-time validation.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A manual deployment to 20 online hosts installs the
  certificate on all of them and the job completes within 60 seconds.
- **SC-002**: After a renewal with automatic deployment, 100 % of online
  selected hosts hold the renewed certificate within 2 minutes and 100 % of
  offline hosts within 1 minute of reconnecting (within the expiry window).
- **SC-003**: 0 occurrences of private-key material in the inventory and
  deployer databases, logs, audit rows, event streams and Valkey after an
  end-to-end test run (automated scan for PEM key headers and key bytes).
- **SC-004**: Re-delivering an unchanged certificate causes 0 file writes
  and 0 hook runs on the host.
- **SC-005**: 100 % of crafted unsafe names, foreign delivery ids,
  oversized or mismatched materials and server-supplied paths are refused
  in the negative test suites.
- **SC-006**: An administrator configures a host-delivery target and runs a
  first deployment in under 5 minutes without editing JSON (US6).
- **SC-007**: An operator configures each of the seven providers through
  the drawer without consulting documentation or typing JSON (every field
  has a label, help and an example), and 100 % of saves with a missing or
  invalid required field are refused — in the browser with the field
  highlighted and focused, and by the API with HTTP 422 naming the field
  (tested for every required field of every provider).
- **SC-008**: 0 secret values (submitted passwords, tokens, keys) appear in
  configuration read/list/validate responses, validation messages,
  deployer logs or audit rows after the drawer test suites (automated scan
  for the submitted test secrets).

## Assumptions

- The deployer and inventory run in the same platform and share tenants;
  host ids are inventory host ids.
- Linux agents (deb, rpm, binary installs) are in scope; Windows agents
  do not announce the capability and end "unsupported" (follow-up feature).
- Agents receive this capability by the existing self-upgrade (feature 023);
  no manual reinstall.
- The deployer's job timeout (default 300 s) bounds how long a deployment
  waits; hosts that need longer are reported as queued, not failed.
- lcm needs only a policy change (a second allowed caller of
  `Certificates/Download`); its API is unchanged.
- Notification on delivery failure is out of scope (the deployer job and
  audit carry it); removing certificates from hosts is out of scope.

## Dependencies

- Feature 023 (agent self-upgrade, persisted commands, capability
  announcement) and 029 (auto-enroll) in inventory.
- Deployer feature 008 (providers, jobs, events consumer, Verify).
- lcm feature 007 (`Certificates/Download`, lifecycle events).
- Feature 032 (server-side lists) conventions for the new inventory lists.
- `@go-tangra/ui` kit ≥ 4.3 form components (`useZodForm` server field
  errors, `UiSecretField`, `UiSwitch`, `UiNumberInput`, `UiTagEditor`,
  `UiSection`); no kit release needed (a list-of-values input is local to
  the deployer UI).
