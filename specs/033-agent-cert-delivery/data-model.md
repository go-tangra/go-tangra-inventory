# Data Model: Deliver Issued and Renewed Certificates to Inventory-Agent Hosts (033)

No table, column, cache key, event or log field anywhere holds private-key
material. Certificates and chains are not stored by the inventory either
(only identity: id, serial, fingerprint, subject, expiry); material is
fetched from lcm per agent fetch (research D1, D5).

## 1. Inventory

### 1.1 Domain structs (`internal/store/models.go`)

```go
// Delivery states (closed set).
const (
    DeliveryPending     = "pending"      // created, waiting for the agent
    DeliveryDelivered   = "delivered"    // CERTIFICATE command pushed at least once
    DeliveryFetched     = "fetched"      // material served to the agent
    DeliveryInstalled   = "installed"    // terminal: written (+ hook ok or no hook)
    DeliveryUnchanged   = "unchanged"    // terminal: same fingerprint already installed
    DeliveryFailed      = "failed"       // terminal (re-armable)
    DeliveryHookFailed  = "hook_failed"  // terminal (re-armable): files installed, hook failed
    DeliveryUnsupported = "unsupported"  // terminal: no agent / no cert.v1 / platform
    DeliverySuperseded  = "superseded"   // terminal: newer item for the same host+name
    DeliveryExpired     = "expired"      // terminal (re-armable)
    DeliveryCancelled   = "cancelled"    // terminal: user, revocation, host/agent removed
)

// Reason codes (closed set; reported by the agent or set by the server).
// agent:  invalid_name, invalid_bundle, key_mismatch, certificate_not_valid,
//         owner_unknown, write_failed, disk_full, hook_failed, hook_timeout,
//         hook_refused, disabled_locally, busy
// server: key_unavailable, certificate_revoked, certificate_expired,
//         certificate_not_found, lcm_unavailable, bundle_too_large,
//         invalid_bundle, fingerprint_mismatch,
//         no_agent, no_capability, platform, host_retired, agent_revoked,
//         host_deleted, no_report, expired, older_than_installed,
//         cancelled_by_user, unknown_host

const CapCertV1 = "cert.v1"

const (
    KeyPolicyRequire         = "require"
    KeyPolicyCertificateOnly = "certificate_only"
)

type CertDelivery struct {
    ID              string     // uuid
    TenantID        string
    Source          string     // spiffe service name, e.g. "deployer"
    IdempotencyKey  string     // deployer job id (unique per tenant+source)
    ConfigurationID string     // deployer configuration id (display/link)
    TargetID        string     // deployer target id ("" for direct jobs)
    Trigger         string     // manual | auto_deploy | retry (from deployer)
    CertificateID   string     // lcm certificate id
    Name            string     // <name>, validated (certmaterial.ValidName)
    KeyPolicy       string     // require | certificate_only
    HostIDs         []string   // requested explicit ids (≤ 1000)
    HostTags        []string   // requested selectors (≤ 16)
    RequestedBy     string     // spiffe id of the caller
    CreatedAt       time.Time
    ExpiresAt       time.Time  // min(created + pending_ttl, certificate not_after)
}

type CertDeliveryItem struct {
    ID               string     // uuid; the only id the agent sees
    TenantID         string
    DeliveryID       string
    HostID           string
    AgentID          string     // "" when the host has no agent
    Name             string     // copy of the delivery name (unique active per host+name)
    CertificateID    string
    State            string
    Reason           string
    Attempts         int        // 1..5 (re-arms)
    Fetches          int        // 0..5 per attempt
    RerunHook        bool       // set when re-armed from hook_failed
    Serial           string     // from lcm at fetch / agent report
    FingerprintSHA256 string    // lowercase hex of the leaf DER; reported by the agent
    CommonName       string     // leaf subject CN served at fetch (migration 0011, <= 256 bytes)
    NotAfter         *time.Time
    HookExitCode     *int
    Detail           string     // ≤ 256 bytes, sanitised
    CreatedAt, UpdatedAt time.Time
    DeliveredAt, FetchedAt, FinishedAt *time.Time
}

type HostCertificate struct {
    TenantID          string
    HostID            string
    Name              string
    CertificateID     string
    ConfigurationID   string
    CommonName        string
    Serial            string
    FingerprintSHA256 string
    NotAfter          *time.Time
    State             string     // last terminal item state
    Reason            string
    HookExitCode      *int
    LastItemID        string
    LastDeliveredAt   *time.Time // last installed/unchanged
    RevokedAt         *time.Time // lcm revoked the certificate currently installed
    UpdatedAt         time.Time
}
```

