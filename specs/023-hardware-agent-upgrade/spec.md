# Feature Specification: Hardware Details for Devices and Agent Self-Upgrade

**Feature Branch**: `023-hardware-agent-upgrade`

**Created**: 2026-09-27

**Status**: Draft

**Spans**: go-tangra-inventory-v4 (agent, inventory service, SDK, UI) and go-tangra-ipam-v4 (device hardware view)

**Input**: User description: "I'd like to enrich the collected/displayed data for the devices. Check the v3 version — most important is BIOS information, memory, CPU, memory type, disks." and "If this involves updating the agent itself, add a self-upgrade option like v3."

## Context

In v3 the host client reported, and IPAM displayed per device: BIOS vendor,
version and date; board and system vendor and serial; chassis type; CPU model
and count; total memory with its type (DDR4/DDR5) and speed; and the disks
(name, SSD/HDD, model, size).

The v4 inventory agent already collects most of this (BIOS, system, chassis,
processors, every memory module, memory array), but a production check on
2026-09-27 (host node-1, 2× Xeon Silver 4310, 16× 16 GB Samsung RDIMM) found:

- **Wrong decoded values**: memory type "LPDDR3" for DDR4 modules, form factor
  "TSOP" for DIMMs, memory array "Video memory" on an "ISA add-on card" instead
  of "System memory" on the "System board", "Multi-bit ECC" instead of
  "Single-bit ECC" — every value decoded from the hardware (SMBIOS) code tables
  is shifted by one entry.
- **No physical disks**: only mounted filesystems (mount, type, size, free) are
  reported — no disk model, serial, capacity, SSD/HDD/NVMe or interface.
- **Nothing reaches IPAM**: the host report IPAM pulls (feature 020) carries
  identity, network, BMC, virtualization, guests and updates, but no hardware,
  so IPAM device pages show none of it.

Fixing collection requires a new agent on every host. v3 could upgrade its
client in place (manually with an `update` command, or on request from the
server; downloaded with a checksum and swapped atomically with rollback); v4
agents can only be upgraded by reinstalling the package by hand on every host.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Correct hardware facts from the agent (Priority: P1)

An administrator opens a server in the inventory: BIOS "American Megatrends
2.5 (2025-11-26)", system "Supermicro Super Server" with serial, chassis
"Rack mount chassis", 2 processors "Xeon Silver 4310, 12 cores / 24 threads",
512 GB memory in 16 of 16 slots, each "16 GB DDR4 RDIMM 3200 MT/s (configured
2666) Samsung M393A2K43EB3", ECC "Single-bit ECC".

**Why this priority**: Wrong values are worse than none; every downstream view
(inventory, IPAM, asset) depends on correct collection.

**Independent Test**: Decoding known SMBIOS code values for memory type, form
factor, array location/use, ECC, chassis type and processor fields yields the
names defined by the SMBIOS specification; a host report from node-1-like data
shows DDR4/DIMM/System memory/System board/Single-bit ECC.

**Acceptance Scenarios**:

1. **Given** a host with DDR4 RDIMMs, **When** the new agent reports, **Then**
   each module shows type DDR4, form factor DIMM (registered/buffered detail
   where the hardware exposes it), size, rated and configured speed,
   manufacturer, part number, serial and slot.
2. **Given** the memory array, **Then** location, use and error correction
   show the correct SMBIOS names; empty slots are counted (populated / total).
3. **Given** any SMBIOS enumerated value the agent does not recognise, **Then**
   it is shown as "Unknown (code N)", never a neighbouring name.
4. **Given** a virtual machine without meaningful SMBIOS data, **Then** the
   hardware section shows what exists and marks the rest as not available,
   without errors.

---

### User Story 2 - Physical disks are reported (Priority: P1)

The administrator sees each physical disk of a server: "Samsung PM9A3
3.84 TB, NVMe SSD, serial S64…", "Seagate ST4000NM 4 TB, SATA HDD", next to the
filesystems with their usage.

**Why this priority**: Disks were one of the four items the user named; v4
reports none.

**Independent Test**: With a simulated Linux block-device tree (NVMe, SATA SSD,
SATA HDD, a virtual disk, a USB stick, device-mapper/loop devices), the agent
reports the three physical disks with model, serial, size, media type and
interface, excludes loop/ram/device-mapper devices, and flags the USB disk as
removable.

