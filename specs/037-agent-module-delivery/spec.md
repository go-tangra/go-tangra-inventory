# Feature Specification: Deliver Module Install Bundles Through the Inventory Agent

**Feature Branch**: `037-agent-module-delivery`
**Created**: 2026-10-10
**Status**: Draft
**Input**: "Add an option to configure and download the module bundle via
inventory-agent. If the agent is already enrolled it can be considered a
trusted source."

Repositories: go-tangra-inventory-v4 (agent, ingest edge, mesh service, SDK
contract) and go-tangra-portal-v4 (gateway catalogue, add-module wizard).
Builds on 033 (certificate delivery: reference-only commands, pull at fetch
time) and on the gateway catalogue 034–036 (verified bundles, join wizard).

## Context

Spec 036 made the add-module wizard produce a ready-to-run zip. The
administrator still downloads it to a laptop, copies it to the target host
and unpacks it there, so a bundle full of secrets travels by hand. Most
target hosts already run an enrolled inventory agent. That agent holds a
per-host credential issued by the platform and keeps an authenticated
command stream to the inventory, so the platform already trusts it to
identify the host. The wizard can deliver the bundle straight to it.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deliver a module to an enrolled host (Priority: P1) 🎯 MVP

On **Modules › Add**, an administrator chooses **Deliver to a host**, searches
the hosts of their tenant, picks one whose agent is online and supports
module delivery, reviews the host inputs (pre-filled from the host's
reported facts) and clicks **Deliver**. The agent receives the rendered
bundle and writes it to its local modules directory
(`/opt/tangra/modules/<module>/`). The wizard shows the progress live:
queued, delivered, written, then (from 036) token used and registered.

**Independent Test**: With a test agent connected, deliver sms-gw. The
directory holds the same files as the downloaded zip (compose, rendered
config, `.env` 0600, `private/` 0600), the inventory item reaches
`installed`, and no secret appears in the inventory database, logs or
audit, or in any command.

### User Story 2 - Start the module on the host (Priority: P2)

A host owner who wants delivery to also start the module configures a local
`modules.deploy_hook` (for example a script that runs
`docker compose up -d` in `MODULE_DIR`). The agent runs it after writing the
bundle and reports `installed` or `hook_failed`. Without a hook the agent
only writes files, and the operator starts the module by hand.

### User Story 3 - Host owner control (Priority: P2)

The host owner can turn delivery off (`modules.enabled: false`), and the
agent then reports `disabled_locally`. The owner also chooses the directory
and the hook. The server never chooses a path, a command or an owner on the
host.

### Edge Cases

- **Agent offline:** the item stays pending and is pushed when the agent
  reconnects, until the join expires.
- **Agent too old, Windows, or delivery disabled on the server:** the host is
  shown as not eligible in the picker and creating a delivery is refused.
- **Module already present on the host** (`<dir>/<module>` exists): the
  agent refuses with `already_installed` and changes nothing. Re-delivering
  would rotate the generated store passwords and break existing data, so a
  reinstall means removing the directory first.
- **A new delivery for the same host and module** supersedes the older
  active item.
- **Failures:** if the gateway cannot render (join expired or unknown, host
  mismatch, auth down), the fetch fails and the item fails after its fetch
  budget is spent. The administrator starts a new delivery.
- **Hostile or oversized bundles:** the agent refuses absolute paths, `..`,
  links, entries outside `<module>/`, more than 300 files, and more than
  16 MiB in total (`invalid_bundle`, `too_large`). It writes nothing.
- **Concurrency:** the agent handles one module item at a time.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The gateway MUST offer `GET /gateway/v1/ops/catalogue/{module}/targets`
  (administrator only). It lists the caller's tenant hosts with hostname, OS,
  agent online, delivery capability, and the IP addresses reported in the
  latest snapshot.
- **FR-002**: The gateway MUST offer `POST /gateway/v1/ops/catalogue/{module}/deliver`
  (administrator only) with `host_id`, `inputs` and `ttl_hours`. It validates
  the inputs as the 036 download does and ensures the allow-list entry. It
  stores a join record with the host and the inputs, which are not secret,
  and asks the inventory to create a delivery item. It returns the join id.
- **FR-003**: Inventory MUST store a delivery item per (host, module) without
  any bundle material and push a `MODULE` command carrying only the item id,
  the module name and the attempt. It MUST replay active items when the agent
  reconnects and supersede older active items for the same host and module.
- **FR-004**: The agent MUST pull the bundle with `FetchModuleBundle(item_id)`.
  Only then does inventory ask the gateway to render it
  (`ModuleBundleSource.RenderModuleBundle`, over the mesh). Inventory relays
  it to the agent and keeps nothing.
- **FR-005**: The gateway MUST render only for a join it created for agent
  delivery that has not expired, whose tenant and host match the request, and
  which has had fewer than 5 renders. It MUST mint the join token at render
  time, with a lifetime that ends when the join expires.
- **FR-006**: The agent MUST validate the archive, write it atomically into
  `<modules.directory>/<module>/` and run only the locally configured hook.
  It reports `installed`, `failed` or `hook_failed` with a reason from a
  closed set.
- **FR-007**: The gateway join progress MUST include the delivery state
  (`pending`, `delivered`, `fetched`, `installed`, `failed`, `hook_failed`,
  `unsupported`, `expired`, `superseded`), the reason and whether the agent
  is online.
- **FR-008**: Every step MUST be audited without secrets:
  - gateway: `module_join_bundle` with `channel: agent` and the host, and
    `module_bundle_rendered`
  - inventory: created, fetched, reported, refused

### Security Requirements

- **SR-001**: Trust the enrolled agent as the host. The agent's own
  credential authenticates the fetch, and inventory hands an item only to the
  agent bound to that item's host. If more than one agent claims a host, the
  item is unsupported.
- **SR-002**: No material at rest or in transit outside the fetch:
  - Commands, database rows, audit and logs carry no bundle bytes, token,
    password or key.
  - Inventory holds the bundle only in the memory of the fetch call and
    zeroes the buffer afterwards.
  - The fetch is refused on a plaintext ingest edge unless
    `allow_plaintext_ingest` is set (as in 033).
- **SR-003**: Only the configured source may render. The gateway accepts
  `RenderModuleBundle` only from the configured inventory SPIFFE service (mesh
  policy plus a handler check). Inventory accepts `ModuleDeliveryService`
  calls only from `module_delivery.sources` (mesh policy plus a handler
  check).
- **SR-004**: The host decides what happens on the host. The directory,
  enable switch, hook and file ownership come only from the local
  `agent.yaml`. The hook passes the 033 checks (root-owned, not writable by
  group or others, no shell, fixed environment, timeout).
- **SR-005**: Archive safety:
  - only regular files and directories
  - every path under `<module>/` and clean
  - limits on count and size
  - files under `private/` and `.env` are written 0600; others 0644 (0755
    when the archive marks them executable); directories 0750
  - files are created with `O_EXCL|O_NOFOLLOW` under an `os.Root`
- **SR-006**: The inventory and gateway handlers bound the size of the
  rendered bundle (≤ 12 MiB) and the agent re-checks it.

## Success Criteria

- **SC-001**: Adding sms-gw to an enrolled host takes one form and no file
  copying.
- **SC-002**: Grepping the inventory and gateway databases, logs and audit for
  the delivered token and passwords finds nothing.
- **SC-003**: A forged or another agent's `item_id` returns NotFound and is
  audited.

## Assumptions

- Hosts belong to the administrator's own tenant, which is the platform
  tenant under the 036 decision.
- Delivery is Linux only, like 033.
- Upgrading an installed module is out of scope.
