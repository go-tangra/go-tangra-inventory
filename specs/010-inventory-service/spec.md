# Feature Specification: Inventory Service

**Feature Branch**: `010-inventory-service`

**Created**: 2026-09-21

**Status**: Draft

**Input**: User description: replicate and extend go-tangra-inventory as a Freya platform module — a tenant-scoped IT asset inventory system: endpoint agents collect hardware, software/OS and network inventory and report time-series snapshots to a central server that tracks changes, answers queries, and shows a UI.

## Overview

The **inventory** service gives an organization an always-current, tenant-isolated
picture of every managed endpoint (a **host**). A lightweight **agent** installed
on each endpoint gathers its hardware (SMBIOS/DMI), software/OS, and network/storage
inventory and reports it as an immutable **snapshot** to the central **inventory
server**. The server resolves the snapshot to a stable host identity, keeps the
full time-series history, records what changed between snapshots, and exposes
query, diff, statistics and backup capabilities through the platform gateway and
service-to-service APIs, plus a Module-Federation UI.

Two trust planes: the **query/admin API** is an ordinary mesh module (SPIFFE mTLS
service-to-service; platform token via the gateway for the browser), while a
separate **ingest edge** authenticates off-mesh agents that cannot join the mesh,
using per-agent credentials obtained through a tenant-scoped enrollment token.

Vocabulary note: unlike the source project, the **server** is called the inventory
service and the endpoint client is the **agent** (one agent per host).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Enroll an endpoint and receive its first inventory (Priority: P1)

An operator issues a tenant-scoped enrollment token and installs the agent on an
endpoint. On first run the agent enrolls with that token, receives a persistent
per-agent credential, collects the machine's inventory, and submits a snapshot.
The server resolves/creates the host and the endpoint appears in the operator's
host list with its latest inventory.

**Why this priority**: This is the MVP — without ingest of a real snapshot from an
enrolled agent there is no inventory at all. It exercises enrollment, the ingest
edge, host identity resolution, snapshot storage and the host list end to end.

**Independent Test**: Issue an enrollment token; run an agent (or a test client)
that enrolls and submits a snapshot; confirm a host is created and its latest
snapshot is retrievable, scoped to the issuing tenant.

**Acceptance Scenarios**:

1. **Given** a valid, unexpired enrollment token, **When** an agent enrolls, **Then** it receives a per-agent credential bound to the token's tenant and the token cannot be reused.
2. **Given** an enrolled agent, **When** it submits a snapshot, **Then** the server creates (or updates) exactly one host resolved by hardware UUID → machine id → hostname, stores the snapshot, and updates the host's last-seen and summary.
3. **Given** a submission bearing no or an invalid agent credential, **When** it reaches the ingest edge, **Then** it is rejected and nothing is stored.
4. **Given** two agents in different tenants reporting the same hostname, **When** both submit, **Then** each tenant sees only its own host (no cross-tenant collision).

---

### User Story 2 - Browse hosts and inspect full inventory (Priority: P1)

An operator browses the tenant's hosts, filters them, opens a host, and inspects
its hardware, software/OS and network/storage detail.

**Why this priority**: The collected data is only useful if it can be viewed and
searched; this is the primary day-to-day use of the system.

**Independent Test**: With at least one host present, list hosts with filters and
open a host to see hardware, software and network sections populated from the
latest snapshot.

**Acceptance Scenarios**:

1. **Given** hosts exist, **When** the operator lists them filtered by OS/manufacturer/status/tag/last-seen/hostname, **Then** only matching hosts in the caller's tenant are returned, paginated.
2. **Given** a host, **When** the operator opens it, **Then** the latest snapshot's hardware (BIOS, system, board, chassis, CPU, memory, disks, monitors), software (OS, installed programs, services, users, patches) and network interfaces are shown.
3. **Given** the caller lacks inventory read permission, **When** they query, **Then** access is refused.

---

### User Story 3 - Track changes over time (history & diff) (Priority: P2)

An operator reviews a host's snapshot history and compares two snapshots to see
exactly what hardware or software was added, removed or changed.