**Acceptance Scenarios**:

1. **Given** a Linux host, **Then** every physical block device is reported with
   model, serial (when readable), capacity, media type (SSD / HDD / NVMe SSD /
   unknown) and interface (NVMe, SATA, SAS, USB, virtio, …).
2. **Given** a Windows host, **Then** the same fields are reported for its
   physical disks.
3. **Given** partitions/filesystems, **Then** they stay reported as today and
   are shown under the disk they belong to when that is known.
4. **Given** software RAID, LVM or device-mapper volumes, **Then** they are not
   reported as physical disks (the underlying disks are).

---

### User Story 3 - Hardware shown on IPAM devices (Priority: P1)

In IPAM, the device page of a host-reported server gets a **Hardware** tab with
BIOS, system/board/chassis, processors, memory (total, modules per slot, empty
slots, ECC) and disks (with filesystem usage) — the same data v3 showed.

**Why this priority**: The user asked for the data to be displayed for devices;
IPAM is where administrators look at devices.

**Independent Test**: A host report carrying hardware data updates the IPAM
device; the Hardware tab shows it; changes (e.g. a replaced disk, a BIOS
update) appear after the next report and are audited.

**Acceptance Scenarios**:

1. **Given** a host whose agent reports hardware, **When** IPAM syncs it,
   **Then** the device's Hardware tab shows BIOS, system, chassis, processors,
   memory and disks, and the device summary shows CPU, memory total and disk
   total at a glance.
2. **Given** a BIOS update, a memory change or a replaced disk, **When** the
   next report arrives, **Then** the Hardware tab reflects it and the change is
   audited (what changed, old and new value).
3. **Given** a device not reported by an agent, **Then** no Hardware tab is
   shown (or an explanatory empty state).
4. **Given** an older inventory without hardware in the report, **Then** IPAM
   keeps working and shows no hardware (compatibility).

---

### User Story 4 - Upgrade agents from the platform (Priority: P1)

Before rolling out the fixed agent, the administrator opens the inventory's
agent list, sees each agent's version and whether an upgrade is available,
and clicks "Upgrade" on one host (to try it) and then "Upgrade all". Each agent
downloads the new version from the platform, verifies it, installs it,
restarts, and reports again with the new version within minutes. A host whose
upgrade fails keeps running the old version and shows the failure reason.

**Why this priority**: Without it, the collection fixes above require manual
reinstallation on every host; v3 had self-upgrade.

**Independent Test**: An agent on an older version receives an upgrade request,
downloads the platform-provided release for its OS/architecture, verifies it,
installs it and reports the new version; a tampered or wrong-platform artifact
is refused and the old agent keeps running.

**Acceptance Scenarios**:

1. **Given** agents on older versions, **When** the administrator requests an
   upgrade for one host, a selection or all, **Then** each targeted online
   agent upgrades and reports the new version; offline agents upgrade when
   they next connect (the request stays pending, with an expiry).
2. **Given** a downloaded artifact whose checksum or signature does not match,
   **Then** the agent refuses it, keeps the current version, and reports the
   failure reason.
3. **Given** a new version that fails to start or cannot report within a
   bounded time, **Then** the host returns to the previous version
   automatically and reports the rollback.
4. **Given** a host where an administrator runs the agent's `update` command
   (optionally "check only"), **Then** it checks the platform for a newer
   version and upgrades the same way (v3 parity).
5. **Given** the upgrade source, **Then** agents obtain releases only from the
   platform they are enrolled with (not directly from the internet), over the
   same authenticated channel they already use.
6. Every upgrade request, success, failure and rollback is audited with host,
   versions and actor.

---

### User Story 5 - Upgrade policy (Priority: P2)

The administrator can choose, per tenant, to upgrade agents automatically to
the platform's current agent version (off by default), with a maintenance
window and a maximum number of concurrent upgrades.

**Why this priority**: Convenience at scale; manual upgrade (US4) already
covers the need.

**Independent Test**: With auto-upgrade on and a window of 02:00–04:00, agents
upgrade only inside the window, never more than the configured number at once.

**Acceptance Scenarios**:

1. **Given** auto-upgrade off (default), **Then** agents are never upgraded
   without an explicit request.
2. **Given** auto-upgrade on with a window and a concurrency limit, **Then**
   outdated agents are upgraded inside the window, at most N at a time, and a
   failing version pauses further automatic upgrades until an administrator
   resumes them.

