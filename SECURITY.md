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

## Other secrets

Per-agent credentials and enrollment tokens are sealed with the platform
key-encryption key and never logged, audited or exported. The ingest edge is
off-mesh and authenticates every call with the agent credential over TLS; see
[deploy/README.md](deploy/README.md#security-notes).
