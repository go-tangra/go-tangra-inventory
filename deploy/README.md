# Inventory service — operations

The **inventory** service is a tenant-scoped IT **asset inventory** platform
module. Endpoint **agents** collect hardware (SMBIOS/DMI), software/OS and
network/storage inventory and report immutable **snapshots** to the central
inventory **server**, which resolves each to a stable **host** identity, keeps
the full time-series history, records what changed between snapshots, and serves
query/diff/statistics/backup over the gateway and service-to-service gRPC, plus a
Module-Federation UI.

Vocabulary note (vs the source project): here the **server** is the `inventory`
module (`inventorysvc`) and the endpoint client is the **agent**
(`inventory-agent`) — one agent per host.

## Two trust planes

- **Query/admin API** — an ordinary Freya mesh module: SPIFFE mTLS
  service-to-service, platform token via the application gateway for the browser
  (`/api/inventory`), gateway-registered (routes/permissions/abilities/nav).
- **Ingest edge** — a separate, network-isolated listener that authenticates
  untrusted off-mesh agents by a per-agent credential obtained through a
  tenant-scoped enrollment token. No shared secrets.

## Running the server

```
inventorysvc -config deploy/container.yaml     # run (applies migrations)
inventorysvc bootstrap -config <cfg>           # apply migrations and exit
```

In the containerized platform stack it comes up with one command; see
`deploy/stack/README.md`. The server:

- enrolls for its mesh SVID (`spiffe://<td>/svc/inventory`) over lcm,
- migrates its TimescaleDB schema (per-tenant RLS; snapshots are a hypertable),
- serves the browser API (via the gateway) and `inventory.v1` gRPC on `:9975`,
  the off-mesh **ingest edge** on `:9977`, and admin health/readiness on `:9810`,
- registers routes/permissions/abilities/nav with the gateway and seeds its API
  permissions into auth,
- runs maintenance workers (mark stale hosts, purge snapshots past retention).

## The agent

`inventory-agent` is a cross-platform (Windows/Linux) endpoint binary:

```
inventory-agent -ingest <host:port> -token <enrollment-token>   # one-shot enroll+collect+submit
inventory-agent -ingest <host:port> -auto-enroll-key-id ak_… -auto-enroll-key-file <file>   # same, with an auto-enrollment key
inventory-agent -config agent.yaml -daemon                      # enroll once, submit on an interval, hold a refresh stream
inventory-agent -o ./out                                        # collect and write JSON (no submit)
inventory-agent -service install | uninstall                    # Windows service only
```

`-service install|uninstall` manages the Windows service. It does not create
a systemd unit: on Linux it fails with "not supported"; run the daemon under
systemd as shown below.

On first run the agent exchanges an operator-issued **enrollment token** for a
persistent per-agent credential (stored locally, 0600), then submits full
snapshots over authenticated TLS to the ingest edge and holds a reconnecting
command stream for on-demand **refresh**. It collects: hardware from the raw SMBIOS table
(BIOS/system/board/chassis/CPU/cache/memory arrays and every memory slot/ports/
slots/OEM/BIOS-language, decoded per DSP0134), physical disks (`collect_disks`),
monitors (EDID, Windows), OS/software/services/users/patches, network interfaces
and filesystems, and the host report data described below. Categories unavailable on
a platform yield empty sections, never failures.

### Installing the agent from a package

Every `v*` release carries `tangra-inventory-agent` packages for amd64 and
arm64 (`.deb` and `.rpm`, with `SHA256SUMS`); `make packages` builds the same
into `dist/`. The package installs `/usr/bin/inventory-agent`, the systemd
unit below as `/usr/lib/systemd/system/inventory-agent.service` (enabled), a
sample `/etc/inventory-agent/agent.yaml` (kept on upgrade) and
`/var/lib/inventory-agent` (0700).

```sh
sudo apt install ./tangra-inventory-agent_<version>_amd64.deb   # or: sudo dnf install ./tangra-inventory-agent-<version>-1.x86_64.rpm
sudoedit /etc/inventory-agent/agent.yaml                        # ingest_endpoint (and ca_file if pinned)
sudo install -m 0600 /dev/stdin /etc/inventory-agent/enrollment.token <<< '<token from Inventory > Agents>'
sudo systemctl start inventory-agent
```