### Edge Cases

- Mixed fleet: Linux amd64/arm64 (deb and rpm installs) and Windows
  amd64/arm64; each agent receives only the artifact for its platform and
  install type.
- An agent older than this feature cannot understand the upgrade request; it is
  shown as "manual upgrade required" (one last manual install).
- Disk full or no write permission on the host: the upgrade fails cleanly, the
  old agent keeps running, and the reason is reported.
- Two upgrade requests for the same host: only one runs; duplicates are
  ignored.
- The platform does not yet have the requested release cached: the request
  waits until it is available, or fails with a clear reason.
- SMBIOS data partially readable without full privileges: collected fields are
  reported, missing ones marked not available.
- Hosts with dozens of disks (storage servers) and hundreds of partitions: the
  report stays within the existing bounds (truncation is flagged).
- Removable media (USB sticks) are reported as removable, not as server disks
  in summaries.

## Requirements *(mandatory)*

### Functional Requirements

**Hardware collection (agent)**

- **FR-001**: Values decoded from SMBIOS enumerations (memory type, form factor,
  type detail, array location/use/error correction, chassis type, processor
  family/type/upgrade, system wake-up type, …) MUST use the names defined by the
  SMBIOS specification for each code; unknown codes MUST be reported as
  "Unknown (code N)".
- **FR-002**: The agent MUST report memory: total, array (location, use, error
  correction, maximum capacity, slot count) and every slot — populated or empty
  — with locator, size, type, type detail (e.g. registered), form factor, rated
  and configured speed, manufacturer, part number and serial.
- **FR-003**: The agent MUST report BIOS (vendor, version, release date),
  system (manufacturer, product, version, serial, UUID, SKU, family), board
  (manufacturer, product, serial) and chassis (type, manufacturer, serial,
  asset tag), and processors (model, sockets, cores, threads, speeds).
- **FR-004**: The agent MUST report physical disks on Linux and Windows: model,
  serial (when readable), capacity, media type (SSD, HDD, NVMe SSD, unknown),
  interface (NVMe, SATA, SAS, USB, virtio, SCSI, other), removable flag;
  loop, ram, zram, device-mapper, md and similar virtual block devices MUST
  NOT be reported as physical disks. Filesystems MUST continue to be reported
  and SHOULD reference their disk when known.
- **FR-005**: Collection MUST stay bounded in time and size (existing caps
  apply; new caps: ≤ 256 disks, ≤ 1024 memory slots) and flag truncation.

**Hardware in the host report and IPAM**

- **FR-006**: The host report that IPAM pulls MUST gain an optional hardware
  section (BIOS, system, board, chassis, processors summary and list, memory
  total/array/modules, disks with filesystems) — additive, so older consumers
  and older inventory versions keep working; hardware changes MUST change the
  report digest so IPAM re-applies the host.
- **FR-007**: IPAM MUST store the reported hardware per host-reported device and
  show a Hardware tab (BIOS, system/board/chassis, processors, memory with
  slots, disks with filesystem usage) and a summary line (CPU, memory total,
  disk total) on the device page.
- **FR-008**: Hardware on IPAM devices is reported data: it MUST be replaced by
  each report, never edited by users, and changes MUST be audited field by
  field (e.g. BIOS version, memory total, disk added/removed) with old and new
  values.
- **FR-009**: The inventory host page MUST show the corrected hardware
  including physical disks.

**Agent self-upgrade**

- **FR-010**: The inventory module MUST make agent releases available to its
  agents for every supported OS/architecture/install type, each with a
  SHA-256 checksum and a signature verifiable with a public key built into the
  agent.
- **FR-011**: Administrators with agent-management permission MUST be able to
  request an upgrade for one agent, a selection, or all outdated agents of the
  tenant, to the platform's current agent version; requests to offline agents
  MUST stay pending until they connect or expire (default 7 days).
- **FR-012**: The agent MUST accept an upgrade request only over its existing
  authenticated connection to the inventory module, download the artifact for
  its own platform from the same module, verify checksum and signature, and
  refuse anything that does not verify.
- **FR-013**: The agent MUST install the new version, restart itself, and roll
  back to the previous version automatically if the new version does not start
  and report within a bounded time (default 5 minutes).
