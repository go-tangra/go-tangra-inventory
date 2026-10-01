# Contract: schema-driven deployer configuration drawer (033, US5)

Repository: go-tangra-deployer-v4. Packages `internal/provider` (field
descriptors + validator), `internal/providers/*` (declarations),
`internal/configs`, `internal/targets`, `internal/httpapi`,
`api/openapi/deployer.yaml`, `ui/`. Research D21–D25.

The provider capability returned by the backend is the **single source of
truth** for the drawer *and* for save-time validation: the UI builds its
form and its zod schema from it, and `configs.Create/Update`,
`configs.Validate` and target overrides run the same Go validator over the
same descriptors. No provider field list is hard-coded in the UI (v3 kept
optional fields and secret names in the UI — research F8).

## 1. Provider catalogue — `GET /api/deployer/v1/providers`

Existing route, permission `configurations:read` (unchanged). Response
`{"items": [Capabilities…]}` sorted by `type`.

```json
{
  "type": "cloudflare",
  "display_name": "Cloudflare",
  "description": "Uploads the certificate as a Cloudflare custom certificate for one zone.",
  "supports_verify": true,
  "supports_rollback": false,
  "delivers_by_reference": false,
  "test_connection": false,
  "schema_version": 1,
  "config_fields": [
    {"key": "zone_id", "label": "Zone ID", "type": "string", "required": true,
     "group": "connection", "pattern": "^[a-f0-9]{32}$",
     "placeholder": "023e105f4ecef8ad9ca31a8372d0c353",
     "help": "Cloudflare dashboard → the zone → Overview → API → Zone ID."}
  ],
  "credential_fields": [
    {"key": "api_token", "label": "API token", "type": "string", "secret": true,
     "required": true, "group": "credentials", "max_length": 256,
     "help": "API token with the permission Zone → SSL and Certificates → Edit for this zone."}
  ],
  "one_of_required": []
}
```

- `test_connection`: `true` when `ValidateCredentials` contacts the
  endpoint (button "Test connection"); `false` when it only checks the
  input (button "Check settings"); the inventory-agent provider labels it
  "Preview hosts" (its validation returns `details.matched_hosts`).
- `one_of_required`: list of key groups of which at least one key must be
  non-empty (inventory-agent: `[["host_ids", "host_tags"]]`).
- `required_config` / `required_credentials` are **not** part of the
  response (the current UI reads these non-existent keys, research F8);
  the UI derives required keys from the descriptors.

## 2. Field descriptor

| Key | Type | Meaning |
|---|---|---|
| `key` | string | Stable JSON key in `config` or `credentials` (`^[a-z][a-z0-9_]{0,63}$`) |
| `label` | string | Human label without "(optional)" — the UI marks required fields |
| `type` | enum | `string`, `text` (multi-line), `url` (http/https only), `int`, `bool`, `enum`, `string_list`, `key_value`, `host_selector` |
| `required` | bool | Non-empty value needed (empty string, empty list and empty map count as missing — v3 `isEmptyValue`) |
| `secret` | bool | Masked, write-only, never returned; allowed only with `string`/`text`; only in `credential_fields` |
| `default` | any | Pre-filled by the UI on create; type must match `type`; never a secret |
| `options` | `[{value,label}]` | For `enum` (non-empty) |
| `help` | string | One-sentence help shown under the input (≤ 300 chars) |
| `placeholder` | string | Example value (never a real secret) |
| `group` | enum | `connection`, `credentials`, `options` — display section; independent of storage |
| `min`, `max` | int | Bounds for `int` |
| `max_length` | int | For `string`/`text`/`url` and each `string_list` item (default 1024) |
| `pattern` | string | RE2 regex for `string` and each `string_list` item (anchored) |
| `max_items` | int | For `string_list`, `key_value`, `host_selector` |

Omitted keys mean "not set" (`omitempty`), so existing JSON consumers keep
working. A descriptor test (T079) rejects: duplicate keys across both
lists, unknown `type`/`group`, `secret` on a non-string type or in
`config_fields`, `enum` without options, a `default` of the wrong type or
outside its bounds/pattern, a pattern that does not compile.

## 3. Save-time validation and errors

