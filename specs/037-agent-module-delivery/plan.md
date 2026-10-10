# Implementation Plan: Deliver Module Install Bundles Through the Inventory Agent

**Branch**: `037-agent-module-delivery` | **Date**: 2026-10-10 | **Spec**: [spec.md](spec.md)

## Summary

The add-module wizard (036) gains **Deliver to a host**. The gateway stores
an agent join (host, inputs) and calls inventory
`ModuleDeliveryService.CreateModuleDelivery`; inventory stores a reference
item and pushes a `MODULE` command to the host's enrolled agent. The agent
calls `FetchModuleBundle`; inventory then streams
`ModuleBundleSource.RenderModuleBundle` from the gateway, which mints the
token and renders the bundle at that moment, checks size and digest, and
relays it. The agent validates the archive, writes it under its local
modules directory, runs only its locally configured hook, and reports.
Progress flows back through `GetModuleDelivery` into the 036 join progress.

## Technical Context

**Language/Version**: Go 1.26; Vue 3 (gateway shell)
**Primary Dependencies**: none new
**Storage**: inventory Postgres `module_delivery_items` (0012); gateway
`catalogue_joins` columns (0008)
**Testing**: `go test -race`, agent tests on a temp `os.Root`, bufconn
mesh tests, shell vitest; integration suites tagged as before
**Constraints**: no material at rest; ≤ 12 MiB rendered; Linux agents only

## Constitution Check

- [x] **I. Secure by default**: delivery off on the server until configured; on the agent, only file writes unless a hook is set locally.
- [x] **II. Least privilege**: sources allow-list both directions; the agent fetches only its own items.
- [x] **III. Validate inputs**: closed reasons, name patterns, archive checks, size caps.
- [x] **IV. Tests first incl. negative**: other agent's item, expired join, host mismatch, traversal zip, existing dir, plaintext edge.
- [x] **V. Audit**: create/render/fetch/report/refusal.
- [x] **VI. No new dependency, stdlib crypto.**
- [x] **VII. Typed config** (`module_delivery`, `modules`, `catalogue.agent_delivery`).

## Project Structure

```text
inventory (go-tangra-inventory-v4, branch 037-agent-module-delivery)
  sdk/api/proto/inventory/v1/inventory.proto   # contracts/proto.md
  internal/store/migrations/0012_module_delivery.sql
  internal/store (models), internal/repo + repodb + memstore (persistence)
  internal/moduledelivery/   # create, resolve, fetch(relay), report, replay, sweep, views
  internal/grpcapi/moduledelivery.go           # ModuleDeliveryService
  internal/ingest/modules.go                   # FetchModuleBundle, ReportModule, replay at connect
  internal/registry                            # Command.Module
  internal/config (module_delivery server, modules agent)
  internal/agentmodules/     # archive checks, staging write, hook
  internal/daemon/modules.go # queue, fetch, write, report
  packaging/agent.yaml, packaging/examples/module-compose-up.sh
  deploy/README.md, deploy/policy.yaml
portal (go-tangra-portal-v4, branch 037-agent-module-delivery)
  internal/store/migrations/0008_catalogue_join_agent.sql
  internal/catalogue/join.go (render with stored inputs)
  internal/grpcapi/modulebundle.go   # ModuleBundleSource server
  internal/httpapi/catalogue_deliver.go  # targets, deliver, progress
  internal/config (catalogue.agent_delivery), internal/app wiring
  api/openapi/gateway.yaml (+ spec 003 copy), deploy/policy.yaml
  shell JoinWizard (channel choice, host picker, delivery progress)
```

## Release order

1. inventory PR (proto + server + agent) → tag inventory `v4.8.0` and `sdk/v4.5.0`.
2. portal PR bumps inventory SDK to `sdk/v4.5.0` → tag gateway `v4.11.0`.
3. Prod: gateway policy rule, inventory `module_delivery` config, agents upgraded.
