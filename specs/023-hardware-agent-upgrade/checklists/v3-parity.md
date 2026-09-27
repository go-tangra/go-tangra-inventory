# v3 parity checklist: hardware facts and agent self-upgrade

**Feature**: 023 · **Checked**: 2026-09-27 · Each v3 capability maps to the task that delivers it
and the test that proves it (inventory repo unless marked *ipam*).

## Hardware facts (v3 host client → IPAM device page)

| v3 capability | v4 | Tasks | Proof (tests) |
|---|---|---|---|
| BIOS vendor, version, release date | `bios.vendor/version/release_date`, last type-0 structure wins | T032, T038, T039 | `internal/agentfacts` TestSystemBoardChassisBIOS, TestNode1Fixture (AMI 2.5 11/26/2025); *ipam* TestHardwareBIOSChange |
| Board vendor and serial | `baseboard.manufacturer/serial_number` | T032, T038, T039 | TestSystemBoardChassisBIOS, TestNode1Fixture; *ipam* TestHardwarePlaceholderSerialsNotKeys |
| System vendor, product and serial | `system.manufacturer/product_name/serial_number/uuid` | T032, T038, T039 | TestNode1Fixture (Supermicro Super Server), TestSplitMatchesGoSMBIOS (UUID byte order) |
| Chassis type | `chassis.type` + bootup state, DSP0134 table (no off-by-one) | T032, T038 | TestEnumTables, TestGoSMBIOSShiftReproduced |
| CPU model and count | every socket: version, family (full table incl. 0xFE extension), cores/threads enabled | T032, T038 | TestProcessorDecoding, TestProcessorFamilyTableComplete, TestNode1Fixture (2× Xeon Silver 4310 12C/24T); *ipam* TestHardwareProcessorsAndScalars |
| Total memory | sum of populated module sizes (extended size, 32 GiB+ modules) | T032, T038 | TestMemoryDeviceSizesAndSpeeds, TestMemoryArraysAndTotals (256 GiB node-1) |
| Memory type (DDR4/DDR5) | `memory.modules[].type`, type detail, form factor from corrected tables | T032, T038 | TestEnumTables, TestTypeDetailBits, TestNode1Fixture (DDR4 RDIMM, not "LPDDR3/TSOP") |
| Memory speed | rated and configured speed (MT/s), extended speed fields | T032, T038 | TestMemoryDeviceSizesAndSpeeds (3200 rated / 2666 configured) |
| (new) memory array, ECC, slots incl. empty | arrays: location, use, ECC, max capacity, device count; all slots | T032, T038, T041 | TestMemoryArraysAndTotals (System board / System memory / Single-bit ECC, 12 TB, 16 devices); *ipam* TestHardwareMemorySlots, TestHardwareDuplicateLocators |
| Disks: name | `disks[].name` (sda, nvme0n1; Windows PhysicalDriveN) | T042, T044, T048 | TestBlockDevicesNode1, TestParseWindowsPhysicalDisks |
| Disks: SSD/HDD | `media` ssd/hdd/nvme from rotational + transport | T042, T048 | TestBlockDevicesLab, TestInterfaceFromPath, TestParseWindowsPhysicalDisks |
| Disks: model | `disks[].model` (bounded, cleaned) | T042, T048 | TestBlockDevicesNode1, TestAttributeReadsCapped |
| Disks: size | `disks[].size_bytes`, dashboard total counts physical disks | T042, T046, T048 | TestBlockDevicesNode1, `internal/repo/repodb` TestStatsPhysicalDiskTotal; *ipam* TestHardwareDisks |
| (new) filesystem → disk | filesystems resolved to their disk through holders/partitions | T043, T048 | TestResolveFilesystemDisks, TestResolveDepthAndCycles |
| Reaches IPAM | host report `hardware` (schema ≥ 2) → IPAM device Hardware tab | T011, T020, T052–T062 | `internal/hostreport` projection tests; *ipam* TestHardwareFirstReport, TestHardwareNeverTouchesDeviceColumns |

## Agent self-upgrade

| v3 capability | v4 | Tasks | Proof (tests) |
|---|---|---|---|
| Manual `update --check` | `inventory-agent update -check` (exit 0 / 10 / 1 / 2) | T075, T094 | `cmd/inventory-agent` TestUpdateCheckExitCodes, TestUpdateRefusals |
| Manual `update` | `inventory-agent update` → `CheckAgentUpdate(apply)` + same flow | T075, T094 | TestUpdateRuns; `internal/selfupdate` TestCheckAndUpdate |
| Server-pushed update | UPGRADE command on the command stream (online at once, offline on connect) | T069, T087, T088, T093 | `internal/ingest` TestStreamStoresPlatformAndDeliversPending; `internal/daemon` TestUpgradeCommandDispatch; `internal/upgrades` TestDeliveryOnConnect |
| Checksum verification | Ed25519-signed manifest (stronger than v3's checksum), then size + SHA-256 while streaming; failures never install | T063, T072, T078 | `internal/agentrelease` Verify tests + FuzzManifest; TestVerificationFailuresNeverInstall; `internal/ingest` TestDownloadThroughRealIngest (integration, tampered chunk); `tests/e2e` tampered artifact |
| Atomic swap | binary installs: rename-based swap with backup; Windows detached helper + SCM | T074, T092 | `internal/upgrader` TestSwapAndRestore; TestApplyBinary; `service_windows_test.go` |
| Rollback | package installs reinstall the previous package; binary installs restore the backup; confirm timeout | T072, T092 | TestApplyTimeoutRollsBackPackage, TestApplyRollbackFallbacks; `tests/e2e` broken release rolled back (Debian 12, Rocky 9) |
| (new) package-manager aware | deb/rpm upgrades through dpkg/rpm, package DB stays right | T074, T092 | TestLinuxArgv, TestDpkgLockRetried; `tests/e2e` `dpkg-query`/`rpm -q` show the new then the rolled-back version |
| (new) fleet view, bulk and automatic upgrades | fleet states, Upgrade / selected / all outdated, per-tenant policy with pause | T068, T071, T076, T096–T104 | `internal/upgrades` TestFleet, TestSchedulerFailurePausesPolicyAndResume; `internal/httpapi` TestUpgradePolicyRoutes; UI agents.spec.ts |

All rows are covered; nothing from v3 is left out.
