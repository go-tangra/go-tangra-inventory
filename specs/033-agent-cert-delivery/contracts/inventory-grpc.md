# Contract: inventory gRPC — certificate delivery (033)

All changes are additive to `sdk/api/proto/inventory/v1/inventory.proto`
and released as SDK **`sdk/v4.4.0`**; `buf breaking` against `sdk/v4.3.0`
must pass.

## 1. Mesh service (deployer → inventory)

Served on the inventory module gRPC listener (SPIFFE mTLS), **not**
gateway-proxied. Authorized by mesh policy (rule `deployer-cert-delivery`,
[mesh-policy.md](mesh-policy.md)) and, in the handler, by
`cert_delivery.sources` (service name from the verified SPIFFE peer).
All methods return `FailedPrecondition "certificate delivery is disabled"`
when `cert_delivery.enabled = false`. Tenant = `tenant_id` of the request
(required, uuid); actor = the SPIFFE service.

```proto
// CertificateDeliveryService relays lcm certificates to inventory agents
// (feature 033). Callers pass references only; no message of this service
// ever carries certificate or key material.
service CertificateDeliveryService {
  rpc CreateCertificateDelivery(CreateCertificateDeliveryRequest) returns (CertificateDelivery);
  rpc GetCertificateDelivery(GetCertificateDeliveryRequest) returns (CertificateDelivery);
  rpc PreviewCertificateTargets(PreviewCertificateTargetsRequest) returns (PreviewCertificateTargetsResponse);
  rpc VerifyHostCertificates(VerifyHostCertificatesRequest) returns (VerifyHostCertificatesResponse);
  rpc MarkCertificateRevoked(MarkCertificateRevokedRequest) returns (MarkCertificateRevokedResponse);
}

message HostSelector {
  repeated string host_ids = 1;   // <= 1000 uuids
  repeated string host_tags = 2;  // <= 16, "key" or "key=value"; all must match
}

message CreateCertificateDeliveryRequest {
  string tenant_id = 1;
  string idempotency_key = 2;     // deployer job id, 1..128
  string configuration_id = 3;    // <= 128
  string target_id = 4;           // <= 128, "" for direct jobs
  string trigger = 5;             // manual | auto_deploy | retry
  string certificate_id = 6;      // lcm certificate id
  string name = 7;                // ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$, no ".."
  string key_policy = 8;          // require | certificate_only
  HostSelector selector = 9;      // at least one id or tag
  bool rearm_failed = 10;         // existing delivery: re-arm failed|hook_failed|expired items
}

message GetCertificateDeliveryRequest {
  string tenant_id = 1;
  string id = 2;
}

enum DeliveryState {
  DELIVERY_STATE_UNSPECIFIED = 0;
  DELIVERY_STATE_PENDING = 1;
  DELIVERY_STATE_DELIVERED = 2;
  DELIVERY_STATE_FETCHED = 3;
  DELIVERY_STATE_INSTALLED = 4;
  DELIVERY_STATE_UNCHANGED = 5;
  DELIVERY_STATE_FAILED = 6;
  DELIVERY_STATE_HOOK_FAILED = 7;
  DELIVERY_STATE_UNSUPPORTED = 8;
  DELIVERY_STATE_SUPERSEDED = 9;
  DELIVERY_STATE_EXPIRED = 10;
  DELIVERY_STATE_CANCELLED = 11;
}

message CertificateDeliveryItem {
  string id = 1;
  string host_id = 2;
  string hostname = 3;
  bool agent_online = 4;
  DeliveryState state = 5;
  string reason = 6;              // closed set (data-model §1.1)
  string serial = 7;
  string fingerprint_sha256 = 8;
  int32 hook_exit_code = 9;       // -1 = none
  int32 attempts = 10;
  int64 updated_at = 11;          // unix seconds
}

message CertificateDelivery {
  string id = 1;
  string tenant_id = 2;
  string certificate_id = 3;
  string name = 4;
  string key_policy = 5;
  int64 created_at = 6;
  int64 expires_at = 7;
  repeated CertificateDeliveryItem items = 8;  // <= 1000
  repeated string unknown_host_ids = 9;        // requested ids not found in the tenant (no detail)
  bool created = 10;                           // false = idempotent replay
}

message PreviewCertificateTargetsRequest {
  string tenant_id = 1;
  HostSelector selector = 2;
}

message TargetHost {
  string host_id = 1;
  string hostname = 2;
  string os_name = 3;
  map<string, string> tags = 4;
  bool agent_online = 5;
  string capability = 6;          // enabled | disabled_on_host | upgrade_required | not_supported_platform | no_agent | disabled_on_server | ambiguous_agent
}

message PreviewCertificateTargetsResponse {
  repeated TargetHost hosts = 1;  // <= 1000
  repeated string unknown_host_ids = 2;
  bool truncated = 3;             // selection exceeded 1000 hosts
}

message VerifyHostCertificatesRequest {
  string tenant_id = 1;
  HostSelector selector = 2;
  string name = 3;
  string expected_fingerprint_sha256 = 4;   // lowercase hex
}

message HostCertificateStatus {
  string host_id = 1;
  string hostname = 2;
  string status = 3;              // match | mismatch | failed | pending | missing | revoked
  string fingerprint_sha256 = 4;  // last reported
  string serial = 5;
  string reason = 6;
  int64 last_delivered_at = 7;
}

message VerifyHostCertificatesResponse {
  repeated HostCertificateStatus hosts = 1;
  int32 matched = 2;
  int32 total = 3;
}

message MarkCertificateRevokedRequest {
  string tenant_id = 1;
  string certificate_id = 2;
}

message MarkCertificateRevokedResponse {
  int32 cancelled_items = 1;
  int32 flagged_hosts = 2;
}
```

