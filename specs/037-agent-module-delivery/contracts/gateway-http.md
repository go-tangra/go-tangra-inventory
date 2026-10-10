# Contract: gateway ops endpoints (go-tangra-portal-v4)

Administrator only (`catalogue:join`), like 036.

## GET /gateway/v1/ops/catalogue/{module}/targets?q=<substring>

200 `{ "hosts": [ { "host_id", "hostname", "os_name", "agent_online": bool,
"capability", "ip_addresses": [..] } ], "truncated": bool }` for the
caller's tenant. 503 when agent delivery is not configured or inventory is
unreachable; 404 when the module has no verified entry.

## POST /gateway/v1/ops/catalogue/{module}/deliver

Body `{ "host_id": uuid, "inputs": {..}, "ttl_hours": 1..24 }`.

| Status | Body |
|---|---|
| 202 | `{ "join_id", "expires_at", "delivery": DeliveryView }` |
| 400 `validation_failed` | `detail.param` = input key, `host_id` or `ttl_hours` |
| 403 | not an administrator |
| 404 | no verified entry / unknown host |
| 409 `conflict` | allow-list entry differs (036), or host not eligible (`detail.reason`) |
| 503 | not configured, inventory unreachable |

## GET /gateway/v1/ops/catalogue/{module}/join/{id}

036 response plus, for agent joins, `"channel": "agent"` and
`"delivery": { "host_id", "hostname", "state", "reason"?, "agent_online",
"hook_exit_code"? }` (omitted and `partial: true` when inventory is
unreachable). Download joins report `"channel": "download"`.

## Mesh: inventory.v1.ModuleBundleSource/RenderModuleBundle (gateway serves)

Accepted only from `spiffe://<td>/svc/<catalogue.agent_delivery.inventory_service>`.
Renders for a join with `channel = agent`, matching tenant and host, not
expired, `renders < 5`; mints the token (expires with the join), records its
JTI on the join (latest wins), audits `module_bundle_rendered` (join id,
item id, jti, host; no material).

Gateway config:

```yaml
catalogue:
  agent_delivery:
    inventory_service: inventory   # empty disables agent delivery
```