`provider.ValidateInput(caps, config, credentials, mode)` (pure,
100 % covered) runs on configuration create, update (after merging stored
credentials, §4), validate, and on target save for the merged effective
config (base config + override; credentials are not overridable — existing
`rejectCredentialKeys`). It checks: required, `one_of_required`, type,
`pattern`, bounds, `max_length`, `max_items`, enum membership, URL scheme,
and **unknown keys** (`additionalProperties: false`). For `webhook.headers`
the header names `Authorization`, `Proxy-Authorization`, `Cookie`,
`X-API-Key`, `X-Webhook-Secret` are refused (they belong in credential
fields, which are sealed).

Errors → HTTP 422:

```json
{"reason": "validation_failed",
 "detail": {"fields": {"config.zone_id": "required",
                       "credentials.api_token": "required",
                       "config.timeout_seconds": "out_of_range:1..300"}}}
```

- Keys are `config.<key>` / `credentials.<key>` (top-level `name`,
  `provider_type` as today). The kit `useZodForm.setServerError` already
  maps `detail.fields` onto inline errors and focuses the first one; the
  UI binds inputs with `data-field="config.<key>"`.
- Values are message **codes** built only from the descriptor:
  `required`, `one_of_required:<k1>,<k2>`, `wrong_type`, `pattern`,
  `too_long:<n>`, `out_of_range:<min>..<max>`, `too_many_items:<n>`,
  `not_in_options`, `invalid_url`, `unknown_field`,
  `forbidden_header:<name>`. They **never** contain the submitted value
  (fuzz-tested, SR-012).
- Today `failSvc` drops `ValidationError.Field` and writes a bare
  `validation_failed` (research F11); it now writes `detail.fields`.
- Rows saved before this feature that contain unknown keys or lack a
  required field stay readable and deployable; the drawer shows them
  (§6) and they are validated only when edited. A job whose effective
  config misses a **required** field fails before the provider runs with
  "configuration incomplete: <label>" (no value in the message).

## 4. Credentials: write-only, per-field merge

Read (`GET /configurations/{id}`, list rows):

```json
{"id": "…", "provider_type": "bigip", "config": {"partition": "Common"},
 "has_credentials": true,
 "credentials_set": ["host", "password", "username"],
 "credentials_public": {"host": "bigip.example.com", "username": "deployer"}}
```

- `credentials_set`: keys stored in the sealed blob (names only).
- `credentials_public`: values of credential fields whose descriptor has
  `secret: false`, **only** when the caller holds
  `configurations:manage` on the row (the edit drawer); omitted
  otherwise. Secret values are never returned in any response, log,
  audit detail or error.

Update (`PUT /configurations/{id}`):

```json
{"name": "edge-lb", "config": {"partition": "Common"},
 "credentials": {"password": "n3w"},
 "clear_credentials": ["session_token"]}
```

- The server unseals the stored blob, overlays every key of `credentials`
  whose value is non-empty, removes the keys in `clear_credentials`,
  validates the result against the descriptors and reseals it (AD =
  configuration id, as today). Absent or empty values keep the stored
  value ("leave blank to keep").
- `clear_credentials` may name only optional credential fields; naming a
  required one → 422 `credentials.<key>: required`.
- `provider_type` cannot change on update (422 `provider_type`); the UI
  disables the select on edit.

## 5. Validate / test connection — `POST /configurations/validate`

```json
{"provider_type": "bigip", "configuration_id": "0192…",
 "config": {"partition": "Common"}, "credentials": {"password": ""}}
```

- `configuration_id` (optional): the stored credentials of that
  configuration (same tenant, caller holds `configurations:manage` on it,
  same provider type) are merged as in §4 before validating, so "Test
  connection" works on edit with blank secrets. Unknown/foreign id → 404.
- Order: descriptor validation (422 `validation_failed` + fields) →
  `ValidateCredentials` with a 20 s deadline.
- Provider rejection → 422 `{"reason": "credentials_rejected"}`; the
  provider's error text is logged server-side (values redacted) and not
  returned. Success → `200 {"valid": true, "checked": "probe"|"static",
  "details": {…}}` (`details` only for `Previewer` providers, e.g.
  inventory-agent `matched_hosts`).
- Nothing is persisted; the request is audited `configuration_validated`
  (provider type, result; no values).

## 6. Drawer behaviour (UI)

1. Create: **Provider** select first (display name + description); every
   other provider field is hidden until a provider is chosen. Name and
   description follow.
2. Sections in order **Connection**, **Credentials**, **Options** (from
   `group`), each rendered only when it has fields; "Options" is
   collapsed when all its values equal their defaults.
