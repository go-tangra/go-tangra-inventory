# Contract: agent configuration, certificate store and deploy hook (033)

## 1. Configuration (`/etc/inventory-agent/agent.yaml`, strict YAML)

New section (all keys optional; defaults shown). Unknown keys are rejected
(`KnownFields(true)`, as today). Validation errors refuse to start.

```yaml
# Certificates delivered by the platform (deployer provider "Inventory
# agent"). Files go only to `directory`; the server chooses only the
# certificate name. Nothing is executed unless deploy_hook is set here.
certificates:
  enabled: true
  directory: /etc/inventory-agent/certs
  owner: root
  group: root
  dir_mode: "0750"
  cert_mode: "0644"
  key_mode: "0600"
  keep_previous: 1
  deploy_hook: ""
  hook_timeout_seconds: 300
  allow_insecure_transport: false
```

| Key | Rule (startup validation) |
|---|---|
| `directory` | absolute, clean, not `/`, not under `/proc`, `/sys`, `/dev`, `/home`, `/root`, `/tmp` (systemd `ProtectHome`/`PrivateTmp` would hide it) |
| `owner`, `group` | user/group name `^[a-z_][a-z0-9_-]{0,31}$` or numeric id; existence checked per install (`owner_unknown`) |
| `dir_mode` | octal string, 0700–0755, no world write |
| `cert_mode` | octal, no group/world write, owner read |
| `key_mode` | `0600` or `0640` |
| `keep_previous` | 0–5 |
| `deploy_hook` | empty, or absolute clean path |
| `hook_timeout_seconds` | 30–1800 |
| `allow_insecure_transport` | needed to announce `cert.v1` when `insecure: true`; logs a warning at start |

The agent announces `cert.v1` iff `enabled`, `runtime.GOOS == "linux"`, and
(TLS or `allow_insecure_transport`). With `enabled: false` a CERTIFICATE
command is answered by `ReportCertificate(failed, disabled_locally)` without
fetching.

`packaging/agent.yaml` ships the section commented out with these
defaults. The systemd unit is unchanged: it has no `ProtectSystem`, so
`/etc/inventory-agent/certs` is writable; if `ProtectSystem=` is ever
added, `ReadWritePaths=/etc/inventory-agent/certs` must be added with it
(test `packaging_test.go` asserts the pairing).

## 2. Store layout

```text
<directory>/                                   dir_mode, root:<group> (all directories)
├── live/                                      dir_mode
│   └── <name> -> ../archive/<name>/<gen>      symlink (atomically replaced)
├── archive/                                   dir_mode
│   └── <name>/                                dir_mode
│       └── <gen>/                             dir_mode (0700 while staging)
│           ├── cert.pem                       cert_mode   leaf only
│           ├── chain.pem                      cert_mode   intermediates ("" file when none)
│           ├── fullchain.pem                  cert_mode   leaf + intermediates
│           └── privkey.pem                    key_mode    PKCS#8/SEC1/PKCS#1 as delivered (absent: certificate_only without existing key)
└── renewal/                                   dir_mode
    └── <name>.json                            cert_mode   metadata
```

- `<name>`: `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` without `..`
  (`certmaterial.ValidName`); otherwise `failed/invalid_name`.
- `<gen>`: `YYYYMMDDTHHMMSSZ-<first 16 hex of serial>`.
- Directories are owned by `root:<group>`; files by `<owner>:<group>`
  (so a service user such as `nginx` can read its key via the group
  without being able to modify the store).
- Consumers reference `live/<name>/fullchain.pem` and
  `live/<name>/privkey.pem` (certbot-compatible paths).
- Directories are opened with `O_NOFOLLOW`; a pre-existing `live`,
  `archive` or `renewal` that is a symlink, or not owned by uid 0, makes
  the install fail with `write_failed` (detail `unsafe_directory`).

### Install algorithm

1. Validate name and bundle (`certmaterial.ParseBundle`: PEM, leaf,
   ≤ 10 chain certs, sizes, key ↔ leaf, `now ∈ [not_before, not_after]`).
2. Idempotency (research D11): metadata fingerprint == bundle fingerprint
   and file hashes, key match, owner and modes OK → `unchanged` (no write,
   no hook) unless `rerun_hook`; owner/mode drift only → fix, `unchanged`.