### Server rules

- **Create**: validates every field (InvalidArgument with field name);
  `certificate_id` must match `[A-Za-z0-9._:-]{1,128}` (it reaches the
  agent's hook environment); `(tenant, source, idempotency_key)` exists →
  returns it (`created=false`), re-arming items when `rearm_failed` — but a
  key reused for another `certificate_id`, `name` or `key_policy` →
  InvalidArgument `idempotency_key`; a host claimed by more than one
  non-revoked agent becomes `unsupported/ambiguous_agent` (nothing pushed,
  nothing fetchable); otherwise resolves the selection
  under RLS (explicit ids ∪ non-retired hosts matching **all** tags; > 1000
  → InvalidArgument `too_many_hosts`), fetches the certificate's metadata
  from lcm (`Download(include_key=false)`: not_after, serial, CN; NotFound →
  `FailedPrecondition certificate_not_found`; revoked/expired →
  `FailedPrecondition`), creates the delivery and items in one
  transaction (supersedes older active items per host+name; applies
  `older_than_installed` for non-manual triggers; marks `unsupported` items
  per capability table), audits, commits, then pushes commands to online
  agents. 0 resolved hosts → item list empty (the provider fails the job).
- **Get**: tenant-scoped; NotFound for other tenants.
- **Preview**: same resolution as Create, no writes.
- **Verify**: resolves the selection; per host reads
  `inventory_host_certificates(host, name)`: `match` when the reported
  fingerprint equals the expected and state ∈ {installed, unchanged} and
  not revoked; `revoked` when `revoked_at` set; `pending` when an active
  item exists; `failed` when the last state is failed/hook_failed;
  `missing` otherwise.
- **MarkCertificateRevoked**: cancels active items with that certificate
  id (`cancelled/certificate_revoked`), sets `revoked_at` on host
  certificates holding it, audits; idempotent.
- Errors never reveal hosts or certificates of other tenants.

## 2. Ingest edge (agent → inventory, per-agent credential)

```proto
enum CommandType {
  // … existing 0-2
  // Install the certificate of delivery item Command.certificate.item_id
  // (feature 033). Agents older than 033 log and ignore it.
  COMMAND_TYPE_CERTIFICATE = 3;
}

message Command {
  // … existing 1-3
  CertificateCommand certificate = 4;   // set when type = CERTIFICATE
}

// CertificateCommand carries only references; the agent pulls the material
// with FetchCertificate over its authenticated connection.
message CertificateCommand {
  string item_id = 1;                   // uuid
  string name = 2;                      // validated again by the agent
  uint32 attempt = 3;                   // the item's attempt; a re-armed item is a new attempt the agent must not dedupe away
}

service IngestService {
  // … existing
  // Certificate delivery (feature 033), authenticated like every method
  // except Enroll; bound to the verified agent's own delivery items.
  rpc FetchCertificate(FetchCertificateRequest) returns (CertificateBundle);
  rpc ReportCertificate(ReportCertificateRequest) returns (ReportCertificateResponse);
}

message FetchCertificateRequest {
  string item_id = 1;
}

// CertificateBundle is the only message that carries a private key outside
// lcm. It is never logged, stored, cached or published by the inventory.
message CertificateBundle {
  string item_id = 1;
  string name = 2;
  string certificate_id = 3;
  string cert_pem = 4;                  // leaf, <= 64 KiB
  string chain_pem = 5;                 // intermediates, <= 256 KiB, <= 10 certs
  string key_pem = 6;                   // "" when key_policy = certificate_only; <= 16 KiB
  bool has_key = 7;
  string serial = 8;
  string fingerprint_sha256 = 9;        // of the leaf DER, lowercase hex
  string common_name = 10;
  repeated string dns_names = 11;       // <= 100
  repeated string ip_addresses = 12;    // <= 100
  int64 not_before = 13;                // unix seconds
  int64 not_after = 14;
  bool is_renewal = 15;                 // host had a different certificate under name
  bool rerun_hook = 16;                 // re-armed after hook_failed: run the hook even if unchanged
}

message ReportCertificateRequest {
  string item_id = 1;
  string state = 2;                     // installed | unchanged | failed | hook_failed
  string serial = 3;
  string fingerprint_sha256 = 4;        // of the leaf as written on disk
  string reason = 5;                    // closed set (data-model §1.1, agent reasons)
  int32 hook_exit_code = 6;             // -1 = hook not run; 0..255; 256 = timeout
  string detail = 7;                    // <= 256 bytes, sanitised; never hook output
}

message ReportCertificateResponse {
  bool accepted = 1;                    // false = ignored (terminal/duplicate)
}
```

`StreamRequest.capabilities` gains `cert.v1` (no proto change).

### Server rules (ingest)

- `FetchCertificate`: agent from context; item must belong to
  `(agent.TenantID, agent.ID)` and be `pending|delivered|fetched` with
  `fetches < 5` and its delivery's `expires_at` not yet passed (even before
  the sweeper runs), else `NotFound` (audited `cert_delivery_refused`). Plaintext
  ingest without `allow_plaintext_ingest` → `FailedPrecondition`. One
  concurrent fetch per agent, `max_concurrent_fetches` per replica →
  `ResourceExhausted`. Calls lcm `Download(tenant, certificate_id,
  include_key = key_policy == require)` with `lcm_timeout_seconds`;
  lcm "no stored private key" → item `failed/key_unavailable`,
  `FailedPrecondition`; lcm NotFound → `failed/certificate_not_found`;
  lcm status revoked/expired → `failed/certificate_revoked|expired`; lcm
  unavailable → `Unavailable` (item unchanged, agent retries).
  Validates with `certmaterial.ParseBundle` (`failed/bundle_too_large` or
  `invalid_bundle`). Sets `fetched`, `fetches+1`, `serial`, `not_after`,
  audits `cert_delivery_fetched` (no material), sends, zeroes the key
  buffer.