**Why this priority**: Change tracking (e.g. a RAM module removed, a program
installed) is the key value over a point-in-time inventory and drives audits.

**Independent Test**: Submit two differing snapshots for a host; list its history
and request a diff; confirm the added/removed/changed components are reported.

**Acceptance Scenarios**:

1. **Given** multiple snapshots for a host, **When** the operator lists history, **Then** snapshots are returned newest-first with collected/received times.
2. **Given** two snapshots, **When** the operator diffs them, **Then** the response lists added, removed and modified components per category.
3. **Given** a new snapshot differs from the previous one, **When** it is ingested, **Then** a change-history record is created automatically.

---

### User Story 4 - On-demand refresh and live agent status (Priority: P2)

An operator sees which agents are currently connected and triggers an immediate
re-collection on a specific host without waiting for its next scheduled report;
the UI reflects agent online/offline and new snapshots live.

**Why this priority**: Operators need current data on demand and visibility into
which endpoints are reporting; this must work across multiple server instances.

**Independent Test**: Connect an agent's command stream; trigger a refresh; confirm
the agent receives the command and submits a fresh snapshot, and that connected-agent
status and the new snapshot appear live.

**Acceptance Scenarios**:

1. **Given** a connected agent, **When** the operator triggers a refresh for its host, **Then** the agent receives a refresh command and submits a new snapshot.
2. **Given** the target agent is not connected, **When** a refresh is requested, **Then** the operator is told it was not delivered (and no snapshot is fabricated).
3. **Given** agents connect/disconnect and snapshots arrive, **When** the operator views the agents/hosts screens, **Then** status and new snapshots update live without a manual reload.
4. **Given** multiple server instances, **When** a refresh targets an agent connected to another instance, **Then** the command is still delivered.

---

### User Story 5 - Fleet statistics and backup (Priority: P3)

An operator views fleet statistics (counts and rollups) and an administrator
exports/imports inventory for a tenant.

**Why this priority**: Reporting and portability are valuable but not required for
core collect/view/track flows.

**Independent Test**: With hosts present, read tenant statistics and export then
re-import the tenant's inventory, verifying counts and round-trip integrity.

**Acceptance Scenarios**:

1. **Given** hosts and snapshots exist, **When** statistics are requested, **Then** host counts by status/OS/manufacturer, hardware/software rollups, agents online/offline, stale-host and snapshot-per-day figures are returned for the caller's tenant.
2. **Given** the caller is a system administrator, **When** system-wide statistics are requested, **Then** a per-tenant breakdown is returned.
3. **Given** an export of a tenant, **When** it is imported (skip or overwrite), **Then** hosts, latest snapshots (optionally full history), tags and change history are recreated with schema-version handling.

### Edge Cases

