# Contract: deployer provider `inventory-agent` (033)

Repository: go-tangra-deployer-v4. Package
`internal/providers/inventoryagent`. Registered at app wiring when the
`inventory` section is configured (research D3).

## 1. Capabilities (as returned by `GET /api/deployer/v1/providers`)

```json
{
  "type": "inventory-agent",
  "display_name": "Inventory agent",
  "supports_verify": true,
  "supports_rollback": false,
  "delivers_by_reference": true,
  "config_fields": [
    {"key": "host_ids", "label": "Hosts", "type": "host_selector", "help": "Inventory hosts that receive the certificate"},
    {"key": "host_tags", "label": "Host tags", "type": "string_list", "max": 16, "help": "key or key=value; a host must match all"},
    {"key": "cert_name", "label": "Certificate name", "type": "string", "help": "Directory name under live/ on the host; default from the common name"},
    {"key": "key_policy", "label": "Private key", "type": "enum", "options": ["require", "certificate_only"], "default": "require"},
    {"key": "require_all_success", "label": "Require all hosts", "type": "bool", "default": false},
    {"key": "wait_seconds", "label": "Wait for hosts (s)", "type": "int", "min": 0, "max": 240, "default": 60}
  ],
  "credential_fields": []
}
```

`required_config` (legacy hint list) stays empty: the "at least one of
`host_ids`/`host_tags`" rule is enforced by `ValidateConfig`.

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
- `configs.Create/Update`: `if v, ok := p.(provider.ConfigValidator); ok
  { v.ValidateConfig(in.Config) }`; targets: validate the merged effective
  config for every configuration override touching an inventory-agent
  configuration.
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

- `src/views/configurations/index.vue`: when `provider_type ===
  'inventory-agent'`, render `<InventoryAgentConfig v-model="config">`
  instead of the JSON textarea; the credentials textarea is hidden
  (no credential fields).
- `src/components/InventoryAgentConfig.vue`: fields from §1; host picker
  `src/components/HostPicker.vue`: server-paged table (032 conventions)
  over `GET /api/inventory/v1/hosts?page=&pageSize=&search=&tag=` with the
  user's session (columns: hostname, OS, tags, agent online, certificate
  capability); selected hosts as chips; 401/403/404 → notice "Host list
  unavailable — enter host ids manually" and a textarea for ids.
- Validate shows `matched_hosts` in a table with capability badges.
- `src/views/jobs/index.vue`: job drawer renders `result.details.hosts`
  and `counts` for provider `inventory-agent` (states as badges).
- `src/schemas/configuration.ts`: zod schema for the provider config
  mirroring §2.
