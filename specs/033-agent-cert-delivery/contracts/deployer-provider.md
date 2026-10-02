# Contract: deployer provider `inventory-agent` (033)

Repository: go-tangra-deployer-v4. Package
`internal/providers/inventoryagent`. Registered at app wiring when the
`inventory` section is configured (research D3).

## 1. Capabilities (as returned by `GET /api/deployer/v1/providers`)

Field descriptors follow [deployer-config-ui.md](deployer-config-ui.md) §2
(the generic schema-driven drawer, US5); the same descriptors drive
save-time validation.

```json
{
  "type": "inventory-agent",
  "display_name": "Inventory agent",
  "description": "Delivers the certificate to inventory hosts through their agents (certbot layout).",
  "supports_verify": true,
  "supports_rollback": false,
  "delivers_by_reference": true,
  "test_connection": false,
  "schema_version": 1,
  "config_fields": [
    {"key": "host_ids", "overridable": true, "label": "Hosts", "type": "host_selector", "group": "connection", "max_items": 1000, "help": "Inventory hosts that receive the certificate"},
    {"key": "host_tags", "overridable": true, "label": "Host tags", "type": "string_list", "group": "connection", "max_items": 16, "pattern": "^[A-Za-z0-9_.:/-]{1,63}(=[^\\u0000-\\u001f]{0,255})?$", "placeholder": "role=web", "help": "key or key=value; a host must match all"},
    {"key": "cert_name", "overridable": true, "label": "Certificate name", "type": "string", "group": "options", "pattern": "^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$", "placeholder": "www", "help": "Directory name under live/ on the host; default from the common name"},
    {"key": "key_policy", "overridable": true, "label": "Private key", "type": "enum", "group": "options", "options": [{"value": "require", "label": "Required"}, {"value": "certificate_only", "label": "Certificate only (keep the host's key)"}], "default": "require"},
    {"key": "require_all_success", "overridable": true, "label": "Require all hosts", "type": "bool", "group": "options", "default": false},
    {"key": "wait_seconds", "overridable": true, "label": "Wait for hosts (s)", "type": "int", "group": "options", "min": 0, "max": 240, "default": 60}
  ],
  "credential_fields": [],
  "one_of_required": [["host_ids", "host_tags"]]
}
```

The "at least one of `host_ids`/`host_tags`" rule is expressed by
`one_of_required` and enforced by the generic validator; the provider's
`ValidateConfig` adds the rules a descriptor cannot express (UUID format of
`host_ids`, `cert_name` without `..`). The UI labels the validate action
"Preview hosts".

Every field is `overridable` (research D25, open question Q8): a shared
configuration may leave the host selection empty — both members of the
`one_of_required` group are then target-supplied, and each target that
attaches it must supply `host_ids` and/or `host_tags` in its override
(422 `config_overrides.host_ids` / `config_overrides.host_tags`
`one_of_required:host_ids,host_tags` otherwise). `ValidateConfig` runs on
the merged config at attach time as well.