3. Free-space check (≥ 4 × bundle size) → `disk_full`.
4. Stage `archive/<name>/<gen>/` (0700): each file `O_CREAT|O_EXCL|O_WRONLY`,
   write, `fchmod`, `fchown`, `fsync`; `certificate_only`: copy the current
   generation's `privkey.pem` if it matches the leaf, else `key_mismatch`.
   fsync the generation directory, `chmod` to `dir_mode`.
5. `symlink("../archive/<name>/<gen>", "live/.<name>.tmp")`,
   `rename("live/.<name>.tmp", "live/<name>")`, fsync `live/`.
6. Metadata `renewal/<name>.json` via `renewal/.<name>.json.tmp` + rename.
7. Prune generations older than `keep_previous` (never the target of
   `live/<name>`).
8. Run the hook (§4) when configured; report.

Crash recovery at start: generations newer than the one `live/<name>`
points to are removed; leftover `.tmp` entries are removed.

## 3. Metadata `renewal/<name>.json`

v3 field names (go-tangra-client `CertMetadata`) plus v4 identifiers:

```json
{
  "name": "www",
  "common_name": "www.example.com",
  "serial_number": "4f3a…",
  "fingerprint": "9c1d…(sha256 hex)",
  "issued_at": "2026-10-02T08:00:00Z",
  "expires_at": "2026-12-31T08:00:00Z",
  "last_updated": "2026-10-02T08:00:05Z",
  "issuer_name": "Freya Issuing CA",
  "dns_names": ["www.example.com"],
  "ip_addresses": [],
  "previous_serial": "11aa…",
  "renewal_count": 3,
  "last_hook_execution": "2026-10-02T08:00:06Z",
  "certificate_id": "…lcm id…",
  "item_id": "…delivery item id…",
  "generation": "20261002T080005Z-4f3a…",
  "has_key": true,
  "hook_exit_code": 0
}
```

## 4. Deploy hook

- Runs only when `deploy_hook` is set **locally**, after a new or replaced
  installation (or `rerun_hook`); never for `unchanged`, never on failure
  before the swap.
- Checks before each run (else `hook_refused`, hook exit code −1): `lstat`
  is a regular file (no symlink), uid 0, mode has no group/other write,
  executable by owner; the path is absolute and its directory and every
  ancestor up to `/` are uid 0 directories without group/other write (a
  writable ancestor would let a local user rename the path and substitute
  the hook), so no hook under `/tmp` or a user's tree.
- `exec` of the file itself — no shell, no arguments; working directory
  `live/<name>`; new process group; `hook_timeout_seconds` then SIGTERM,
  5 s, SIGKILL to the group (`hook_timeout`, exit code reported as 256);
  stdin `/dev/null`; stdout+stderr captured up to 4 KiB into the agent
  log only.
- Environment — exactly:

| Variable | Value |
|---|---|
| `PATH` | `/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin` |
| `LANG` | `C.UTF-8` |
| `LCM_CERT_NAME` | `<name>` |
| `LCM_CERT_DIR` | `<directory>/live/<name>` |
| `LCM_CERT_PATH` | `<directory>/live/<name>/cert.pem` |
| `LCM_KEY_PATH` | `<directory>/live/<name>/privkey.pem` (empty when no key) |
| `LCM_CHAIN_PATH` | `<directory>/live/<name>/chain.pem` |
| `LCM_FULLCHAIN_PATH` | `<directory>/live/<name>/fullchain.pem` |
| `LCM_COMMON_NAME` | leaf subject CN |
| `LCM_DNS_NAMES` | comma-separated DNS SANs |
| `LCM_IP_ADDRESSES` | comma-separated IP SANs |
| `LCM_SERIAL_NUMBER` | lowercase hex serial |
| `LCM_EXPIRES_AT` | RFC 3339 UTC |
| `LCM_IS_RENEWAL` | `true` / `false` |
| `LCM_CERTIFICATE_ID` | lcm certificate id (new in v4), validated `[A-Za-z0-9._:-]{1,128}` by inventory and the agent |

Values derived from the certificate are taken from the agent's own parse
of the PEM (not from server-supplied strings), and contain no control
characters.
- Exit 0 → `installed`; non-zero → `hook_failed/hook_failed` with the exit
  code; files stay installed (the previous generation remains available
  for manual restore: `ln -sfn ../archive/<name>/<prev> live/<name>`).

## 5. Agent log lines (no material)

`certs: item <id> name <name> installed serial <s> fingerprint <f>
(hook exit 0)` / `unchanged` / `failed: <reason>`. PEM content, key bytes
and hook output beyond the 4 KiB local excerpt are never logged.
