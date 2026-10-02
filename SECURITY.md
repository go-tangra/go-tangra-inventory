# Security policy

Report vulnerabilities privately through GitHub security advisories of this
repository (Security > Report a vulnerability), not in public issues.

## Agent release signing key

Every agent the platform installs on a host is verified against the Ed25519
release keys compiled into the agent and the server. Whoever holds a trusted
private key can run code as root on every managed host, so the key is handled
as a production root secret.

### Custody

- The private key (base64 seed) exists only as the secret
  `AGENT_RELEASE_SIGNING_KEY` of the GitHub environment **`release`** and in an
  offline backup (encrypted, two custodians, outside GitHub).
- The `release` environment requires reviewers and only deploys `v*` tags; the
  CI `sign` job is the only job that reads the secret. It never prints it and
  passes it to `agent-release sign` by environment variable name only (the tool
  refuses a key given as a flag value).
- The public keyring (`<key id>:<base64>`, comma-separated) is the repository
  variable `AGENT_RELEASE_PUBLIC_KEYS`, injected at build time into
  `inventorysvc` and `inventory-agent`. It is public; changing it changes what
  every future build trusts, so it is changed only through this procedure.
- Test and development keys are generated on the fly (`make
  agent-release-dev`, `.dev/agent-release/`, key ids `dev-*`) and are never
  committed. `make release-check` refuses release binaries that carry a `dev-`
  key or no keyring.

### Generation

On a trusted machine:

```bash
go run ./cmd/agent-release keygen -key-id release-2026 -out-private release-2026.key > release-2026.pub
```

Store `release-2026.key` as the environment secret, make the offline backup,
then delete the file. Store `release-2026.pub` in `AGENT_RELEASE_PUBLIC_KEYS`.

### Rotation

1. Generate the new key with a new key id (for example `release-2027`).
2. Append its public key to `AGENT_RELEASE_PUBLIC_KEYS`
   (`release-2026:…,release-2027:…`) and release a version still signed with
   the old key. Agents that install it trust both keys.
3. When the fleet view shows every agent on that version (or later), replace
   the secret with the new private key; releases are now signed with the new
   key id.
4. Remove the old public key from the variable only after no agent runs a
   version that trusts the old key alone, and destroy the old key and its
   backup.

### Compromise

If the private key may be exposed:

1. Delete the secret from the `release` environment and pause automatic
   upgrades in every tenant (Inventory > Agents > Automatic upgrades, disable).
2. Audit what was signed: GitHub releases, `agent_release_imported` audit rows
   and the releases stored on each platform (`GET
   /api/inventory/v1/agent-releases`). Delete unknown releases from the
   bundle/import sources and stop serving them.
3. Generate a new key (new key id), set the keyring variable to the new public
   key only, and build a release signed with it. Agents still trusting the
   compromised key must be upgraded to this release by hand or through the
   platform before the old key is considered retired; hosts that may have run
   an attacker-signed agent are treated as compromised.
4. Rotate the ingest TLS certificate and revoke/re-enroll agents if the
   investigation shows the platform itself was reached.

## Delivered certificate keys (feature 033)

Inventory relays lcm certificates to agents for the deployer's
`inventory-agent` provider; a private key is the most sensitive data that
passes through the service:

- **Never stored by the platform outside lcm.** The deployer sends only
  references (certificate id, name, host selection) and never fetches a key
  for this provider. Inventory downloads the bundle from lcm only when the
  item's own agent pulls it, keeps the key in memory for that one call
  (byte copies are wiped after the response), and never writes it to the
  database, Valkey, the connection registry, events, audit rows or logs.
  Commands carry ids only. Tests scan database dumps, captured logs, audit
  rows and Valkey keys/streams for `PRIVATE KEY` and the test key's bytes
  after an end-to-end run.
- **Only to the item's own agent.** A fetch must come from the agent the
  item was created for (same tenant, active item, at most 5 fetches, inside
  the delivery window); anything else is `NotFound` and audited as
  `cert_delivery_refused`. A host claimed by more than one non-revoked agent
  receives nothing (`ambiguous_agent`) — an agent credential cannot attract
  another host's key by reporting that host's identity.
- **Only over TLS.** Agent and server refuse delivery over a plaintext ingest
  edge unless both opt out for development; the server refuses the opt-out
  with `env: production`.
- **On the host** the key is written only below the agent's local
  `certificates.directory`, as `privkey.pem` with `key_mode` (0600 or 0640,
  owner root by default) in a generation directory staged at 0700; the
  server chooses only the certificate name (one safe path component). No
  directory, owner, mode, command or environment comes from the server; a
  deploy hook runs only if configured locally and owned by root along its
  whole path.
- **key_policy `certificate_only`** delivers no key: the agent keeps an
  existing matching `privkey.pem` or fails with `key_mismatch`.

If a host or agent credential is compromised: revoke the agent (its queued
deliveries are cancelled), treat every key delivered to that host as
compromised (revoke and re-issue in lcm; revocation flags the host
certificate and cancels queued deliveries of it) and re-enroll the host.

## Other secrets

Per-agent credentials, enrollment tokens and auto-enrollment key secrets are
sealed with the platform key-encryption key and never logged, audited or
exported. An auto-enrollment key (feature 029) is never sent by agents: they
present an HMAC-SHA256 proof bound to the key id, a timestamp (±5 min), a
single-use nonce and their identity, accepted only from the key's networks
while the tenant switch and the key are enabled. A leaked key secret is
contained by its networks, expiry and limit; disable or rotate it in
Inventory > Agents > Automatic enrollment (agents already enrolled keep their
own credentials, revoke them separately if needed). The ingest edge is
off-mesh and authenticates every call with the agent credential over TLS; see
[deploy/README.md](deploy/README.md#security-notes).