3. Field rendering (kit components; D22):

   | type | component |
   |---|---|
   | `string` | `UiInput` (`UiSecretField` when `secret`) |
   | `text` | `UiTextarea` |
   | `url` | `UiInput type=url` |
   | `int` | `UiNumberInput` (min/max) |
   | `bool` | `UiSwitch` |
   | `enum` | `UiSelect` (≤ 12 options) / `UiCombobox` |
   | `string_list` | `StringListInput` (deployer-local chip input, item pattern) |
   | `key_value` | `UiTagEditor` (`max_items`) |
   | `host_selector` | slot → `HostPicker` (US6); manual id entry fallback |

4. Required fields show the kit required marker and are enforced by a zod
   schema generated from the descriptors (`fieldsToZod`), mirroring the
   Go validator through shared test vectors (T084).
5. Defaults are pre-filled on create; placeholders and help come from the
   descriptor.
6. Edit: provider fixed; config values pre-filled; non-secret credential
   values from `credentials_public`; secret inputs empty with the hint
   "Stored — leave blank to keep" when the key is in `credentials_set`
   (plus a "Clear" action for optional secrets → `clear_credentials`).
7. Switching provider on create with any value entered asks "Discard the
   values entered for <provider>?"; confirming clears config and
   credentials, cancelling restores the previous provider.
8. Server 422 `detail.fields` → inline errors on the matching inputs,
   focus on the first; `credentials_rejected` → form-level alert.
9. "Test connection" / "Check settings" / "Preview hosts" per §1; it runs
   the client-side schema first.
10. Rows with keys not declared by the provider show a notice "Settings
    not recognised by this provider: <keys> — they are removed when you
    save"; the save sends only declared keys.
11. View (read-only rows): labelled values per section, secrets shown as
    "stored" badges only.
12. Accessibility: every input has a label and `aria-describedby` for help
    and error (kit `UiField`), required state announced, sections are
    headed (`UiSection`), keyboard-only operation; `vitest-axe` and the
    e2e a11y suite cover the drawer.

## 7. Per-provider fields (v4 declarations after this feature)

Legend: **R** required, **S** secret (credential, write-only), G = group
(C connection, K credentials, O options). "v4 today" = declared in
`Capabilities()` before this feature; "read" = read by the provider code
but not declared (silently accepted from JSON today).

### aws_acm — AWS Certificate Manager (`test_connection: false`)

| Key | Store | Type | R | S | Default | G | Validation / help | v3 | v4 today |
|---|---|---|---|---|---|---|---|---|---|
| `region` | config | string | R | | | C | `^[a-z]{2}(-[a-z]+)+-\d{1,2}$`; placeholder `eu-central-1` | R | R |
| `certificate_arn` | config | string | | | | O | `^arn:aws[a-z-]*:acm:[a-z0-9-]+:\d{12}:certificate/[A-Za-z0-9-]+$`; "Reimport into this ACM certificate" | — | declared |
| `access_key_id` | creds | string | R | | | K | `^[A-Z0-9]{16,128}$` | R | R |
| `secret_access_key` | creds | string | R | S | | K | ≤ 256 | R | R, S |
| `session_token` | creds | text | | S | | K | ≤ 4096; temporary credentials only | — | S |

Not declared: `endpoint` (test-only override, research F10) — refused at
save; the provider reads it only from an unexported test option.

### bigip — F5 BIG-IP (`test_connection: true`)

| Key | Store | Type | R | S | Default | G | Validation / help | v3 | v4 today |
|---|---|---|---|---|---|---|---|---|---|
| `host` | creds | string | R | | | C | host or host:port (`^[A-Za-z0-9.-]{1,253}(:\d{1,5})?$` or IPv6 literal); placeholder `bigip.example.com` | R | R |
| `partition` | config | string | R | | `Common` | C | `^[A-Za-z0-9_.-]{1,64}$` | R | R |
| `username` | creds | string | R | | | K | ≤ 128 | R | R |
| `password` | creds | string | R | S | | K | ≤ 256 | R | R, S |

v3 field missing in v4: **`ssl_profile`** (optional; v3 bound the
certificate into an *existing* client-SSL profile; v4 always creates
`<name>_clientssl`) — open question Q5.

### cloudflare — Cloudflare (`test_connection: false`)