The unit only starts once `/etc/inventory-agent/enrollment.token` or the
stored credential `/var/lib/inventory-agent/credential` exists, so an
unconfigured install stays idle instead of restarting in a loop. Upgrades
restart a running agent; `apt purge` also removes the credential and state.

### Automatic enrollment (without a token)

A tenant can let agents enroll without a single-use token (feature 029), e.g.
when go-tangra-client or configuration management installs agents on many
hosts. In **Inventory > Agents > Automatic enrollment** an inventory
administrator (`agents:manage`) turns the tenant switch on and creates
**enrollment keys**. Each key has a name, the networks agents may enroll from
(required; a bare address is one host; `/0` is refused), an optional expiry
and an optional enrollment limit, and can be disabled, edited, rotated or
deleted at any time. Its secret (`aks_…`) is shown once, at creation or
rotation; afterwards only the public key id (`ak_…`) is visible.

```sh
sudo install -m 0600 /dev/stdin /etc/inventory-agent/auto-enroll.key <<< '<secret aks_… from the console>'
sudoedit /etc/inventory-agent/agent.yaml     # auto_enroll: { key_id: ak_…, key_file: /etc/inventory-agent/auto-enroll.key }
sudo systemctl start inventory-agent
# or: inventory-agent -daemon -ingest <host:9977> -auto-enroll-key-id ak_… -auto-enroll-key-file /etc/inventory-agent/auto-enroll.key
```

The agent uses the key only when it has no stored credential and no token
file exists. The key never travels: the request carries
`AutoEnrollProof{key_id, timestamp, nonce, signature}` where signature is
base64url(HMAC-SHA256(secret, canonical message)) over the key id, the unix
timestamp, the nonce and the request identity (hardware UUID, machine id,
hostname) — see `sdk/pkg/autoenroll` (`NewProof`, `Message`, a fixed test
vector) for other implementations such as go-tangra-client. The ingest edge
accepts it only when the tenant switch is on, the key is enabled, not
expired and under its limit, the **caller's address** (the TCP peer of the
ingest connection) is inside the key's networks, the signature matches, the
timestamp is within 5 minutes of the server clock and the nonce was not used
before. Every refusal is the same `PermissionDenied`; the reason
(`address_not_allowed`, `bad_signature`, `stale_timestamp`, `replay`,
`tenant_disabled`, `key_disabled`, `key_expired`, `key_exhausted`,
`rate_limited`, `state_changed`) is audited in the key's tenant
(`auto_enroll_refused`, at most once per key, reason and address every 10 s).
A key accepts at most 60 enrollments per minute per replica, so a leaked
secret cannot mint agents quickly; set an enrollment limit on keys that only
serve a known batch of hosts. Accepted enrollments are audited as `agent_auto_enrolled` with
the key id and address, and the fleet view shows which key enrolled an agent.

