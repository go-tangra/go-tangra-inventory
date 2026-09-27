# Contract: inventory HTTP admin API changes (023)

`api/openapi/inventory.yaml` (embedded; routes and permissions registered
with the gateway through `pkg/inventorymanifest`). Mutating requests carry
the existing `X-CSRF-Token` header; bodies declare
`x-freya-max-body-bytes`; permission refs are module-scoped
(`inventory:<resource>:<action>`); tenant always from the gateway subject.

## New permission (D12)

```go
{Resource: "agentupgrades", Action: "manage", Description: "Configure automatic agent upgrades and the target agent version"}
```

Grants: `owner`, `admin` (via `PermissionRefs()`), module role
`administrator` (via `PermissionRefs()`); **not** `operator`, `member`,
`auditor`, module roles `editor`/`viewer`. CASL:
`{Action: ["manage"], Subject: ["InventoryAgentUpgradePolicy"], Requires: "agentupgrades:manage"}`.
Existing `agents:manage` description becomes "Enroll, refresh, list,
upgrade and revoke endpoint agents".

## Endpoints

| Method | Path | Permission | Body limit | Result |
|---|---|---|---|---|
| GET | `/api/inventory/v1/agents` (changed, additive) | `agents:manage` | — | `{items: AgentFleetEntry[], current_version}` — all enrolled, non-revoked agents (online and offline); query `state`, `outdated=true`, `cursor`, `limit` (1–500) |
| GET | `/api/inventory/v1/agents/{id}` | `agents:manage` | — | `AgentFleetEntry` + `recent_upgrades` (≤ 20) |
| POST | `/api/inventory/v1/agents/upgrades` | `agents:manage` | 64 KiB | 202 `UpgradeBatchResult` |
| GET | `/api/inventory/v1/agents/upgrades` | `agents:manage` | — | `{items: AgentUpgrade[], next_cursor}`; query `state`, `agent_id`, `cursor`, `limit` |
| POST | `/api/inventory/v1/agents/upgrades/{id}/cancel` | `agents:manage` | 1 KiB | `AgentUpgrade` (409 `not_cancellable` unless pending/delivered) |
| GET | `/api/inventory/v1/agents/upgrade-policy` | `agents:manage` | — | `UpgradePolicy` (defaults when no row) |
| PUT | `/api/inventory/v1/agents/upgrade-policy` | `agentupgrades:manage` | 4 KiB | `UpgradePolicy` |
| POST | `/api/inventory/v1/agents/upgrade-policy/resume` | `agentupgrades:manage` | 1 KiB | `UpgradePolicy` |
| GET | `/api/inventory/v1/agent-releases` | `agents:manage` | — | `{current_version, items: AgentReleaseInfo[]}` |

The existing `GET /agents` items keep their keys (`agent_id`, `tenant_id`,
`host_id`, `version`, `connected_at`) and gain the new ones, so the UI can
migrate in the same release.

## Schemas

```yaml
AgentFleetEntry:
  type: object
  properties:
    agent_id:        { type: string, format: uuid }
    host_id:         { type: string, format: uuid }
    hostname:        { type: string }
    version:         { type: string }
    os:              { type: string, enum: ['', linux, windows] }
    arch:            { type: string, enum: ['', amd64, arm64] }
    install_type:    { type: string, enum: ['', deb, rpm, binary] }
    online:          { type: boolean }
    connected_at:    { type: string, format: date-time }
    last_seen:       { type: string, format: date-time }
    target_version:  { type: string }
    upgrade_state:   { type: string, enum: [up_to_date, available, pending, in_progress, failed, rolled_back, manual_upgrade_required, unsupported] }
    upgrade_reason:  { type: string, description: 'reason code only' }
    upgrade_id:      { type: string, format: uuid }
    state_changed_at: { type: string, format: date-time }

UpgradeRequest:            # POST /agents/upgrades
  type: object
  additionalProperties: false
  properties:
    agent_ids:    { type: array, maxItems: 1000, uniqueItems: true, items: { type: string, format: uuid } }
    all_outdated: { type: boolean, description: 'every outdated, upgrade-capable agent of the tenant' }
  oneOf:
    - required: [agent_ids]
    - required: [all_outdated]

UpgradeBatchResult:
  type: object
  properties:
    target_version: { type: string }
    created:  { type: array, items: { $ref: '#/components/schemas/AgentUpgrade' } }
    skipped:  { type: array, items: { type: object, properties: {
                  agent_id: { type: string, format: uuid },
                  reason: { type: string, enum: [up_to_date, upgrade_active, manual_upgrade_required, unsupported, no_release_for_platform, not_found] } } } }

AgentUpgrade:
  type: object
  properties:
    id:              { type: string, format: uuid }
    agent_id:        { type: string, format: uuid }
    host_id:         { type: string, format: uuid }
    from_version:    { type: string }
    target_version:  { type: string }
    state:           { type: string, enum: [pending, delivered, downloading, installing, succeeded, failed, rolled_back, expired, cancelled] }
    origin:          { type: string, enum: [user, policy, agent] }
    requested_by:    { type: string }
    reason:          { type: string }
    created_at:      { type: string, format: date-time }
    updated_at:      { type: string, format: date-time }
    expires_at:      { type: string, format: date-time }
    finished_at:     { type: string, format: date-time }

UpgradePolicy:
  type: object
  additionalProperties: false
  required: [enabled, window_start, window_end, timezone, max_concurrent, target_version]
  properties:
    enabled:        { type: boolean }
    window_start:   { type: string, pattern: '^([01][0-9]|2[0-3]):[0-5][0-9]$' }
    window_end:     { type: string, pattern: '^([01][0-9]|2[0-3]):[0-5][0-9]$' }
    timezone:       { type: string, maxLength: 64, description: 'IANA name, validated with time.LoadLocation' }
    max_concurrent: { type: integer, minimum: 1, maximum: 100 }
    target_version: { type: string, maxLength: 64, description: '"" = platform current; otherwise a stored release version' }
    paused:         { type: boolean, readOnly: true }
    paused_reason:  { type: string, readOnly: true }
    updated_by:     { type: string, readOnly: true }
    updated_at:     { type: string, format: date-time, readOnly: true }

AgentReleaseInfo:
  type: object
  properties:
    version:   { type: string }
    source:    { type: string, enum: [bundled, import] }
    key_id:    { type: string }
    imported_at: { type: string, format: date-time }
    platforms: { type: array, items: { type: object, properties: {
                   os: { type: string }, arch: { type: string }, install_type: { type: string }, size: { type: integer } } } }
```

## Errors

`400 invalid_argument` (schema/unknown field), `403 forbidden`,
`404 not_found`, `409 upgrade_active` / `not_cancellable` /
`unknown_version` (policy pin not stored), `413 payload_too_large`,
`503 temporarily_unavailable`. Error bodies carry codes only.

## Behaviour

- `POST /agents/upgrades`: target = tenant pin or platform current; a pin
  lower than an agent's version creates the request with
  `allow_downgrade=true` (only possible because setting the pin required
  `agentupgrades:manage`); each created request is audited
  (`agent_upgrade_requested`, actor user) and delivered immediately to
  online agents.
- `PUT /agents/upgrade-policy`: validates the window, timezone and that
  `target_version` is stored; audit `upgrade_policy_updated` with
  before/after; enabling does not create requests synchronously (the
  scheduler does, inside the window).
- `POST …/resume`: clears `paused`, audit `upgrade_policy_resumed`.
