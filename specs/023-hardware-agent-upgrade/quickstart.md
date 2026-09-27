# Quickstart: validating hardware details and agent self-upgrade (023)

## Automated

- **inventory** (`go-tangra-inventory-v4`):
  - `make test` — SMBIOS decoder against the raw-table corpus
    (`internal/agentfacts/testdata/smbios/*.bin` + golden JSON: node-1,
    a QEMU VM, a Hyper-V VM, a desktop), sysfs block trees
    (`testdata/sysblock/*`: NVMe + SATA SSD + SATA HDD + virtio + USB +
    dm/md/loop/zram), Windows disk JSON fixtures, ingest validation of the
    new fields, projection with/without hardware and the schema gate,
    diff suppression on the 1 → 2 transition, manifest/keyring/version
    tests, selfupdate state machine with fakes, upgrade service
    transitions, fleet view, policy planner, HTTP handlers and permissions.
  - `make cover` — gate incl. `internal/agentrelease`,
    `internal/selfupdate`, `internal/upgrades` at 100 %.
  - `make fuzz` — `FuzzSMBIOSStructures`, `FuzzSysBlock`,
    `FuzzWindowsDisks`, `FuzzManifest`, `FuzzVersion`,
    `FuzzSubmitMapper` (extended), `FuzzHostReport` (extended).
  - `make test-integration` — migration 0006 on a 0005 database with
    data; two replicas seeding the same bundle (one release row);
    chunked `DownloadAgentRelease` over the real ingest gRPC server
    (bufconn, per-agent credential) incl. foreign-request and cap
    refusals; upgrade transitions under RLS; expiry and stale sweepers.
  - `make proto-check` — `buf lint` + `buf breaking` against `sdk/v4.1.0`.
  - `make e2e-upgrade` (CI job, Docker with systemd) — Debian 12 and
    Rocky 9 containers: install agent package N, serve a dev-signed
    release N+1 from an in-process inventory, upgrade → `dpkg -s` /
    `rpm -q` show N+1 and the agent reports N+1; a release N+1 whose
    binary exits immediately → rollback to N within the confirm timeout,
    package DB shows N; tampered artifact → refused, N keeps running.
  - `make vuln`, agent cross-compile matrix, UI lint/unit/build.
- **ipam** (`go-tangra-ipam-v4`): `make test` (hardware normalisation
  bounds, planner field diff incl. slots/disks added/removed, absent
  hardware leaves the row untouched, audit keys survive the guard),
  `make cover` (100 % `internal/hostreport`, `internal/hostplan`),
  `make fuzz` (`FuzzNormalizeHardware`, `FuzzHardwareDiff` + existing),
  `make test-integration` (0009 on a 0008 database, RLS, apply with
  hardware in one transaction, e2e with an in-process inventory
  `HostReportService` serving hardware), OpenAPI contract test,
  `cd ui && npm run lint && npm run test:unit && npm run build`,
  `make vuln`.

## Manual (freya-stack)

Prerequisites: inventory ≥ 4.4.0 (locally: image built after
`make agent-release-dev` so it bundles a dev-signed release; agents built
with `-tags agentdevkey`), ipam ≥ 4.6.0, an operator signed in with
inventory administrator and IPAM rights, a bare-metal Linux host (node-1
class: 2 sockets, RDIMMs, NVMe + SATA disks), a Linux VM, a Windows host.

1. **US1** — Install agent 4.4.0 on the bare-metal host, refresh. Inventory
   → host: memory "16 GB DDR4 DIMM, Registered (Buffered), 3200 MT/s
   (configured 2666)", array "System board or motherboard / System memory /
   Single-bit ECC", slots 16/16 (on a host with free slots the empty ones
   are listed), chassis "Rack Mount Chassis", processors with family and
   socket. Compare with `dmidecode -t 16,17,3,4`: every value identical.
   The change log shows one `hardware_schema 1 → 2` entry, not dozens of
   memory changes. On the VM: fields without data show "not available",
   no errors.
2. **US2** — Same host: disks table lists the NVMe ("nvme_ssd", NVMe), SATA
   SSD ("ssd", SATA) and HDD ("hdd", SATA) with model, serial, capacity;
   `lsblk -d -o NAME,MODEL,SERIAL,SIZE,ROTA,TRAN` matches; no `dm-*`,
   `md*`, `loop*`, `zram*`. Filesystems list `/` on the LVM volume with
   its underlying disk(s). Plug a USB stick, refresh → shown as removable.
   Windows host: disks from `Get-PhysicalDisk` with media and bus type.
3. **US3** — IPAM → the host's device → Hardware tab shows BIOS, system,
   board, chassis, processors, memory with slots, disks with filesystem
   usage; the summary line shows CPU, memory total/type, disk total.
   Simulate a change (remove a USB disk or update BIOS) and refresh →
   Hardware tab updated; IPAM audit `hardware_updated` lists the changed
   field with old and new value. A device created by hand has no Hardware
   tab; a host still on an old agent shows the explanatory empty state.
4. **US4** — Inventory → Agents: the fleet view lists online and offline
   agents with version and state; an agent < 4.4.0 shows "manual upgrade
   required". With agents on 4.4.0 and inventory carrying 4.4.1 (dev:
   bump `AGENT_VERSION`, `make agent-release-dev`, rebuild the image):
   "Upgrade" on one host → states pending → in progress → up to date
   within minutes; `dpkg -s tangra-inventory-agent` shows 4.4.1;
   `journalctl -u inventory-agent-upgrade-*` shows the helper run.
   "Upgrade all outdated" → all online agents reach 4.4.1 in ≤ 10 min;
   an offline agent stays "pending" and upgrades when started.
   Negative: corrupt the stored artifact chunk in the DB (lab only) →
   agent reports "failed: checksum_mismatch", old version keeps running.
   Rollback: import a dev release whose agent exits at start → host
   returns to the previous version ≤ 5 min, state "rolled back:
   start_timeout". On the host: `inventory-agent update -check` prints
   current/available and exits 10; `inventory-agent update` upgrades.
   Audit lists requested/delivered/started/succeeded/failed/rolled_back
   with actor and versions.
5. **US5** — As operator: policy card read-only, PUT policy → 403. As
   administrator: enable auto-upgrade with a window covering the next
   minutes and max concurrent 1 → agents upgrade one at a time only inside
   the window; make one upgrade fail → policy shows "paused"; resume.
   Disable → nothing upgrades without an explicit request.
6. **Rollout check (SC-003)** — after all agents are ≥ 4.4.0 and ipam 4.6.0
   is deployed, every agent-reported server in IPAM has a Hardware tab
   with BIOS, CPU, memory (with type) and disks.