Bounds constants: `MaxDeliveryHosts = 1000`, `MaxHostTags = 16`,
`MaxItemAttempts = 5`, `MaxItemFetches = 5`, `MaxReplayPerConnect = 50`,
`MaxDetailBytes = 256`.

### 1.2 Pure packages

- `internal/certmaterial` (server **and** agent; 100 % gate):
  `ValidName(string) bool`, `DefaultName(cn string) (string, bool)`,
  `ParseBundle(cert, chain, key []byte, opts) (Bundle, error)` (PEM
  parsing, leaf first, ≤ 10 chain certs, size bounds, key ↔ leaf public key,
  validity window), `Fingerprint(leafDER) string`, `FullChain(cert,
  chain) []byte`, `ValidTag(string) bool`. No I/O.
- `internal/certdelivery` (server; 100 % gate): service — create/resolve/
  supersede/deliver/OnConnect/AuthorizeFetch/Fetch/Report/Sweep/
  Revoke/Verify/Preview/Cancel/List.
- `internal/agentcerts` (agent; 100 % gate for the pure core with fake FS
  and fake exec): store layout, idempotency check, atomic install,
  generation pruning, crash recovery, hook environment builder and file
  checks. OS glue (`hook_linux.go`: process group, kill, `os/user`
  lookups, `fchown`) excluded like `internal/collector`.

### 1.3 Migration `internal/store/migrations/0010_cert_delivery.sql`

