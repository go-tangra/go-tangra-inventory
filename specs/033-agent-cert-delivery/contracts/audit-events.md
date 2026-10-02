# Contract: audit vocabulary and realtime events (033)

## Inventory (`internal/audit/audit.go`, table `inventory_audit_events`)

Rows are written **in the same transaction** as the state change
(`AppendAudit` on the tx), like upgrade transitions (023). Subject kind
NEW `cert_delivery` with subject id = item id (request-level rows use the
delivery id). Common `detail` keys: `delivery_id`, `item_id`, `host_id`,
`agent_id`, `name`, `certificate_id`, `serial`, `fingerprint_sha256`,
`state`, `reason`, `source`, `idempotency_key` (deployer job id),
`configuration_id`, `trigger`.

| action (NEW) | actor_kind / actor_id | subject | outcome | extra detail |
|---|---|---|---|---|
| `cert_delivery_requested` | service / `deployer` (SPIFFE service) | cert_delivery / delivery id | ok | `hosts` (count), `unknown_hosts` (count), `key_policy`, `created` |
| `cert_delivery_rearmed` | service / `deployer` | cert_delivery / item id | ok | `attempts` |
| `cert_delivery_delivered` | system / `inventory` | cert_delivery / item id | ok | `instance` (first push only) |
| `cert_delivery_fetched` | agent / agent id | cert_delivery / item id | ok | `has_key`, `fetches` |
| `cert_delivery_installed` | agent / agent id | cert_delivery / item id | ok | `hook_exit_code`, `is_renewal` |
| `cert_delivery_unchanged` | agent / agent id | cert_delivery / item id | ok | — |
| `cert_delivery_failed` | agent / agent id, or system / `inventory` | cert_delivery / item id | error | `reason` |
| `cert_delivery_hook_failed` | agent / agent id | cert_delivery / item id | error | `hook_exit_code`, `reason` |
| `cert_delivery_unsupported` | system / `inventory` | cert_delivery / item id | refused | `reason` |
| `cert_delivery_superseded` | system / `inventory` | cert_delivery / item id | ok | `superseded_by` (item id) or `reason: older_than_installed` |
| `cert_delivery_expired` | system / `inventory` | cert_delivery / item id | error | — |
| `cert_delivery_cancelled` | user / user id; system / `inventory` (revocation, host/agent removal) | cert_delivery / item id | ok | `reason` |
| `cert_delivery_refused` | agent / agent id; service / name | cert_delivery / requested id | refused | `reason` (`not_found`, `not_active`, `fetch_limit`, `plaintext`, `source_not_allowed`, `fingerprint_mismatch`) — throttled 1 per agent+reason per 10 s; the next audited row carries `suppressed` (refusals not audited in between); a requested id that is not a uuid is not recorded as subject |
| `host_certificate_revoked` | service / `deployer` | host / host id | ok | `certificate_id`, `name` |

`detail` never contains PEM, key bytes, hook output or file contents.
`audit.Known` lists the new types; `audit_test.go` asserts the vocabulary
and that a detail containing `-----BEGIN` is rejected by `audit.Validate`
(defence in depth, SC-003).

## Inventory realtime events (`platform:events:<tenant>`, content-free)

- `inventory.certificate.delivery` `{host_id, item_id, state}` after every
  committed transition (host page / agent list refresh).

## Deployer (`go-tangra-deployer-v4/internal/audit/audit.go`)

Existing rows unchanged (`deployment_started|completed|failed|verified`,
`configuration_created|updated`, `credentials_validated`). Additions:

| action | actor | subject | outcome | detail |
|---|---|---|---|---|
| `deployment_completed` / `deployment_failed` (existing) | system / `deployer` | job / job id | ok / failed | for `inventory-agent`: `delivery_id`, `counts` |
| `certificate_revocation_forwarded` (NEW) | system / `deployer` | system / certificate id | ok / failed | `flagged_hosts`, `cancelled_items` |
