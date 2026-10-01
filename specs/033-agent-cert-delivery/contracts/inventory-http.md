# Contract: inventory HTTP API — host certificates (033)

Gateway-proxied browser API in `api/openapi/inventory.yaml`
(`x-freya-permission`, `additionalProperties: false`, body limits).
No endpoint returns material (PEM) of any kind. Lists follow the 032
server-side paging contract (`page`, `pageSize`, `sort`, `order`, response
`{items, total, page, pageSize}`).

## Permissions

No new permission (research D18). Reads: `inventory:read`. Cancel:
`agents:manage`.

## Endpoints

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/api/inventory/v1/hosts/{id}/certificates` | `inventory:read` | current certificates of a host (one row per name) |
| GET | `/api/inventory/v1/certificate-deliveries` | `inventory:read` | delivery items, paged; filters `host_id`, `state`, `name`, `certificate_id`, `delivery_id`; sort `created_at` (default desc), `updated_at`, `state`, `name` |
| GET | `/api/inventory/v1/certificate-deliveries/{item_id}` | `inventory:read` | one item with its delivery (configuration id, trigger, requested by) |
| POST | `/api/inventory/v1/certificate-deliveries/{item_id}/cancel` | `agents:manage` | cancel an active item (`cancelled/cancelled_by_user`) |

`GET /api/inventory/v1/agents` items gain `certificate_capability`
(data-model §1.5). `GET /api/inventory/v1/hosts` (used by the deployer
host picker) is unchanged; its items already carry `tags`; the picker
joins online state and capability from `GET /api/inventory/v1/agents`.

## Schemas

```yaml
HostCertificate:
  type: object
  additionalProperties: false
  properties:
    name: {type: string}
    certificate_id: {type: string}
    configuration_id: {type: string}
    common_name: {type: string}
    serial: {type: string}
    fingerprint_sha256: {type: string}
    not_after: {type: string, format: date-time, nullable: true}
    state: {type: string, enum: [installed, unchanged, failed, hook_failed, unsupported, expired, cancelled, superseded]}
    reason: {type: string}
    hook_exit_code: {type: integer, nullable: true}
    last_delivered_at: {type: string, format: date-time, nullable: true}
    revoked: {type: boolean}
    active_item: {$ref: '#/components/schemas/CertificateDeliveryItem', nullable: true}
CertificateDeliveryItem:
  type: object
  additionalProperties: false
  properties:
    id: {type: string, format: uuid}
    delivery_id: {type: string, format: uuid}
    host_id: {type: string, format: uuid}
    hostname: {type: string}
    name: {type: string}
    certificate_id: {type: string}
    configuration_id: {type: string}
    trigger: {type: string, enum: [manual, auto_deploy, retry]}
    state: {type: string}
    reason: {type: string}
    attempts: {type: integer}
    serial: {type: string}
    fingerprint_sha256: {type: string}
    hook_exit_code: {type: integer, nullable: true}
    created_at: {type: string, format: date-time}
    updated_at: {type: string, format: date-time}
    finished_at: {type: string, format: date-time, nullable: true}
```

## Errors

- 404 for hosts/items of other tenants or unknown ids.
- 409 `not_cancellable` when cancelling a terminal item.
- 422 `validation_failed` for bad list parameters (032).
- 503 `certificate_delivery_disabled` when `cert_delivery.enabled = false`
  (reads still work and return empty lists).

## UI (`ui/`)

- `src/views/hosts/detail.vue`: tab **Certificates** — table of
  `HostCertificate` (name, CN, serial, fingerprint (shortened, copy),
  expires, state badge, hook exit, last delivered, revoked badge, link
  "Open in deployer" to `/deployer/configurations?id=<configuration_id>`),
  and a paged delivery history (`certificate-deliveries?host_id=`); cancel
  action on active items when `can('manage','InventoryAgent')`.
- `src/views/agents/index.vue`: column **Certificates** with capability
  badge and tooltip reason.
- `src/stores/certificates.ts`, `src/api/types.ts` (+ generated
  `schema.d.ts`).