```sql
-- +goose Up
CREATE TABLE inventory_cert_deliveries (
    id               uuid PRIMARY KEY,
    tenant_id        uuid NOT NULL,
    source           text NOT NULL CHECK (source ~ '^[a-z][a-z0-9-]{0,62}$'),
    idempotency_key  text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    configuration_id text NOT NULL DEFAULT '' CHECK (length(configuration_id) <= 128),
    target_id        text NOT NULL DEFAULT '' CHECK (length(target_id) <= 128),
    trigger          text NOT NULL CHECK (trigger IN ('manual','auto_deploy','retry')),
    certificate_id   text NOT NULL CHECK (length(certificate_id) BETWEEN 1 AND 128),
    name             text NOT NULL CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$' AND position('..' in name) = 0),
    key_policy       text NOT NULL CHECK (key_policy IN ('require','certificate_only')),
    host_ids         uuid[] NOT NULL DEFAULT '{}' CHECK (cardinality(host_ids) <= 1000),
    host_tags        text[] NOT NULL DEFAULT '{}' CHECK (cardinality(host_tags) <= 16),
    requested_by     text NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    UNIQUE (tenant_id, source, idempotency_key)
);

CREATE TABLE inventory_cert_delivery_items (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    delivery_id        uuid NOT NULL REFERENCES inventory_cert_deliveries(id) ON DELETE CASCADE,
    host_id            uuid NOT NULL,
    agent_id           uuid,
    name               text NOT NULL,
    certificate_id     text NOT NULL,
    state              text NOT NULL CHECK (state IN ('pending','delivered','fetched','installed','unchanged',
                         'failed','hook_failed','unsupported','superseded','expired','cancelled')),
    reason             text NOT NULL DEFAULT '' CHECK (reason ~ '^[a-z_]{0,32}$'),
    attempts           int  NOT NULL DEFAULT 1 CHECK (attempts BETWEEN 1 AND 5),
    fetches            int  NOT NULL DEFAULT 0 CHECK (fetches BETWEEN 0 AND 5),
    rerun_hook         boolean NOT NULL DEFAULT false,
    serial             text NOT NULL DEFAULT '' CHECK (serial ~ '^[0-9a-fA-F:]{0,128}$'),
    fingerprint_sha256 text NOT NULL DEFAULT '' CHECK (fingerprint_sha256 ~ '^([0-9a-f]{64})?$'),
    not_after          timestamptz,
    hook_exit_code     int CHECK (hook_exit_code BETWEEN -1 AND 255),
    detail             text NOT NULL DEFAULT '' CHECK (octet_length(detail) <= 256),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    delivered_at       timestamptz,
    fetched_at         timestamptz,
    finished_at        timestamptz,
    UNIQUE (delivery_id, host_id)
);
-- One active item per host and name (research D8).
CREATE UNIQUE INDEX inventory_cert_items_active_uq
    ON inventory_cert_delivery_items (tenant_id, host_id, name)
    WHERE state IN ('pending','delivered','fetched');
CREATE INDEX inventory_cert_items_agent_active
    ON inventory_cert_delivery_items (tenant_id, agent_id, created_at)
    WHERE state IN ('pending','delivered','fetched');
CREATE INDEX inventory_cert_items_host ON inventory_cert_delivery_items (tenant_id, host_id, created_at DESC);
CREATE INDEX inventory_cert_items_cert ON inventory_cert_delivery_items (tenant_id, certificate_id);
CREATE INDEX inventory_cert_items_sweep ON inventory_cert_delivery_items (state, updated_at)
    WHERE state IN ('pending','delivered','fetched');

CREATE TABLE inventory_host_certificates (
    tenant_id          uuid NOT NULL,
    host_id            uuid NOT NULL,
    name               text NOT NULL,
    certificate_id     text NOT NULL,
    configuration_id   text NOT NULL DEFAULT '',
    common_name        text NOT NULL DEFAULT '' CHECK (octet_length(common_name) <= 256),
    serial             text NOT NULL DEFAULT '',
    fingerprint_sha256 text NOT NULL DEFAULT '',
    not_after          timestamptz,
    state              text NOT NULL,
    reason             text NOT NULL DEFAULT '',
    hook_exit_code     int,
    last_item_id       uuid NOT NULL,
    last_delivered_at  timestamptz,
    revoked_at         timestamptz,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, host_id, name)
);
CREATE INDEX inventory_host_certs_cert ON inventory_host_certificates (tenant_id, certificate_id);

-- RLS exactly as 0004/0006: tenant isolation + app.system bypass.
ALTER TABLE inventory_cert_deliveries ENABLE ROW LEVEL SECURITY;      -- + FORCE, policies
ALTER TABLE inventory_cert_delivery_items ENABLE ROW LEVEL SECURITY;  -- + FORCE, policies
ALTER TABLE inventory_host_certificates ENABLE ROW LEVEL SECURITY;    -- + FORCE, policies

-- +goose Down
DROP TABLE inventory_host_certificates;
DROP TABLE inventory_cert_delivery_items;
DROP TABLE inventory_cert_deliveries;
```

Migration `0011_cert_item_common_name.sql` (US4) adds
`inventory_cert_delivery_items.common_name text NOT NULL DEFAULT ''
CHECK (octet_length(common_name) <= 256)`: the CN is recorded at fetch like
the serial and fingerprint, and an installed/unchanged report copies it to
`inventory_host_certificates.common_name`.

Host or agent deletion: `DeleteHost`/`RevokeAgent` cancel the host's or
agent's active items in the same transaction (`cancelled/host_deleted`,
`agent_revoked`); host deletion also deletes its
`inventory_host_certificates` rows. Retention: items older than
`retention.days` in a terminal state are purged with snapshots' purge job;
`inventory_host_certificates` rows are kept while the host exists.

### 1.4 State transitions — delivery item

