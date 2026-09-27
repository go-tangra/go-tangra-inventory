# Security and constitution review (T110)

**Date**: 2026-09-27 · **Scope**: inventory `023-hardware-agent-upgrade`, ipam `023-device-hardware`
· **Second reviewer** (independent pass, read-only): `internal/agentrelease`, `internal/selfupdate`,
`internal/upgrader`, `internal/ingest/upgrade.go`, `internal/releases`, `internal/upgrades`,
`internal/httpapi/upgrades.go`, `cmd/agent-release`, CI `sign`/`docker`/`release` jobs,
`scripts/check-release-binary.sh`, ipam `internal/hostplan/hardware.go` and `internal/hostreport/hardware.go`.

## Findings and resolution

| Severity | Finding | Resolution |
|---|---|---|
| HIGH | The automatic policy could send an unproven release to `max_concurrent` (up to 100) agents in one tick; the pause only follows the first failure. STRIDE claimed "one host, then all". | Fixed: `PlanAuto` takes a `proven` flag (some agent of the tenant runs the target); unproven targets go to one agent at a time. Tests `TestPlanAuto` (canary), `TestSchedulerCanary`. STRIDE row updated. |
| MEDIUM | `Apply` checked the state file but not the staging directory it lives in. | Fixed: `FS.StatDir` (Lstat, no symlink); `Apply` refuses a staging directory that is not root/SYSTEM-owned and private (`ErrStagingMode`). Tests in `TestApplyRefusals`, `TestOSFS`. |
| MEDIUM (found while fixing the above) | Windows: the state-file check compared the Unix mode (0600), which Windows never reports, so every Windows helper run would have been refused. `MkdirAll(0700)` set no ACL, and ProgramData lets Users create files. | Fixed: `FileInfo.Private` is platform-specific (Unix: no group/other bits; Windows: every allow ACE is SYSTEM or Administrators); `OSFS.MkdirAll` sets a protected SYSTEM/Administrators-only DACL on the staging directory on Windows. Windows code vetted with `GOOS=windows`; not run on a Windows host (T117). |
| LOW | `ExecRunner` appends `env` to the inherited environment; safe only while callers pass literals. | Comment added; callers pass fixed literals only. |
| LOW | git-describe versions (`4.3.1~11-gabc`) sort before their base release. | Intended (never automatic, refused below the floor); covered by `FuzzVersion`. |
| LOW | ipam hardware audit details carry reported strings. | Accepted: cleaned of control characters and bounded to 256 bytes. |
| — | Fuzzing (`make fuzz`) found an unbounded joined port label in the SMBIOS decoder (`"in / ext"` > 256 bytes). | Fixed with `clipString`; crasher kept in `testdata/fuzz/FuzzSMBIOSStructures`, regression in `TestSmallEdges`. |

No CRITICAL findings. The CI signing secret is only in the `release` environment, read only by the tag-only `sign`
job and unset after use; only the public keyring is a build argument.

## STRIDE (research.md) re-checked against the code

All rows implemented; the DoS "fleet-wide broken release" row was partial (see HIGH) and is now implemented.

## Constitution (plan.md principles)

- **I. Secure by Default**: automatic upgrades off; builds without a keyring trust nothing; `agentupgrades:manage` least privilege.
- **II. Zero Trust**: downloads only over the authenticated ingest edge; artifacts verified regardless of the channel; request ownership checked per agent and tenant.
- **III. Boundary Validation**: manifest ≤ 64 KiB, artifacts ≤ 150 MiB, batches ≤ 1000, policy body ≤ 4 KiB, strict JSON, closed state/reason sets, SMBIOS/sysfs bounds (fuzzed).
- **IV. Test-First**: tests before code in every phase; 100 % for the security set; negative, fuzz, integration and systemd container e2e tests.
- **V. Observability**: transactional audit for every request, transition, refusal, policy change and import; `inventory.upgrades` metric; content-free realtime events.
- **VI. Supply Chain**: Ed25519 from the standard library, signing key only in the protected CI environment, SBOM for the agent artifacts, `make release-check`, `make vuln` clean.
- **VII. Simplicity**: one SMBIOS decoder, one selfupdate core behind small OS interfaces, releases stored in PostgreSQL (no new service).

All satisfied.