| Key | Store | Type | R | S | Default | G | Validation / help | v3 | v4 today |
|---|---|---|---|---|---|---|---|---|---|
| `zone_id` | config | string | R | | | C | `^[a-f0-9]{32}$` | R | R |
| `api_token` | creds | string | R | S | | K | ≤ 256; Zone → SSL and Certificates → Edit | R | R, S |

Not declared: `api_base` (test-only override, F10) — refused at save.

### dummy — Dummy (testing) (`test_connection: false`)

| Key | Store | Type | R | S | Default | G | Validation / help | v3 | v4 today |
|---|---|---|---|---|---|---|---|---|---|
| `fail` | config | bool | | | `false` | O | "Simulate a failed deployment" | (`should_fail`) | read |

(v3 dummy knobs `fail_message`, `simulate_delay_ms`,
`simulate_progress_steps` were never in the v3 drawer; not carried over.)

### fortigate — FortiGate (`test_connection: true`)

| Key | Store | Type | R | S | Default | G | Validation / help | v3 | v4 today |
|---|---|---|---|---|---|---|---|---|---|
| `host` | creds | string | R | | | C | host or host:port; placeholder `fortigate.example.com` | R | R |
| `vdom` | config | string | R | | `root` | C | `^[A-Za-z0-9_-]{1,31}$` | R | R |
| `api_token` | creds | string | R | S | | K | ≤ 256; REST API administrator token | R | R, S |
| `import_scope` | config | enum | | | `global` | O | `global` \| `vdom` | read | read |

v3 field missing in v4: **`default_ssl_profile`** (optional; v3 also
updated the production ssl-ssh-profile bound to firewall policies in
place) — open question Q5.

### webhook — Webhook (generic HTTP) (`test_connection: true`)

| Key | Store | Type | R | S | Default | G | Validation / help | v3 | v4 today |
|---|---|---|---|---|---|---|---|---|---|
| `url` | config | url | R | | | C | http/https, ≤ 2048 | R | R |
| `verify_url` | config | url | | | | O | defaults to `url` | opt | declared |
| `rollback_url` | config | url | | | | O | defaults to `url` | opt | declared |
| `timeout_seconds` | config | int | | | `60` | O | 1–300 | opt | read |
| `skip_tls_verify` | config | bool | | | `false` | O | "Only for endpoints with self-signed certificates" | opt | read |
| `headers` | config | key_value | | | | O | ≤ 20; auth header names refused (§3) | — | read |
| `metadata` | config | key_value | | | | O | ≤ 20; sent in the payload | — | read |
| `token` | creds | string | | S | | K | Bearer token | opt | S |
| `authorization` | creds | string | | S | | K | raw `Authorization` value; wins over `token` | opt | read |
| `api_key` | creds | string | | S | | K | sent as `X-API-Key` | opt | read |
| `secret` | creds | string | | S | | K | sent as `X-Webhook-Secret` | opt | S |

### inventory-agent — Inventory agent (`test_connection` label "Preview hosts")

| Key | Store | Type | R | S | Default | G | Validation / help | v3 (`tangra-client`) |
|---|---|---|---|---|---|---|---|---|
| `host_ids` | config | host_selector | one-of | | | C | UUIDs, ≤ 1000 | `client_ids` |
| `host_tags` | config | string_list | one-of | | | C | ≤ 16, `^[A-Za-z0-9_.:/-]{1,63}(=[^\x00-\x1f]{0,255})?$` | `labels` (AND) |
| `cert_name` | config | string | | | (from CN) | O | `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, no `..` | `cert_name` |
| `key_policy` | config | enum | | | `require` | O | `require` \| `certificate_only` | — |
| `require_all_success` | config | bool | | | `false` | O | | `require_all_success` |
| `wait_seconds` | config | int | | | `60` | O | 0–240 | — |

`one_of_required: [["host_ids", "host_tags"]]`; no credential fields
(Credentials section hidden). Full schema and behaviour:
[deployer-provider.md](deployer-provider.md).

## 8. OpenAPI (`api/openapi/deployer.yaml`)

New component schemas `ProviderCapabilities`, `ProviderField`,
`FieldOption`, `ConfigurationView` (+ `credentials_set`,
`credentials_public`), `ConfigurationInput` (+ `clear_credentials`,
`additionalProperties: false` at the top level), `ValidateRequest`
(+ `configuration_id`), `ValidateResult`, `FieldErrors`; responses of
`listProviders`, `createConfiguration`, `updateConfiguration`,
`validateCredentials` reference them with `422` documented. Contract test:
every route keeps its `x-freya-permission`.