- **FR-014**: The agent MUST provide an `update` command (with a check-only
  option) that checks the enrolled platform for a newer version and upgrades
  the same way (v3 parity).
- **FR-015**: The agent list MUST show per agent: current version, target
  version, upgrade state (up to date, available, pending, in progress, failed
  with reason, rolled back, manual upgrade required) and last change time.
- **FR-016**: Upgrade requests, results and rollbacks MUST be audited (actor,
  host, from/to version, outcome, reason).
- **FR-017**: [NEEDS CLARIFICATION: How should agents installed from the deb/rpm
  packages upgrade — (A) download the matching deb/rpm from the platform and
  install it through the system package manager, keeping the package database
  consistent (recommended), or (B) replace the agent binary in place like v3,
  leaving the installed package version stale?] Windows installs and
  non-package installs replace the binary in place.

**Upgrade policy**

- **FR-018**: Per tenant, administrators MUST be able to enable automatic
  upgrades (default off) with a maintenance window and a maximum number of
  concurrent upgrades; a failed automatic upgrade MUST pause the policy until
  resumed.

### Security Requirements

- **SR-001**: Agents MUST only upgrade to artifacts that verify against the
  checksum and a signature made with the release signing key; the verification
  key is compiled into the agent; the private key never enters the platform
  runtime.
- **SR-002**: Upgrade requests MUST only be delivered over the agent's existing
  authenticated channel; an agent MUST NOT accept an upgrade from any other
  source or location.
- **SR-003**: Downgrades MUST be refused unless explicitly requested by an
  administrator for a listed version (rollback to known-good).
- **SR-004**: The agent MUST write the new version to a private location, verify
  it before replacing anything, and keep the previous version until the new
  one has reported successfully.
- **SR-005**: Hardware data MUST be validated and bounded in inventory and again
  in IPAM before storage; serial numbers are treated as normal (non-secret)
  inventory data.

### Key Entities

- **Hardware profile** (per host, from the latest report): BIOS, system,
  board, chassis, processors, memory (total, array, slots), disks (with
  filesystems), collection flags (truncated, not available).
- **Agent release**: version, OS, architecture, install type, artifact,
  checksum, signature, availability on the platform.
- **Upgrade request**: tenant, host/agent, target version, requested by, state,
  created/expiry, attempts, last error.
- **Upgrade policy** (per tenant): enabled, window, concurrency, paused flag.
- **IPAM device hardware**: the hardware profile stored on a host-reported
  device, with audit of changes.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100 % of SMBIOS enumerated fields in a test corpus of real
  hardware dumps decode to the names in the SMBIOS specification (0 shifted
  values).
- **SC-002**: On a host with NVMe, SATA SSD and SATA HDD disks, 100 % of
  physical disks are reported with capacity and media type, and 0 virtual block
  devices are reported as disks.
- **SC-003**: After the upgrade rollout, the IPAM device page of every
  agent-reported server shows BIOS, CPU, memory (with type) and disks.
- **SC-004**: An administrator upgrades all online agents of a tenant with one
  action; 95 % of online agents report the new version within 10 minutes.
- **SC-005**: 0 agents install an artifact that fails verification (tested with
  tampered, truncated and wrong-platform artifacts); a failed start rolls back
  within 5 minutes in 100 % of tests.
- **SC-006**: No manual host access is needed for any agent upgrade after the
  one-time installation of the first self-upgrading version.

## Assumptions

- The first self-upgrading agent version must still be installed manually once
  (agents older than this feature cannot upgrade themselves); their state is
  shown as "manual upgrade required".
- The inventory module obtains agent releases from the project's own release
  pipeline (the existing GitHub release with deb/rpm/binaries and
  SHA256SUMS), extended with signatures; operators may also provide artifacts
  for offline sites. Distribution to agents is always through the inventory
  module.
- The platform's "current agent version" is the agent version released with
  the running inventory module unless an administrator pins another listed
  version.
- The existing agent-management permission in inventory governs upgrades; a
  new permission is introduced only if planning shows none fits.
- IPAM stays read-only for hardware (the inventory module remains the system of
  record); the asset module may consume the same data later (out of scope).
- SMART health, firmware versions of disks and GPU details are out of scope.

## Dependencies

- Feature 020 (host sync): the host report and IPAM device sync are extended.
- The inventory release pipeline (GitHub release with deb/rpm packages and
  SHA256SUMS) gains signed artifacts.
