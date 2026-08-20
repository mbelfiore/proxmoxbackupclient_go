# SBAM Correctness Plan

Updated: 2026-08-20

Status meanings:
- **PASS**: implemented and passing at the stated level.
- **PASS WITH WARNINGS**: evidence exists, but important runtime/product limits remain open.
- **RUNTIME PASS**: observed on the isolated Windows LAB.
- **PLANNED**: not yet implemented/validated.

| TEST / AREA | RISK COVERED | EXPECTED RESULT | STATUS | NOTES |
|---|---|---|---|---|
| FIDX empty / short / exact / multi | index integrity | byte-exact reconstruction and ordered checksum | PASS | in-memory deterministic harness |
| Zero and duplicate chunks | dedup / zero fast path | exact reconstruction, correct reuse behavior | PASS | metrics still incomplete |
| Worker reorder | concurrent completion ordering | non-monotonic completion without index corruption | PASS | official FIDX offset semantics preserved |
| Eight-worker lifecycle | orphaned workers / false success | all workers joined before return | PASS | no time.Sleep orchestration |
| Upload failure positions | false finalization | original error propagated; no assign/close | PASS | deterministic regression |
| Producer failure/panic | lost source errors | fail closed; no finalization | PASS | panic converted to controlled error |
| PBS non-2xx | false success | bounded diagnostic error | PASS | create/upload/assign/close/blob/manifest/finish surfaces hardened where covered |
| TLS fingerprint | trust failure | mismatch rejected | PASS | normalized SHA-256 pinning |
| PBS upgrade cancellation | blocked connect | context cancellation closes owned connection | PASS | no arbitrary global timeout |
| PBS strict 101 upgrade | malformed server response | fail closed | PASS | header ceiling and short-write coverage |
| PhysicalDrive casing | routing bug | valid casing resolves same index | PASS | no real device access |
| DiskLayout ordering/gaps | lost boot/GPT metadata | complete contiguous plan | PASS | synthetic model wired into Windows path |
| Layout overlap/out-of-bounds | ambiguous/corrupt source | reject before acquisition | PASS | fail closed |
| Windows MULTI_SZ | wrong mount mapping | preserve all mount paths or reject malformed | PASS | pure parser tests |
| Windows volume extents | first-extent/truncation bug | bounded parse; multi-extent target rejected | PASS | variable IOCTL buffer production path |
| Windows volume mapping | live read of mounted FS | explicit VSS source or fail closed | PASS | drive-root + canonical no-mount GUID fallback |
| Dynamic/LDM identifiers | silent unsupported layout | reject before VSS/raw/upload | PASS | MBR 0x42; GPT LDM data/metadata |
| Storage Spaces known identifiers | silent unsupported layout | reject before VSS/raw/upload | PASS | MBR 0xD7/0xE7; GPT Spaces/Spaces Data |
| No-mount GUID parser | malformed VSS source | only strict canonical root accepted | PASS | Phase 2D.3B |
| No-mount GUID disk plan | wrong Raw/VSS selection | basic single-extent segment uses exact GUID and Raw=false | PASS | Phase 2D.3B |
| Snapshot source normalization | GUID corrupted by filepath helpers | canonical GUID bypasses Abs/VolumeName | PASS | ordinary path behavior preserved |
| Windows probe IsVolumeSupported | GUID capability assumption | no-mount System Reserved GUID supported | RUNTIME PASS | BACKPREALTEST Server 2019; read-only; exit 0 |
| DiskShadow real GUID snapshot | Windows VSS capability | shadow created for exact no-mount GUID | RUNTIME PASS | BACKPREALTEST; Microsoft provider |
| DiskShadow Auto_Release cleanup | leaked test shadow | no residual shadow after exit | RUNTIME PASS | `vssadmin list shadows` empty afterward |
| SBAM/go-vss single GUID runtime | wrapper-specific compatibility | production wrapper creates GUID snapshot correctly | PLANNED | DiskShadow result does not prove wrapper behavior |
| Coordinated VSS multi-volume | cross-volume consistency | one snapshot set containing all supported sources | PLANNED | Phase 2E next gate |
| VSS AddToSnapshotSet partial failure | incomplete set accepted | fail closed; no backup callback; cleanup | PLANNED | Phase 2E |
| VSS creation failure | false success | fail closed; no callback; cleanup | PLANNED | Phase 2E |
| VSS callback failure cleanup | leaked shadows | error propagated; cleanup exactly once | PLANNED | Phase 2E |
| VSS writer metadata/state | application consistency | explicit writer policy | PLANNED | later sub-phase if library seam requires |
| VSS padding | partition-size preservation | zero-pad exact deficit only | PLANNED | preserve historical behavior |
| Windows kernel I/O cancellation | blocked PhysicalDrive/snapshot read | bounded/correct cancellation semantics | PLANNED / WARNING | current Close hook is prudent but not a kernel-level guarantee |
| Real Storage Spaces topology | incomplete identifier characterization | known-safe classification or explicit unsupported | PLANNED | no production support claim |
| Dynamic/LDM real support | FILESERN / RAID layouts | topology-aware safe backup and restore metadata | PLANNED | currently deliberately fail closed |
| `newchunk` / `reusechunk` | incorrect metrics | verified semantics | PLANNED | do not block current correctness gates |
| `ChunkUploadStats` | misleading manifest stats | verified values | PLANNED | later cleanup |
| Post-upload PBS verification | undetected stored corruption | verify against PBS-side evidence | PLANNED | required before production claim |
| Real Windows -> PBS backup | end-to-end product path | complete snapshot stored in PBS | PLANNED | only after Phase 2E runtime gate |
| Real restore | backup usefulness | restored data/image verified | PLANNED | required before production claim |
| P2V bootability | recovery usability | restored machine boots as PVE VM | PLANNED | firmware/TPM/BitLocker/drivers/NIC still open |
| NBD hardening | unsafe read service | strict bounds/index/digest/read-only checks | PLANNED | not current phase |