- `ReportCertificate`: own item only; state `installed|unchanged` requires
  the fingerprint to equal the fingerprint served at fetch (else
  `failed/fingerprint_mismatch`, audited); transitions per data-model §1.4;
  updates `inventory_host_certificates`; audits; publishes the realtime
  event.
- `StreamCommands`: after the upgrade replay, sends up to 50 CERTIFICATE
  commands for the agent's active items (oldest first) and marks pending
  ones delivered; when the agent lacks `cert.v1`, its pending items become
  `unsupported/no_capability` instead.
- Registry: `registry.Command{Type: "certificate", Certificate:
  &CertificatePayload{ItemID, Name}}` — ids only.

## 3. SDK helper (`sdk/pkg/inventoryclient/certdelivery.go`)

Plain-Go wrapper used by the deployer:

```go
type DeliveryRequest struct { TenantID, IdempotencyKey, ConfigurationID, TargetID, Trigger,
    CertificateID, Name, KeyPolicy string; HostIDs, HostTags []string; RearmFailed bool }
type Delivery struct { ID, CertificateID, Name string; Created bool; Items []DeliveryItem; UnknownHostIDs []string }
type DeliveryItem struct { ID, HostID, Hostname, State, Reason, Serial, Fingerprint string; AgentOnline bool; HookExitCode, Attempts int; UpdatedAt time.Time }
func (c *Client) CreateCertificateDelivery(ctx, DeliveryRequest) (Delivery, error)
func (c *Client) GetCertificateDelivery(ctx, tenantID, id string) (Delivery, error)
func (c *Client) PreviewCertificateTargets(ctx, tenantID string, ids, tags []string) (Preview, error)
func (c *Client) VerifyHostCertificates(ctx, tenantID string, ids, tags []string, name, fingerprint string) (Verification, error)
func (c *Client) MarkCertificateRevoked(ctx, tenantID, certificateID string) (int, int, error)
```

States are returned as the lowercase strings of data-model §1.1.