- A host's hardware UUID is missing or all-zero (common on some boards) — identity falls back to machine id, then hostname; the fallback is recorded.
- An enrollment token is expired, already used, or from a different tenant — enrollment is refused.
- An agent submits an oversized or malformed snapshot — it is rejected without partial writes.
- Two snapshots arrive for the same host nearly simultaneously — both are stored; history ordering stays deterministic by collected time.
- A host stops reporting — it is marked stale after a configured interval and surfaces in stale-host statistics.
- Retention purge removes snapshots older than the configured window without deleting the host or its latest snapshot.
- A refresh targets an agent whose stream dropped — the command is not delivered and the operator is informed.
- Collectors that are unavailable on a platform (e.g. Linux monitor EDID) yield empty sections, not failures.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST let an authorized operator mint a tenant-scoped enrollment token with an expiry, and MUST let an agent exchange a valid, unused, unexpired token for a persistent per-agent credential bound to that tenant.
- **FR-002**: System MUST accept authenticated snapshot submissions from enrolled agents over a dedicated ingest edge that is separate from the mesh query API, and MUST reject unauthenticated or unenrolled submissions.
- **FR-003**: System MUST resolve each submission to a stable host identity using hardware UUID, then machine id, then hostname, creating the host on first sighting and updating it thereafter, always scoped to the agent's tenant.
- **FR-004**: System MUST store every submission as an immutable, time-stamped snapshot retaining the full inventory payload, and MUST update the host summary (last-seen, OS, manufacturer/model, agent version, latest snapshot).
- **FR-005**: System MUST capture hardware inventory: BIOS, system, baseboard, chassis, processors, cache, memory (array + modules), ports, slots, OEM strings, BIOS language, and monitors.
- **FR-006**: System MUST capture software/OS inventory: OS details, installed programs, services/daemons, user accounts, patches/updates, and environment (domain, timezone, locale).
- **FR-007**: System MUST capture network/storage inventory: network interfaces (MAC, addresses, DNS, DHCP, speed, state) and disks with partitions.
- **FR-008**: Users MUST be able to list hosts filtered by hostname, OS, manufacturer, status, tag, last-seen range and agent-online, with pagination, and to open a host to view its latest full inventory.
- **FR-009**: System MUST record change history between consecutive snapshots (added/removed/modified components per category) and MUST provide a diff between any two snapshots of a host.
- **FR-010**: Users MUST be able to list a host's snapshot history and retrieve any snapshot or the latest snapshot.
- **FR-011**: System MUST let an authorized operator trigger an on-demand refresh of a specific host; the command MUST be delivered to the target agent's active command stream if connected, and the operator MUST be told whether it was delivered.
- **FR-012**: System MUST maintain agent connection/refresh state in shared storage so that connected-agent listing and refresh delivery work across multiple server instances.
- **FR-013**: System MUST publish agent-online/offline and snapshot-received events to the platform event bus so the UI updates live.
- **FR-014**: Agents MUST support one-shot and daemon/service modes, submit once at startup and on a configurable interval with retry/backoff, and reconnect their command stream with exponential backoff; agents MUST be installable/uninstallable as a Windows service or Linux systemd unit.
- **FR-015**: Users MUST be able to delete a snapshot and to delete/retire a host, and to tag/move hosts.
- **FR-016**: System MUST purge snapshots older than a configurable retention window without removing hosts or their latest snapshot.
- **FR-017**: System MUST provide per-tenant statistics (host counts by status/OS/manufacturer/model, hardware and software rollups, agents online/offline, stale hosts, snapshots per day, recent activity) and a per-tenant breakdown for administrators.
- **FR-018**: System MUST support per-tenant export and import of hosts, snapshots (latest or full history), tags and change history, with schema versioning and skip/overwrite handling.
- **FR-019**: System MUST register its routes, API permissions, UI abilities and navigation with the application gateway and expose service-to-service APIs for other modules.
- **FR-020**: System MUST enforce API permissions (inventory read/write, host management, agent management, snapshot management, statistics read, backup management), and MUST restrict enrollment-token minting to operators/administrators.
- **FR-021**: System MUST record an append-only audit entry for every ingest, enrollment, refresh command, deletion and administrative action.
- **FR-022**: System MUST provide a UI with a hosts list (filters), a host detail view (hardware, software, network/disks, snapshot history + diff tabs), an agents view (online/offline, refresh, issue enrollment token) and a statistics dashboard.

### Security Requirements *(mandatory — Constitution: Development Workflow)*

- **Trust boundaries crossed**: public/off-mesh agent ingest edge (untrusted endpoints); browser API via the application gateway; service-to-service mesh gRPC; shared event bus and connection registry.
- **Data classification**: asset inventory including hardware serial numbers, machine identifiers, logged-in user names and installed software (sensitive, tenant-confidential); agent/enrollment credentials (secret).
- **Authentication/Authorization**: agents authenticate to the ingest edge with per-agent credentials obtained via a tenant-scoped enrollment token; browser callers use the gateway platform token; module callers use SPIFFE mTLS; API permissions gate every operation; RLS isolates tenants.
- **Threat scenarios**: reuse or theft of an enrollment token; a forged/spoofed agent injecting snapshots for another tenant or host; oversized/malformed snapshot DoS; cross-tenant leakage via the shared connection registry or event bus; leakage of serial numbers/user names/credentials in logs, responses or backups.
- **SR-001**: Enrollment tokens MUST be tenant-scoped, single-use and expiring; a used or expired token MUST be refused.
- **SR-002**: Per-agent and enrollment credentials MUST be sealed at rest (envelope encryption) and MUST never be returned in any response, log, audit entry or backup.
- **SR-003**: The ingest edge MUST bind every accepted submission to the enrolled agent's tenant and host scope; an agent MUST NOT be able to write hosts or snapshots outside its tenant.
- **SR-004**: All host, snapshot and change data MUST be isolated per tenant by row-level security; the shared connection registry and event bus MUST NOT leak across tenants.
- **SR-005**: The ingest listener MUST be network-isolated from the mesh query/admin API and MUST bound submission size, rejecting oversized or malformed payloads without partial writes.
- **SR-006**: All operations MUST be recorded in an append-only, tamper-evident audit trail with actor (agent identity or platform user), tenant, outcome and reason.