## Current evidence boundary

The current branch contains implementation through Phase 2D.3B. Phase 2D.3B added strict canonical no-mount Volume GUID handling in `machinebackup` and `snapshot`, with regression tests and Windows amd64 cross-build evidence from the implementation task.

Runtime evidence on the isolated Windows Server 2019 LAB `BACKPREALTEST` is stronger than the older documentation stated:

1. The read-only SBAM probe enumerated the no-mount System Reserved volume `\\?\Volume{3a886445-0000-0000-0000-100000000000}\`, reported a single extent on Disk0, and `IsVolumeSupported=true` with exit code 0.
2. Microsoft DiskShadow then created a real shadow copy using that exact canonical Volume GUID.
3. The shadow was `Auto_Release`; after DiskShadow exited, `vssadmin list shadows` showed no residual shadow.

This proves Windows/VSS capability for the tested no-mount GUID. It does **not** yet prove that the production `github.com/st-matskevich/go-vss` wrapper path creates the same snapshot correctly.

## Phase 2E correctness gate

Before implementing coordinated multi-volume VSS, inspect the exact API surface of `github.com/st-matskevich/go-vss v0.3.3` actually used by this repository.

The required model is one VSS snapshot set containing every supported source for the target Windows disk, for example:

- `\\?\Volume{...}\` for System Reserved without a drive letter;
- `C:\` for the Windows volume.

All sources must be added to the same set before snapshot creation. If the library does not expose the required primitives safely, stop and document the API gap instead of simulating coordination by sequential `CreateSnapshot` calls.

Phase 2E must preserve these invariants:

- no FIDX semantic change;
- no PBS protocol change;
- no DiskLayout/raw-gap change;
- no new LDM/Storage Spaces support;
- no directory-only promotion to supported;
- no real backup/restore/PhysicalDrive access during implementation tests;
- cleanup and fail-closed behavior on partial set failure, creation failure and callback failure;
- deterministic source -> snapshot mapping;
- no production-ready claim until Windows runtime validation succeeds.

## Product gate sequence

1. Phase 2E implementation + deterministic tests.
2. Controlled `BACKPREALTEST` runtime: System Reserved GUID + `C:\` in the same VSS snapshot set through the SBAM/go-vss path.
3. Build/use `machinebackup.exe` for the first controlled Windows -> PBS backup.
4. Verify restore.
5. Add topology-aware Dynamic/LDM/RAID support required by FILESERN.
6. Only later service, scheduler, installer, telemetry and GUI.

No real backup, restore or raw PhysicalDrive access has yet been completed as an SBAM end-to-end product validation. Production ready: **NO**.
