# Quickstart: Inventory Service

Validation scenarios proving the feature end to end. Assumes the `deploy/stack`
platform is up (TimescaleDB, Valkey, gateway, auth, lcm) with the inventory
service registered, plus its ingest edge reachable to endpoints.

## Prerequisites
- inventory service running (mesh API via gateway `/api/inventory`; ingest edge listener up).
- An operator signed into the console with `agents:manage` + `inventory:read`.
- A test endpoint (or the `inventory-agent` binary / a test client) able to reach the ingest edge.

## Scenario 1 — Enroll an agent and ingest the first snapshot (US1)
1. In the console Agents view (or `POST /api/inventory/v1/agents/enroll-token`), mint an enrollment token; copy the one-time secret.
2. Run the agent one-shot with the token and ingest endpoint: it enrolls (token consumed), collects, and submits a snapshot.
3. Confirm a host appears in the Hosts list for your tenant with hostname, OS, manufacturer and a last-seen just now.
4. Re-running the same token fails (single-use). A submit without a valid agent credential is rejected.

## Scenario 2 — Browse and inspect (US2)
1. Filter Hosts by OS / manufacturer / tag; confirm only your tenant's matching hosts appear.
2. Open the host; verify Hardware (BIOS/system/board/CPU/memory/disks/monitors), Software (OS/programs/services/users), and Network (interfaces) tabs are populated from the latest snapshot.

## Scenario 3 — History & diff (US3)
1. Submit a second snapshot for the host with a change (e.g. a new installed program, a removed memory module).
2. Open the host's Snapshot History; confirm both snapshots (newest first).
3. Diff the two snapshots; confirm the added/removed/modified components are listed, and a change-history entry exists.

## Scenario 4 — On-demand refresh & live status (US4)
1. Start the agent in daemon mode so it holds a command stream; confirm it shows Online in the Agents view.
2. Trigger Refresh for the host; confirm the agent re-collects and a new snapshot arrives within ~30s, and the UI updates live (no reload).
3. Trigger Refresh for a host whose agent is offline; confirm you are told it was not delivered.
4. (Scale) With two server instances, confirm a refresh routed to the non-owning instance still reaches the agent.

## Scenario 5 — Statistics & backup (US5)
1. Open the Dashboard; confirm host counts by status/OS/manufacturer, hardware/software rollups, agents online/offline, stale hosts and snapshots-per-day.
2. As an administrator, view system-wide statistics with a per-tenant breakdown.
3. Export the tenant (optionally include history); import into a clean tenant with mode skip vs overwrite; confirm hosts, latest snapshots, tags and change history round-trip and nothing leaks across tenants.

## Security checks (cross-cutting)
- Inspect logs/audit/backups: no agent/enrollment credentials appear anywhere.
- A cross-tenant read/query returns nothing; an agent cannot write outside its tenant/host scope.
- An oversized/malformed snapshot is rejected with no partial write.
