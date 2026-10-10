# Contract: protobuf additions (inventory SDK, `inventory/v1/inventory.proto`)

All additions are wire-compatible (`buf breaking` against the released SDK).

## Commands (ingest stream)

```proto
enum CommandType { ...; COMMAND_TYPE_MODULE = 4; }   // next free value
message Command { ...; ModuleCommand module = 5; }    // set when type = MODULE

// ModuleCommand carries only references; the agent pulls the bundle with
// FetchModuleBundle (feature 037).
message ModuleCommand {
  string item_id = 1;   // uuid
  string module = 2;    // ^[a-z0-9][a-z0-9-]{0,62}$; re-validated by the agent
  uint32 attempt = 3;
}
```

Capability string announced by agents: `module.v1`.

## IngestService (agent edge, per-agent credential)

```proto
rpc FetchModuleBundle(FetchModuleBundleRequest) returns (stream ModuleBundleChunk);
rpc ReportModule(ReportModuleRequest) returns (ReportModuleResponse);

message FetchModuleBundleRequest { string item_id = 1; }

message ModuleBundleChunk {
  oneof part {
    ModuleBundleHeader header = 1;   // always first
    ArtifactChunk chunk = 2;         // <= 1 MiB each, offsets contiguous
  }
}

message ModuleBundleHeader {
  string item_id = 1;      // set by inventory toward the agent; "" from the source
  string module = 2;
  string version = 3;
  int64 size = 4;          // 1..12 MiB
  string sha256 = 5;       // lowercase hex of the zip
  string delivery_id = 6;  // gateway join id
}

// states: installed | failed | hook_failed
// reasons (closed set): invalid_module, invalid_bundle, checksum_mismatch,
// too_large, already_installed, write_failed, disk_full, hook_failed,
// hook_timeout, hook_refused, disabled_locally, busy, fetch_failed
message ReportModuleRequest {
  string item_id = 1;
  string state = 2;
  string reason = 3;
  int32 hook_exit_code = 4;  // -1 none; 0..255; 256 timeout
  string detail = 5;         // <= 256 bytes, sanitised, never hook output
}
message ReportModuleResponse { bool accepted = 1; }
```

## ModuleDeliveryService (mesh; inventory serves; caller in `module_delivery.sources`)

```proto
service ModuleDeliveryService {
  rpc CreateModuleDelivery(CreateModuleDeliveryRequest) returns (ModuleDelivery);
  rpc GetModuleDelivery(GetModuleDeliveryRequest) returns (ModuleDelivery);
  rpc ListModuleTargets(ListModuleTargetsRequest) returns (ListModuleTargetsResponse);
}

enum ModuleDeliveryState {
  MODULE_DELIVERY_STATE_UNSPECIFIED = 0; PENDING = 1; DELIVERED = 2; FETCHED = 3;
  INSTALLED = 4; FAILED = 5; HOOK_FAILED = 6; UNSUPPORTED = 7; SUPERSEDED = 8; EXPIRED = 9;
}   // values are prefixed MODULE_DELIVERY_STATE_

message CreateModuleDeliveryRequest {
  string tenant_id = 1;
  string delivery_id = 2;   // uuid from the source; idempotency key per (tenant, source)
  string host_id = 3;
  string module = 4;
  string version = 5;       // semver without "v", <= 64
  int64 expires_at = 6;     // unix seconds; item expires then (<= now + 7 days)
}
message GetModuleDeliveryRequest { string tenant_id = 1; string delivery_id = 2; }

message ModuleDelivery {
  string item_id = 1;
  string delivery_id = 2;
  string tenant_id = 3;
  string host_id = 4;
  string hostname = 5;
  string module = 6;
  string version = 7;
  ModuleDeliveryState state = 8;
  string reason = 9;          // closed set (agent reasons + no_agent, ambiguous_agent,
                              // not_supported_platform, upgrade_required, disabled_on_host,
                              // retired, fetch_budget, report_timeout, source_refused)
  int32 hook_exit_code = 10;
  bool agent_online = 11;
  int64 created_at = 12;
  int64 updated_at = 13;
  int64 expires_at = 14;
  bool created = 15;          // false = idempotent replay
}

message ListModuleTargetsRequest { string tenant_id = 1; string query = 2; int32 limit = 3; } // limit 1..200 (default 50); query = hostname substring <= 64
message ModuleTarget {
  string host_id = 1;
  string hostname = 2;
  string os_name = 3;
  bool agent_online = 4;
  // enabled | disabled_on_host | upgrade_required | not_supported_platform |
  // no_agent | ambiguous_agent | disabled_on_server
  string capability = 5;
  repeated string ip_addresses = 6;  // latest snapshot, no loopback/link-local, no prefix, <= 16
}
message ListModuleTargetsResponse { repeated ModuleTarget hosts = 1; bool truncated = 2; }
```

Errors: `FailedPrecondition` with the message exactly `disabled_on_server`
while `module_delivery.enabled` is false; `PermissionDenied` for a caller
outside `sources`; `InvalidArgument` with the message `invalid <field>`;
`NotFound` (`not_found`) for an unknown host or delivery;
`FailedPrecondition` whose message is exactly the reason token when the
host is not eligible (`no_agent`, `ambiguous_agent`,
`not_supported_platform`, `upgrade_required`, `disabled_on_host`,
`retired`) — no item is stored then. `version` must be a semantic version
without "v" (pre-release and build suffixes allowed, <= 64 bytes);
`expires_at` must lie in (now, now + 7 days].

## ModuleBundleSource (mesh; the gateway serves; inventory calls)

Defined in the inventory SDK so that neither side needs the other's SDK for
it.

```proto
service ModuleBundleSource {
  rpc RenderModuleBundle(RenderModuleBundleRequest) returns (stream ModuleBundleChunk);
}
message RenderModuleBundleRequest {
  string tenant_id = 1;
  string delivery_id = 2;
  string host_id = 3;
  string item_id = 4;    // for the source's audit only
}
```

The source sends the header (module, version, size, sha256, delivery_id;
item_id empty) then chunks of at most 1 MiB with contiguous offsets from 0.
Errors: `NotFound` for every refusal (unknown, expired, host or tenant
mismatch, render budget spent: no oracle), `PermissionDenied` for a wrong
caller, `Unavailable` when rendering fails. Inventory counts every fetch;
`NotFound` fails the item (`source_refused`) and the agent gets `NotFound`;
any other error or a malformed stream (wrong module, size or digest, gaps)
is `Unavailable` to the agent until the fifth fetch, which fails the item
(`fetch_budget`).
