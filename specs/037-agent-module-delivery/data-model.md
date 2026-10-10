# Data Model

## Inventory: `module_delivery_items` (migration 0012)

| Column | Type | Notes |
|---|---|---|
| id | uuid pk | item id (sent to the agent) |
| tenant_id | uuid | RLS like 033 tables |
| source | text | caller service name (`gateway`) |
| delivery_id | uuid | unique (tenant_id, source, delivery_id) |
| host_id | uuid | |
| agent_id | uuid null | resolved at create; null when unsupported |
| module | text | |
| version | text | |
| state | text | pending, delivered, fetched, installed, failed, hook_failed, unsupported, superseded, expired |
| reason | text | closed set |
| hook_exit_code | int | -1 default |
| attempt | int | 1 (re-delivery = new item) |
| fetches | int | ≤ 5 |
| created_at, updated_at, expires_at, delivered_at, fetched_at, reported_at | timestamptz | |

Index: partial on (tenant_id, host_id, module) where state in active
(pending, delivered, fetched) for supersede; (agent_id) where active for replay.

Active = pending | delivered | fetched. Terminal states never change.

## Gateway: `catalogue_joins` (migration 0008, additive)

| Column | Type | Notes |
|---|---|---|
| channel | text not null default 'download' | download, agent |
| tenant_id | uuid null | agent joins |
| host_id | uuid null | agent joins |
| inputs | jsonb null | host inputs (not secret) for render time |
| renders | int not null default 0 | ≤ 5 |
| jti | text (now nullable) | set at render for agent joins |