Because the address check uses the TCP peer, the ingest port must be reached
directly (Docker's published ports keep the client address); a TCP proxy or
NAT in front of it makes every agent appear with the proxy's address. Key
secrets are sealed with the module KEK and never exported by backups.

### Running the agent under systemd

```ini
# /etc/systemd/system/inventory-agent.service
[Unit]
Description=go-tangra inventory agent
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/local/bin/inventory-agent -config /etc/inventory-agent/agent.yaml -daemon
Restart=on-failure
RestartSec=30
# root reads SMBIOS, /dev/ipmi0 (BMC) and the package managers' state.
User=root
StateDirectory=inventory-agent
NoNewPrivileges=yes
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

`systemctl daemon-reload && systemctl enable --now inventory-agent`. Keep
`credential_file` and `state_file` under `/var/lib/inventory-agent`.

### Host report collection

What the agent reports for the IPAM host sync, per platform:

| Data | Linux | Windows |
|---|---|---|
| interfaces: kind, speed, master, VLAN, addresses with prefix and DHCP/temporary/deprecated flags, gateway, default route | sysfs + rtnetlink (5 s budget) | `GetAdaptersAddresses` |
| primary IPv4/IPv6 | first global, non-temporary address of the lowest-metric default-route interface | same |
| virtualization role and kind | container markers, WSL, Xen, SMBIOS, cpuinfo | SMBIOS |
| BMC LAN settings | OpenIPMI `/dev/ipmi0`, 10 s budget | not collected |
| Proxmox guests (VMID, name, MACs) | `/etc/pve/qemu-server`, `/etc/pve/lxc` | not collected |
| update state, pending and security updates | apt, dnf, yum, apk, pacman (`checkupdates`) | `unknown` |

Bounds: 256 interfaces, 64 addresses per interface, 1000 guests, 32 MACs per
guest, 5000 pending updates, 8 BMC ports; the excess is counted in the
snapshot's `truncated` counters, and the ingest edge enforces the same bounds.

Agent options (`agent.yaml`):

```yaml
collect_bmc: true              # read-only BMC LAN parameters (Linux)
collect_updates: true          # package update state (Linux)
refresh_package_lists: false   # never refresh package lists unless enabled
update_timeout_seconds: 120    # 30-600, whole update collection
```

- **BMC**: needs root and the `ipmi_devintf` and `ipmi_si` kernel modules
  (`modprobe ipmi_devintf ipmi_si`, or load them at boot). The agent never loads
  modules. It reads only IPMI LAN parameters 3 (IP), 4 (IP source), 5 (MAC),
  6 (subnet mask), 12 (default gateway) and 20 (VLAN id): never parameter 16
  (community string) and no user, password, session or cipher command. No
  device, no permission or a timeout means "no BMC".
- **Updates**: the agent reads the package lists the OS keeps fresh (apt/dnf
  timers) and does **not** run `apt update`, `dnf makecache` or `apk update`
  by default. With `refresh_package_lists: true` it refreshes at most once per
  24 h. Every command runs without a shell, with `LANG=C`, a 60 s deadline, and
  the whole collection stops at `update_timeout_seconds`. pacman needs
  `pacman-contrib` (`checkupdates`); without it the state is `unsupported`.

### Hardware and disks

| Data | Linux | Windows |
|---|---|---|
| SMBIOS (BIOS, system, board, chassis, processors, memory arrays and slots) | `/sys/firmware/dmi/tables/DMI` (root), go-smbios fallback | `GetSystemFirmwareTable` via go-smbios |
| physical disks: name, model, serial, size, SSD/HDD/NVMe, interface, removable | `/sys/block` (loop, ram, zram, dm, md excluded) | `Get-PhysicalDisk` / `Win32_DiskDrive` |
| filesystem to disk | `/proc/self/mountinfo` + sysfs holders | partitions to disk number |

Bounds: 256 disks, 1024 memory slots, 64 memory arrays, 256 processors, 1024 filesystems, 16 type-detail flags per module.
Each category reports its availability (`ok`, `partial`, `unavailable`,
`unsupported`, `unknown`) instead of failing the snapshot.

```yaml
collect_disks: true            # physical disks and filesystem-to-disk mapping
```

### Agent self-upgrade

Agents from 4.4.0 on upgrade themselves on request (see the
[README](../README.md#agent-self-upgrade) for the model). Requirements and
knobs:

- **Linux**: systemd (the helper runs as a transient unit
  `inventory-agent-upgrade-<id>` and restarts `inventory-agent.service`);
  deb/rpm installs keep the package database right (`dpkg -i
  --force-confold`, `rpm -U [--oldpackage]`); other installs swap the binary.
  Without systemd the request fails `unsupported_install`.
- **Windows**: the service `FreyaInventoryAgent`; the binary is swapped in place
  and the service restarted by a detached helper.
- **One manual install** of 4.4.0 per host. Older agents show
  `manual_upgrade_required` in the fleet view.
- Staging: `/var/lib/inventory-agent/upgrade` (0700, root; Windows:
  `%ProgramData%\go-tangra\inventory-agent\upgrade`), the two newest versions are kept.

```yaml
upgrade:
  enabled: true                  # false: ignore platform requests (`inventory-agent update` still works)
  confirm_timeout_seconds: 300   # 60-1800: the new version must connect and submit, else rollback
  # staging_dir: /var/lib/inventory-agent/upgrade
```

On the host: `inventory-agent update -check [-config agent.yaml]` (exit 0 up
to date, 10 upgrade available, 1 error, 2 another upgrade running) and
`inventory-agent update` (root/Administrator). Reason codes in the fleet view
and the audit trail: `signature_invalid`, `unknown_key`, `checksum_mismatch`,
`size_mismatch`, `platform_mismatch`, `version_mismatch`, `downgrade_refused`,
`disk_full`, `download_failed`, `install_failed`, `start_timeout`,
`unsupported_install`, `busy`, `package_db_mismatch` (rolled back by binary
restore; reinstall the package), `expired`.

Server side (`agent_releases` in `container.yaml`):

```yaml
agent_releases:
  bundle_dir: /app/agent-releases   # signed releases shipped in the image ("" = none)
  keep_versions: 5                  # 2-50 releases kept in PostgreSQL (~80 MB each); current, pins and active targets always kept
  max_concurrent_downloads: 20      # 1-500 per server instance; one download per agent
  chunk_bytes: 1048576              # 64 KiB-1 MiB storage and download chunk
  request_ttl_hours: 168            # pending/delivered requests expire
  progress_timeout_minutes: 15      # downloading/installing without a report fails
```

At start the server verifies every bundled release with its compiled keyring
(refused bundles are logged and audited `agent_release_imported` refused) and
copies it into PostgreSQL under an advisory lock, so several replicas seed
once. Import a release for an offline site or another version from a verified
directory (`<version>/agent-release.json`, `.sig` and the artifacts):

```sh
inventorysvc agent-release import -config /app/deploy/container.yaml /path/to/4.4.2
```

Automatic upgrades (`GET/PUT /api/inventory/v1/agents/upgrade-policy`,
`POST .../resume`): the scheduler runs every minute per tenant under a
cluster-wide lock, upgrades outdated online agents inside the window (oldest
version first) up to `max_concurrent` (one at a time until an agent runs the
target version), and pauses the policy on the first failed or rolled-back
automatic upgrade (audit `upgrade_policy_paused`).

## Configuration

Server `container.yaml` sections: `db`, `valkey`, `kek` (envelope key),
`ingest` (`addr` — the off-mesh listener; `tls_cert_file`/`tls_key_file`/
`tls_reload_seconds` — its TLS certificate; `insecure` — plaintext, dev only;
see [Ingest TLS](#ingest-tls)), `registry`
(shared agent-connection registry; Valkey-backed when configured, else
in-memory), `retention` (`days`), `stale` (`after_seconds`), `jobs`, `events`,
`gateway`, `enroll` (`token_ttl_seconds` — minted enrollment-token lifetime),
`mesh_enroll` (the server's own SVID enrollment), `limits_inventory`
(`max_request_bytes`, `max_snapshot_bytes`), `agent_releases` (agent
self-upgrade, see [above](#agent-self-upgrade)), and `host_reports`
(`consumers` — mesh service names allowed to call HostReportService, default
`[ipam, asset]` (asset reads the virtualization role for its inventory-sync filter); `max_page_bytes` — bound of one ListHostReports page, default 3 MiB). Framework `server`/`admin`/
`discovery` sections supply the mesh gRPC/HTTP and admin listeners.

## Ingest TLS

The ingest edge serves server-authenticated TLS (minimum TLS 1.2; current
agents negotiate 1.3). Agents authenticate with their per-agent credential in
call metadata, so no client certificate is requested.

```yaml
ingest:
  addr: 0.0.0.0:9977
  tls_cert_file: /app/deploy/ingest/tls.crt   # PEM chain, leaf first
  tls_key_file: /app/deploy/ingest/tls.key
  tls_reload_seconds: 60                      # optional; 0 = 60
```

- The certificate must carry a DNS (or IP) SAN matching the name agents dial.
- The pair is re-read every `tls_reload_seconds`; a renewed certificate needs
  no restart. A pair that fails to load on reload is logged and the current
  certificate stays in use.
- Unless `insecure: true` is set, `tls_cert_file` and `tls_key_file` are
  required, and a missing or unloadable pair refuses start.
- `insecure: true` serves plaintext and is refused with `env: production`. It is
  for the development stack only, and it cannot be combined with the TLS keys.

Agent side (`agent.yaml`, or the matching flags):

```yaml
ingest_endpoint: inventory.example.org:9977
token_file: /etc/inventory-agent/token
credential_file: /var/lib/inventory-agent/credential
ca_file: /etc/inventory-agent/ingest-ca.pem   # optional (-ca-file)
server_name: inventory.example.org            # optional (-server-name)
```

With `insecure: false` (the default) the agent always verifies the server
certificate. Without `ca_file` it uses the operating-system roots. With
`ca_file` it trusts **only** the CAs in that PEM bundle (for a private CA), and
a certificate from any other CA is refused. `server_name` overrides the name
checked against the certificate, which is otherwise the endpoint host. The agent
refuses `insecure` combined with `ca_file` or `server_name`. A plaintext
(`-insecure`) agent cannot talk to a TLS ingest edge.

## Hosts, snapshots, change tracking

A submission is resolved to a host by **hardware UUID → machine id → hostname**
within the agent's tenant (created on first sighting, updated thereafter). Each
submission is stored as an immutable snapshot (hypertable, keyed by host +
collected time) with the full payload plus normalized, queryable component tables
(processors, memory modules, disks, network interfaces, software, services,
monitors). On ingest the server diffs the new snapshot against the host's
previous one and records a **change history** (added/removed/modified per
category); a `diff` API compares any two snapshots.

## Host reports (IPAM)

`inventory.v1.HostReportService` is served on the mesh gRPC listener only (no
gateway route). It exposes, per host, a projection of the latest snapshot
(identity, interfaces, addresses, virtualization, BMC, guests, update state and
the packages with a pending update) with a digest and the time the projection
last changed:

- `ListReportTenants(changed_since)` — tenants with a changed host (cross-tenant);
- `ListHostReports(tenant, changed_since, view full|digest, limit ≤ 200, cursor)` —
  ordered by change time, pages bounded by `host_reports.max_page_bytes`;
- `GetHostReport(tenant, host)`.

A caller needs both the inbound policy rule and its service name in
`host_reports.consumers`. `deploy/policy.yaml` carries the rules for IPAM and asset (`asset-sync`); IPAM:

```yaml
  - id: ipam-hostsync
    from: ["spiffe://example.org/svc/ipam"]
    to: ["inventory"]
    operations: ["/inventory.v1.HostReportService/ListReportTenants",
                 "/inventory.v1.HostReportService/ListHostReports",
                 "/inventory.v1.HostReportService/GetHostReport",
                 "/grpc.health.v1.Health/Check"]
    effect: allow
```

Production stacks copy their own policy file (go-tangra-docker
`policies/inventory.yaml`): add the rule there, with the real trust domain,
when deploying this version. Without it IPAM's host sync reports `degraded` and
writes nothing. Deploy the server before rolling out new agents: an older server
drops the new report fields.

## On-demand refresh & live status

`POST /api/inventory/v1/agents/{host_id}/refresh` delivers a refresh command to
the host's connected agent through the shared registry (Valkey pub/sub, so it
works across horizontally-scaled server instances) and reports whether it was
delivered. agent-online/offline and snapshot-received events publish to the
platform bus (`platform:events:<tenant>`) for the gateway SSE hub, so the UI
updates live.

## Security notes

- Off-mesh agents are untrusted until enrolled; **enrollment tokens** are
  tenant-scoped, single-use and expiring; **auto-enrollment keys** (feature
  029, off by default) are tenant-scoped, network-restricted, optionally
  expiring and limited, and proven with an HMAC that never reveals the key. Per-agent and enrollment credentials
  are **sealed** (envelope encryption + KEK) and never returned in any response,
  log, audit entry or backup.
- The ingest edge binds every accepted submission to the verified agent's
  tenant/host scope — an agent cannot write another tenant's data. It is
  network-isolated from the mesh API and bounds submission size, rejecting
  oversized/malformed payloads with no partial write.
- Per-tenant PostgreSQL row-level security isolates all data; the shared
  connection registry and event bus are tenant-partitioned. Trusted worker paths
  (stale marking, retention purge) run under a scoped system subject.
- Every ingest, enrollment, refresh, deletion and admin action is recorded in the
  append-only, tamper-evident audit trail.
- Agent upgrades: only releases signed with a key compiled into the agent are
  installed (manifest signature, then size and SHA-256 of the streamed
  artifact); the server re-verifies bundles and imports with its own keyring.
  Downloads need the agent's credential and an active request of that agent
  (or its own current version for rollback), are capped per agent and per
  instance and time-bounded. Key custody and rotation: [SECURITY.md](../SECURITY.md).

## Backup

`POST /api/inventory/v1/backup/export` exports the tenant's hosts, their latest
snapshots (or full history when requested), tags and change history, versioned by
schema; agent/enrollment secrets are never included. `POST
/api/inventory/v1/backup/import` recreates them (mode `skip` or `overwrite`),
preserving ids.

## UI

The remote under `services/inventory/ui` is built on the shared kit `@freya/ui` (FlyonUI + Zod,
see `docs/frontend.md`): forms validate through Zod schemas in `src/schemas/`, the
shell provides the theme and shared singletons, and `npm run lint` runs
`check-no-legacy`. Rebuild the image after UI changes; the Dockerfile builds `ui/kit`
first.