## 2. Config JSON schema (`TargetConfiguration.config`, target overrides)

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "host_ids": {"type": "array", "maxItems": 1000, "uniqueItems": true,
                 "items": {"type": "string", "format": "uuid"}},
    "host_tags": {"type": "array", "maxItems": 16, "uniqueItems": true,
                  "items": {"type": "string", "pattern": "^[A-Za-z0-9_.:/-]{1,63}(=[^\\u0000-\\u001f]{0,255})?$"}},
    "cert_name": {"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$"},
    "key_policy": {"enum": ["require", "certificate_only"], "default": "require"},
    "require_all_success": {"type": "boolean", "default": false},
    "wait_seconds": {"type": "integer", "minimum": 0, "maximum": 240, "default": 60}
  },
  "anyOf": [
    {"required": ["host_ids"], "properties": {"host_ids": {"minItems": 1}}},
    {"required": ["host_tags"], "properties": {"host_tags": {"minItems": 1}}}
  ]
}
```

Plus: `cert_name` must not contain `..`. Errors are `ValidationError{Field,
Msg}` → HTTP 422 with the field (existing configs error mapping).

## 3. Behaviour

### Deploy(ctx, cert, config, creds, progress)

1. `meta := provider.JobFrom(ctx)` (required; missing → error "job context
   missing"). Parse/validate config. `name = cert_name` or
   `certmaterial`-equivalent `DefaultName(cert.CommonName)` (deployer copy
   of the rule, tested against the inventory's test vectors).
2. `CreateCertificateDelivery{tenant=meta.TenantID, idempotency_key=meta.JobID,
   configuration_id, target_id, trigger=meta.Trigger, certificate_id=cert.ID,
   name, key_policy, selector, rearm_failed=true}`; progress 10.
3. Poll `GetCertificateDelivery` every 2 s until every item is settled
   (terminal or, after the first 5 s, `pending|delivered` for an offline
   agent) or `min(wait_seconds, deadline − 10 s)` elapsed; progress 10 + 90 ×
   settled/total.
4. Evaluate (research D9) → `Result`; mesh errors → `error` (job retry).

`cert.PrivateKeyPEM` is always empty for this provider (`delivers_by_reference`).

### Verify(ctx, cert, config, creds)

`VerifyHostCertificates{selector, name, expected = sha256(leaf DER of
cert.CertificatePEM)}` → `Success` iff `matched == total` (or, without
`require_all_success`, `matched ≥ 1` and no `mismatch|revoked`); details
list non-matching hosts (≤ 200).

### Rollback

`ErrUnsupported` (`supports_rollback: false`).

### ValidateCredentials(ctx, creds, config)

Validates config, then `PreviewCertificateTargets`; 0 hosts → error "no
hosts match the selection"; otherwise nil. The configs `Validate` HTTP
endpoint returns `{"valid": true, "details": {"matched_hosts": [{host_id,
hostname, os_name, agent_online, capability}], "unknown_host_ids": [...],
"truncated": false}}` when the provider implements the optional
`provider.Previewer` interface (`Preview(ctx, config) (map[string]any,
error)`); other providers return `{"valid": true}` as today.

## 4. Deployer core changes (`internal/provider`, `internal/jobs`, `internal/configs`, `internal/events`)

- `Capabilities.DeliversByReference`; scheduler `process` and
  `runAction` fetch with `includeKey = !caps.DeliversByReference`
  (Rollback keeps `true` for other providers).
- `provider.WithJob(ctx, JobMeta{TenantID, JobID, ConfigurationID,
  TargetID (from the parent job), Trigger})` around `Deploy`/`Verify`.
- `configs.Create/Update`: the generic descriptor validator
  `provider.ValidateInput` (US5, deployer-config-ui.md §3) runs for every
  provider, then `if v, ok := p.(provider.ConfigValidator); ok
  { v.ValidateConfig(in.Config) }` (on the values present; target-supplied
  fields may be empty, D25); targets: `provider.ValidateOverride` +
  `ValidateConfig` on the merged effective config for every attached
  configuration (all providers, US5, deployer-config-ui.md §4a).
- `provider.Result.Permanent` (US8): permanent provider failures are not
  retried and their `details` are stored in the job result.
- Events consumer: `certificate.revoked` → if the tenant has an active
  `inventory-agent` configuration, `MarkCertificateRevoked` (best effort,
  logged, audited `certificate_revocation_forwarded`).
- Config:

```yaml
inventory:
  service: inventory      # discovery name; empty = provider not registered
discovery:
  static:
    inventory: ["inventory:9975"]
```

## 5. Deployer UI (`ui/`)

Built on the generic schema-driven drawer (US5,
[deployer-config-ui.md](deployer-config-ui.md) §6); this provider adds only
what the generic form cannot render:

- `src/components/ProviderConfigForm.vue` renders the provider's fields;
  the `host_selector` field type is rendered through the slot
  `field-host_ids` with `src/components/HostPicker.vue`: server-paged
  table (032 conventions) over `GET /api/inventory/v1/hosts?page=&pageSize=&search=&tag=`
  with the user's session (columns: hostname, OS, tags, agent online,
  certificate capability); selected hosts as chips; 401/403/404 → notice
  "Host list unavailable — enter host ids manually" and a textarea for ids.
  The Credentials section is hidden (no credential fields).
- "Preview hosts" (the validate action) shows `details.matched_hosts` in a
  table with capability badges.
- `src/views/jobs/index.vue`: job drawer renders `result.details.hosts`
  and `counts` for provider `inventory-agent` (states as badges).
- The zod schema comes from `fieldsToZod` over the descriptors (no
  provider-specific schema file); a unit test checks it against §2.
