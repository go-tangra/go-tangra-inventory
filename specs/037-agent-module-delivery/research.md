# Research: Agent Module Delivery

## R1. Pull at fetch time, render at fetch time

**Decision**: inventory stores references only; when the agent fetches,
inventory calls the gateway's `RenderModuleBundle`, which mints the join
token and renders the bundle then. Nothing secret exists before the host
asks for it, an offline host never wastes a token, and no service stores
material (033 pattern, with the gateway as the system of record instead of
lcm).

**Rejected**: gateway renders at create time and inventory keeps the sealed
zip until fetch (secrets at rest in a second service; token clock starts
while the host may be offline).

## R2. Where the source contract lives

**Decision**: `ModuleBundleSource` is declared in the inventory SDK; the
gateway implements it. One SDK release (inventory `sdk/v4.5.0`) serves both
repositories; the gateway SDK is unchanged.

## R3. Transport size

Bundles are ≤ 8 MiB (framework `MaxBundleBytes`) before rendering; the
rendered zip is capped at 12 MiB. Both hops stream `ModuleBundleChunk`
(header + ≤ 1 MiB chunks, as `DownloadAgentRelease`), so no gRPC message
limit is raised. Inventory buffers the whole stream (≤ 12 MiB) to check
size and SHA-256 before forwarding, then zeroes the buffer.

## R4. Trust in the enrolled agent

The agent's credential is issued at enrolment and verified on every call;
the item is bound to the host's single non-revoked agent at create and
re-checked at fetch (033 `resolve` rules). Inventory forwards the
**verified** host id to the gateway, which compares it with the join. A
compromised inventory could request a bundle for any agent join it is told
about, but only through the mesh identity the gateway checks, and only for
joins an administrator created.

## R5. Never overwrite

A second delivery to a host that already has `<dir>/<module>` would
generate new store passwords against existing volumes. The agent refuses
(`already_installed`); upgrades are a separate feature.

## R6. Hook

Reuse the 033 hook checks and runner. Environment `MODULE_NAME`,
`MODULE_VERSION`, `MODULE_DIR`; working directory `MODULE_DIR`. The
example hook runs `docker compose up -d`.

## R7. Pre-fill

The targets list carries the latest snapshot's non-loopback, non-link-local
addresses. The wizard pre-fills inputs named `*_HOST` with the hostname and
`*_IP` with the first address; the administrator can change them, and the
gateway validates them against the entry's patterns as in 036.

## STRIDE

| Threat | Mitigation |
|---|---|
| Spoofing: another agent fetches | item bound to the host's agent; NotFound + audit otherwise |
| Spoofing: rogue service renders | gateway accepts only the inventory SPIFFE id; join must be agent channel, host/tenant match |
| Tampering: path traversal / links in zip | SR-005 archive checks, os.Root, O_EXCL\|O_NOFOLLOW |
| Tampering: server chooses command | hook only from local config |
| Repudiation | audit on create, render, fetch, report, refusal |
| Information disclosure | no material in commands/DB/logs/audit; TLS edge; zeroed buffers |
| DoS | 5 renders, 5 fetches, concurrency cap, size caps, one item at a time on the agent |
| Elevation | token names only `svc/<module>`; agent writes only under its directory |