### Key Entities *(include if feature involves data)*

- **Host (Asset)**: a managed endpoint in a tenant; identity (hardware UUID, machine id, hostname), summary (manufacturer, model, OS, agent version, status active/stale/retired), first/last seen, tags, assigned user, latest snapshot reference.
- **Snapshot**: an immutable, time-stamped inventory report bound to a host; collected/received time, agent version, source (agent/manual/import), and the full hardware + software + network payload.
- **Hardware components**: BIOS, system, baseboard, chassis, processors, cache, memory array and modules, ports, slots, OEM strings, BIOS language, monitors — queryable sub-entities of a snapshot.
- **Software/OS components**: OS, installed programs, services, user accounts, patches, environment — queryable sub-entities of a snapshot.
- **Network/storage components**: network interfaces and disks/partitions — queryable sub-entities of a snapshot.
- **Change record**: the set of added/removed/modified components computed between two consecutive snapshots of a host.
- **Agent**: the enrolled endpoint client (one per host); its identity, tenant, credential (sealed), version, and live connection status.
- **Enrollment token**: a tenant-scoped, single-use, expiring secret minted by an operator to enroll a new agent.

## Success Criteria *(mandatory)*

- **SC-001**: A newly installed, enrolled agent's first snapshot appears as a host in the operator's list within 1 minute of submission.
- **SC-002**: An operator can find a specific host by hostname, OS or tag among 10,000 hosts and open its full inventory in under 3 seconds.
- **SC-003**: 100% of accepted snapshots are attributable to exactly one tenant and one host; no submission is ever stored under another tenant.
- **SC-004**: A diff between two snapshots correctly reports every added, removed and changed component for the covered categories in 100% of test cases.
- **SC-005**: An on-demand refresh reaches a connected agent and yields a new snapshot within 30 seconds, including when the agent is connected to a different server instance than the one handling the request.
- **SC-006**: Agent/enrollment credentials, and no host serial numbers or user names beyond what the viewer is authorized to see, ever appear in logs or backups (verified by inspection).
- **SC-007**: The system sustains at least 5,000 hosts each reporting on a regular interval without loss of snapshots.
- **SC-008**: Stale hosts (no snapshot within the configured window) are reflected in statistics within one evaluation interval.
- **SC-009**: A tenant export re-imports to an equivalent state (hosts, latest snapshots, tags, change history) with no cross-tenant leakage.

## Assumptions

- Endpoints run Windows or Linux; the agent gathers what each platform exposes and leaves unavailable categories empty rather than failing.
- Host identity prefers the SMBIOS hardware UUID; where absent or non-unique it falls back to a stable OS machine id, then hostname.
- Snapshots are immutable and retained as a time series; "current state" is the latest snapshot per host.
- Enrollment is operator-driven (an operator issues a token per endpoint or batch); agents are untrusted until enrolled.
- The platform provides tenant identity, the application gateway, the event bus, the audit trail, certificate/identity issuance and sealed-secret storage; this feature consumes them.
- Reasonable defaults apply for collection interval, stale threshold and retention window; they are configurable.

## Out of Scope

- Remote control or software deployment to endpoints (inventory is read-only collection plus refresh; no remediation/patching actions).
- Real-time streaming of metrics/telemetry (this is periodic inventory, not monitoring).
- Agent auto-update/software distribution beyond initial install and standard packaging.
- Discovery of unmanaged devices via network scanning (only enrolled agents report).
