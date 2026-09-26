# go-tangra-inventory

IT asset inventory service for the
[go-tangra v4 platform](https://github.com/go-tangra/go-tangra).

Endpoint agents collect hardware (SMBIOS/DMI), software/OS, network and storage
inventory and report immutable snapshots to the service. The service resolves each
snapshot to a stable host identity (hardware UUID, then machine id, then hostname),
keeps the full time-series history in TimescaleDB, records what changed between
snapshots, and serves query, diff, statistics, on-demand refresh and backup
export/import over the gateway and service-to-service gRPC, plus a federated UI
remote. Agent and enrollment credentials are sealed with envelope encryption, and
every tenant is isolated by PostgreSQL row-level security.

Operations: [`deploy/README.md`](deploy/README.md).
Design history: `specs/010-inventory-service`.

## Place in the platform

```
go-tangra/go-tangra          platform module + @go-tangra/ui kit
        |
go-tangra-auth  <---->  go-tangra-portal (gateway)  <---->  go-tangra-inventory  <----  inventory-agent
                                    |                              ^                   (off-mesh, ingest edge)
                              go-tangra-lcm (mesh SVID)       go-tangra-asset (inventory sdk)
```

- Built on `github.com/go-tangra/go-tangra/v4` (mTLS transports, identity,
  service policy, audit, observability).
- Verifies platform tokens and registers its permissions with the auth SDK
  (`github.com/go-tangra/go-tangra-auth/sdk/v4`).
- Registers routes, permissions, abilities and navigation with the gateway through
  the portal SDK (`github.com/go-tangra/go-tangra-portal/sdk/v4`).
- Enrolls for its own mesh SVID over lcm (`github.com/go-tangra/go-tangra-lcm/sdk/v4`).

## Two trust planes

| Listener | Default | Who calls it |
|---|---|---|
| mesh gRPC (`inventory.v1`) / HTTP | `:9975` / `:9976` | the gateway (browser API under `/api/inventory`) and other services, SPIFFE mTLS |
| ingest edge | `:9977` | untrusted off-mesh agents, authenticated by a per-agent credential |
| admin (health, readiness, metrics) | `127.0.0.1:9810` | the container runtime |

An agent exchanges a tenant-scoped, single-use, expiring enrollment token for its
per-agent credential, then submits snapshots and holds a command stream for refresh.

## Modules in this repository

| Module | Path | Consumers |
|---|---|---|
| `github.com/go-tangra/go-tangra-inventory/v4` | `/` | the service (`cmd/inventorysvc`), the agent (`cmd/inventory-agent`) and `pkg/inventorymanifest` |
| `github.com/go-tangra/go-tangra-inventory/sdk/v4` | `sdk/` | other services (asset, ipam): the `inventory.v1` protobuf API and `pkg/inventoryclient` |

The service builds against the in-repo SDK through
`replace github.com/go-tangra/go-tangra-inventory/sdk/v4 => ./sdk`. Consumers use the
SDK's published `sdk/vX.Y.Z` tag.

## Layout

| Path | Purpose |
|------|---------|
| `cmd/inventorysvc` | service binary (serve, `bootstrap`, `version`) |
| `cmd/inventory-agent` | endpoint agent for Linux and Windows (one-shot, daemon, Windows service; run under systemd on Linux) |
| `internal/app` | wiring: config, platform, stores, services, HTTP/gRPC, ingest edge |
| `internal/...` | hosts, snapshots, diff, enrollment, ingest, registry, streams, sealing, authz, audit, events, stats, backup and their SQL bindings; agent-side collector, sender, daemon and Windows service |
| `internal/agentfacts` | pure, fuzzed parsers behind the agent's host report collection (netlink, sysfs, Windows adapters, virtualization, Proxmox, package managers, BMC LAN parameters) |
| `internal/hostreport` | the host report projection and digest served to IPAM |
| `ui` | Vue 3 + FlyonUI federated remote on `@go-tangra/ui` |
| `api/openapi`, `sdk/api/proto` | contracts (`inventory.yaml`, `inventory.v1`) |
| `deploy` | operations guide, service policy and the development KEK (never copied into the image) |

## Build and test

You need Go 1.26, Node 22, Docker (for integration tests and the image), and a
GitHub token with `read:packages` to install `@go-tangra/ui` from GitHub Packages.

```bash
go build ./... && go vet ./... && go test -race ./...
(cd sdk && go vet ./... && go test -race ./...)
make proto-check                          # buf lint + buf breaking against sdk/v4.0.0
make test-integration                     # -tags integration, needs Docker
make lint cover vuln
make fuzz                                 # every Fuzz* target, FUZZTIME=10s each

cd ui
export NODE_AUTH_TOKEN=$(gh auth token)   # ui/.npmrc only references this variable
npm ci && npm run lint && npm run test:unit && npm run build
```

The unit coverage gate requires at least 80 % overall and 100 % for the
authorization, sealing, enrollment and host report projection packages. Generated code, SQL bindings,
wiring and the agent's platform collectors are covered by the integration suite
or excluded on purpose.

## The agent

`inventory-agent` is not part of the container image. Build it for the hosts you
manage:

```bash
make agent                                # bin/inventory-agent-{linux,windows}-{amd64,arm64}[.exe]
inventory-agent -ingest <host:9977> -token <token-file>        # one-shot enroll + collect + submit
inventory-agent -config agent.yaml -daemon                      # periodic submit + refresh stream
inventory-agent -o ./out                                        # collect to JSON, no submit
inventory-agent -service install                                # Windows only: installs the Windows service
```

`-service install` exists on Windows only; it does not create a systemd unit.
On Linux install the `tangra-inventory-agent` .deb/.rpm attached to each
release (systemd unit included; `make packages` builds them), see
[deploy/README.md](deploy/README.md#installing-the-agent-from-a-package), or
run the daemon under systemd yourself
([example](deploy/README.md#running-the-agent-under-systemd)).

Besides hardware, software and disks the agent reports what IPAM needs to keep
its devices current: per-interface kind, speed, bond/bridge master, VLAN id,
addresses with prefix and DHCP/temporary/deprecated flags, gateway and default
route, the primary IPv4/IPv6 address, the virtualization role, the BMC LAN
settings (Linux, read-only, never credentials), Proxmox guests (Linux) and the
package update state (Linux: pending and security updates, reboot required,
automatic updates). Every list is bounded and what is dropped is counted.
`collect_bmc`, `collect_updates`, `refresh_package_lists` (off: the agent never
refreshes package lists unless told to) and `update_timeout_seconds` tune it;
see [deploy/README.md](deploy/README.md#host-report-collection).

The ingest edge serves TLS from `ingest.tls_cert_file`/`ingest.tls_key_file`
(hot-reloaded). Only the development opt-out `ingest.insecure: true` serves
plaintext, and it is refused with `env: production`. The agent verifies the
server certificate against the system roots, or only against `ca_file`
(`-ca-file`) for a private CA; `server_name` (`-server-name`) overrides the
verified name. See [deploy/README.md](deploy/README.md#ingest-tls).

CI cross-compiles the agent for every supported platform on each change. It does
not upload or release agent binaries.

## Host reports for IPAM

`inventory.v1.HostReportService` (mesh only, not proxied by the gateway) serves a
slim projection of each host's latest snapshot: identity, interfaces and
addresses, primary addresses, virtualization, BMC, guests, update state and only
the packages with a pending update. Each host keeps a digest of its projection
and the time it last changed, so a consumer polls "tenants changed since",
"host reports changed since" (full or digest view, paged) and "one host report"
without re-reading unchanged hosts. The IPAM host sync is its consumer
(`pkg/inventoryclient`: `ListReportTenants`, `ListHostReports`, `GetHostReport`).

Access needs both the inbound policy rule `ipam-hostsync` in
`deploy/policy.yaml` (the three RPCs and health, for `svc/ipam` only) and the
caller's service name in `host_reports.consumers` (default `[ipam]`), which the
handler checks for every RPC.

## Container image

The image is `ghcr.io/go-tangra/go-tangra-inventory`, built by
`.github/workflows/ci.yaml`. It carries `inventorysvc` with the embedded UI remote.

```bash
docker buildx build --secret id=npm_token,env=NODE_AUTH_TOKEN \
  --build-arg APP_VERSION=4.0.0 -t go-tangra-inventory:dev .
docker run --rm go-tangra-inventory:dev version
```

The image runs `inventorysvc -config deploy/container.yaml` as user `app`
(uid 10001). It ships `deploy/policy.yaml` but no configuration and no key
material: the deployment mounts `deploy/container.yaml` and the key-encryption key
(the go-tangra platform stack mounts its dev KEK at `/app/deploy/kek.dev`) and
publishes the ingest edge port.

## API permissions and module roles

`inventory:read/write`, `hosts:manage`, `agents:manage`,
`snapshots:read/manage`, `stats:read`, `backup:manage`. The gateway enforces the
per-route permission from the manifest; the module checks it again.

The module registers its permissions, its module roles and the built-in role
grants (`pkg/inventorymanifest.Grants`, scoped to inventory by auth) with auth
at start and every five minutes (`pkg/inventorymanifest.Registration`). Module
roles are provided in every tenant; administrators assign them or clone them
into custom roles:

| Role | Display name | Permissions |
|---|---|---|
| `administrator` | Inventory administrator | all inventory permissions |
| `editor` | Inventory editor | `inventory:read`, `inventory:write`, `snapshots:read` |
| `viewer` | Inventory viewer | `inventory:read`, `snapshots:read`, `stats:read` |

## Versioning

- Service releases are tagged `vX.Y.Z`. CI publishes the image as `X.Y.Z`,
  `X.Y`, `X` and `sha-<short>`. There is no `latest` tag.
- The SDK is released separately with `sdk/vX.Y.Z` tags. These tags never build an image.
- v4.0.0 rebuilds the service on the go-tangra v4 platform. The previous line stays on
  the `v3` branch and its existing `v1.x` tags.