```text
            create (agent online + cert.v1) ──push──▶ delivered
create ──▶ pending ──connect (cert.v1)──────────────▶ delivered ──FetchCertificate──▶ fetched
   │           │                                        │                               │
   │           └─ connect without cert.v1 ─▶ unsupported└─ (same)                       ├─Report installed  ─▶ installed
   │                                                                                    ├─Report unchanged  ─▶ unchanged
   ├─ no agent / platform / retired ─▶ unsupported                                      ├─Report failed     ─▶ failed
   └─ older_than_installed (auto) ───▶ superseded                                       ├─Report hook_failed─▶ hook_failed
                                                                                        └─ no report 15 min ─▶ failed(no_report)
pending|delivered|fetched ── newer item same host+name ─▶ superseded
pending|delivered|fetched ── expires_at passed          ─▶ expired
pending|delivered|fetched ── revoked / user / host or agent removed ─▶ cancelled
FetchCertificate fails server-side (key_unavailable, certificate_revoked, …) ─▶ failed(reason)
failed|hook_failed|expired ── deployer retry (rearm_failed) ─▶ pending (attempts+1 ≤ 5; rerun_hook if hook_failed)
```

- Reports for terminal items are ignored (`accepted=false`); reports or
  fetches for another agent's item → `NotFound` and `cert_delivery_refused`.
- `installed`/`unchanged` upsert `inventory_host_certificates`
  (`last_delivered_at = now`, `revoked_at = NULL` when the certificate id
  changed); `failed`/`hook_failed` update state/reason but keep the
  previously installed identity columns (the old certificate is still in
  place on the host).

### 1.5 Derived agent certificate capability (agent list)

| Condition (first match) | Shown as |
|---|---|
| `cert_delivery.enabled = false` | `disabled_on_server` |
| agent `os = windows` | `not_supported_platform` |
| agent announces `cert.v1` | `enabled` |
| agent version ≥ 4.7.0 without `cert.v1` | `disabled_on_host` |
| otherwise | `upgrade_required` |

### 1.6 Configuration

Service (`internal/config/config.go`, new typed section):

```yaml
cert_delivery:
  enabled: false                 # server-side switch (Constitution I)
  sources: ["deployer"]          # mesh services allowed to create deliveries (handler check, on top of policy)
  lcm_service: lcm               # discovery name of lcm for Certificates/Download
  pending_ttl_hours: 168         # 1-720
  report_timeout_minutes: 15     # 5-120
  max_concurrent_fetches: 50     # 1-500 per replica
  lcm_timeout_seconds: 10        # 2-60
  allow_plaintext_ingest: false  # dev only; refused with env=production; warning at start
```

Agent (`AgentConfig`, strict YAML):

```yaml
certificates:
  enabled: true                  # announce cert.v1 (false: refuse all deliveries)
  directory: /etc/inventory-agent/certs   # absolute; fixed, never from the server
  owner: root                    # user name or numeric uid
  group: root                    # group name or numeric gid
  dir_mode: "0750"               # 0700-0755, no world write
  cert_mode: "0644"              # cert/chain/fullchain; no group/world write
  key_mode: "0600"               # privkey; 0600 or 0640 only
  keep_previous: 1               # 0-5 previous generations kept per name
  deploy_hook: ""                # absolute path of a root-owned executable; empty = disabled (default)
  hook_timeout_seconds: 300      # 30-1800
  allow_insecure_transport: false   # required with `insecure: true` (dev only)
```

## 2. Deployer (`go-tangra-deployer-v4`)

No migration. The provider configuration lives in the existing
`TargetConfiguration.Config` JSON and per-target `ConfigOverrides`
(`map[configuration id]map[string]any`, stored **unsealed** in the target
row). After this feature an override may hold only keys the provider
declares `overridable` (never credentials or secrets, research D25); a
configuration may leave required overridable fields empty
("target-supplied") and every attached target's merged config must
satisfy them. Existing rows are not rewritten; they are re-validated when
edited and checked for required values at job start.

### 2.1 Provider config (validated by `ValidateConfig`)

| Key | Type | Default | Rule |
|---|---|---|---|
| `host_ids` | string[] | `[]` | uuids, ≤ 1000, deduplicated |
| `host_tags` | string[] | `[]` | ≤ 16, `key` or `key=value` (certmaterial.ValidTag) |
| `cert_name` | string | derived from CN | `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, no `..` |
| `key_policy` | enum | `require` | `require`, `certificate_only` |
| `require_all_success` | bool | `false` | |
| `wait_seconds` | int | `60` | 0–240, clamped to job deadline − 10 s |

At least one of `host_ids`/`host_tags` is required on the merged
(configuration + target override) config; all six keys are overridable,
so a configuration may leave the selection to its targets. Unknown keys
rejected.

### 2.2 Provider package types (`internal/provider/provider.go`, additive)

Field descriptor per [contracts/deployer-config-ui.md](contracts/deployer-config-ui.md) §2 (US5).

```go
type FieldOption struct { Value, Label string }
type Field struct {
    Key       string        `json:"key"`
    Label     string        `json:"label"`
    Secret    bool          `json:"secret"`
    Required  bool          `json:"required"`
    Type      string        `json:"type,omitempty"`  // string|text|url|int|bool|enum|string_list|key_value|host_selector ("" = string)
    Group     string        `json:"group,omitempty"` // connection|credentials|options
    Help      string        `json:"help,omitempty"`
    Placeholder string      `json:"placeholder,omitempty"`
    Options   []FieldOption `json:"options,omitempty"` // enum
    Default   any           `json:"default,omitempty"`
    Min, Max  *int          `json:"min,omitempty"`     // int bounds
    MaxLength int           `json:"max_length,omitempty"`
    Pattern   string        `json:"pattern,omitempty"` // RE2, anchored
    MaxItems  int           `json:"max_items,omitempty"`
    Overridable bool        `json:"overridable,omitempty"` // config fields only, never with Secret (D25)
}
type Capabilities struct {
    /* existing */
    Description         string     `json:"description,omitempty"`
    DeliversByReference bool       `json:"delivers_by_reference"`
    TestConnection      bool       `json:"test_connection"`
    SchemaVersion       int        `json:"schema_version"`
    OneOfRequired       [][]string `json:"one_of_required,omitempty"`
}
type FieldErrors map[string]string // "config.<key>"|"credentials.<key>"|"config_overrides.<key>" → code (never a value)
type Mode int // ModeConfiguration (target-supplied allowed) | ModeEffective (everything required)
func CheckCapabilities(c Capabilities) error
func ValidateInput(c Capabilities, config, creds map[string]any, m Mode) (FieldErrors, []string /* target_supplied */)
func ValidateOverride(c Capabilities, config, override map[string]any) FieldErrors
func TargetSupplied(c Capabilities, config map[string]any) []string
func MissingRequired(c Capabilities, effective map[string]any) []string // labels
// Result gains (US8): Permanent bool — no retry, failure details stored in the job result
type JobMeta struct { TenantID, JobID, ConfigurationID, TargetID, Trigger string }
func WithJob(ctx context.Context, m JobMeta) context.Context
func JobFrom(ctx context.Context) (JobMeta, bool)
type ConfigValidator interface { ValidateConfig(config map[string]any) error }
```

Configuration view/input additions (US5, `internal/configs`):
`View.CredentialsSet []string`, `View.CredentialsPublic map[string]any`
(manage only), `View.TargetSupplied []string` (computed),
`Input.ClearCredentials []string`; targets (`internal/targets`): attach
validation per configuration, `View.MissingRequired map[configuration
id][]string` (computed); credentials remain one
sealed blob (`CredentialsSealed`, AD = configuration id), merged per field
on update. No migration.

### 2.3 Job result details (`DeploymentJob.Result`)

```json
{"message": "Installed on 18, unchanged on 1, queued for 1, failed on 0, unsupported on 0",
 "details": {"provider": "inventory-agent", "delivery_id": "…", "cert_name": "www",
   "counts": {"installed": 18, "unchanged": 1, "queued": 1, "failed": 0, "unsupported": 0, "superseded": 0},
   "hosts": [{"host_id": "…", "hostname": "web-7", "state": "pending", "reason": ""}],
   "hosts_truncated": false}}
```

## 3. Host (agent store)

See [contracts/agent-config.md](contracts/agent-config.md) for the
directory layout, metadata file and hook environment.
