# SBAM PBS Agent — Core technical audit

## Scope and safety

This audit covers the inherited `machinebackup`, Windows/VSS acquisition, PBS protocol client, FIDX writer, manifest, P2V metadata, and NBD reader. No real disk, backup, or restore operation is part of the correctness work.

## Architecture

`machinebackup` opens one PBS writer session, acquires each configured source, creates one fixed index per disk, optionally uploads `qemu-server.conf.blob` for PBS backup type `vm`, uploads `index.json.blob`, and calls `finish`. Windows acquisition combines raw reads for partition-table/gap regions with VSS device reads for mounted volumes. Non-Windows acquisition reads a seekable file or block-device path. The FIDX pipeline assigns absolute offsets, hashes/compresses/uploads with eight workers, assigns digest/offset pairs, computes the index checksum in offset order, and closes the writer.

`directorybackup` is related but not interchangeable: it uses content-defined chunks and DIDX for PXAR/streams. FIDX uses fixed chunks because block-image random access and NBD require deterministic offset-to-chunk mapping.

## Historical intent

The machine path was introduced as WIP in `1cc5b61`, gained a working non-incremental FIDX in `c8c99b3`, Windows disk probing in `2a29b15`, and working Windows imaging in `d1daabf`. Later commits restored Linux block devices (`dcec95d`), allowed a short final chunk (`69683a9`), corrected digest ordering (`c9dbf8a`), added overrun detection (`d6e8753`), padded VSS/filesystem length to the partition boundary (`a59e4ab`), introduced P2V/NBD (`4917175`, `c454e7b`), and optimized maps, ZSTD, and zero blocks (`9024500`, `5142604`, `519b624`). These changes encode fixes for observed failures and must not be removed as cosmetic leftovers.

## Active and inherited code

Active core paths are Windows layout discovery, `GetDiskLength`, VSS acquisition, raw-gap reconstruction, fixed-index creation/upload/assignment/close, previous-FIDX digest discovery, manifest/blob upload, finish, optional VM config generation, and NBD FIDX reads.

`ChunkState.C`, `current_chunk`, local `digests`, and parts of the new/reused counters appear inherited or incomplete. They are retained pending historical/protocol evidence. `ChunkUploadStats` is an upstream-equivalent manifest structure and must be completed rather than replaced.

## Confirmed or likely defects

1. Worker completion order controls `assignments` and `assignments_offset`, while the checksum is explicitly calculated in offset order. This is valid PBS fixed-index behavior: `fixed_append` in the official `src/api2/backup/mod.rs` pairs every digest with its offset, and `fixed_writer_append_chunk` in `src/api2/backup/environment.rs` derives the chunk index from offset and size before writing the digest directly at that position. Assignment arrival order therefore need not be monotonic. The deterministic harness retains this case to verify concurrency, byte reconstruction, and checksum correctness.
2. Phase 2B replaces the per-worker result channel with shared context cancellation, one retained first error, an owned producer channel, and a worker wait group. Upload errors, producer errors/panics, cancellation, and size overruns now stop finalization and are covered by deterministic lifecycle tests; the function waits for producer and all eight workers before returning.
3. `newchunk` is never incremented in the machine writer; `reusechunk` is local and not returned; `ChunkUploadStats` remains zero.
4. Phase 2A now makes the machine-backup PBS API path fail closed on non-2xx responses and closes response bodies on both success and error paths; regression tests cover 400/401/403/500 and workflow termination.
5. Phase 2A now enforces a configured SHA-256 certificate fingerprint even with self-signed or explicitly insecure connections; without a fingerprint, normal CA validation or explicit insecure mode remains unchanged.
6. Phase 2D replaces the broken mount-path/extent coupling with a tested `WindowsVolume` model preserving volume GUID, all mount paths, and all extents. Single-extent basic volumes with a drive-root VSS source are mapped explicitly; directory-only, no-mount, ambiguous, duplicate, and target multi-extent mappings fail closed.
7. Phase 2D wires `DiskLayout` validation into Windows acquisition before raw-gap construction, ordering partitions and rejecting overlap, out-of-bounds ranges, and arithmetic overflow. Upstream issue #72 remains classified as possible source-layout corruption, distinct from writer overrun.
8. Phase 2D.1 parses the native Windows amd64 partition union and rejects MBR type `0x42` plus GPT LDM data/metadata GUIDs before volume enumeration or acquisition. Storage Spaces detection is not yet demonstrated by the current IOCTL model and remains an explicit runtime-validation warning.
9. The P2V blob is scaffolding: CPU/RAM/controller/firmware/storage identity are mostly static, SMBIOS/vmgenid are random, and bootability is not established.
10. NBD is read-only but its cache synchronization, bounds checks, HTTP validation, index checksum verification, and chunk digest verification are incomplete.

## Integrity and P2V risk

Phase 2A makes the PBS API fail closed and Phase 2B prevents FIDX finalization after pipeline failure. Remaining caller-level risks include unchecked return values outside the hardened writer path, such as some manifest/blob/finish calls in `machinebackup` main flow. Phase 2D prevents a discovered but unsupported or ambiguous Windows volume from silently becoming a live raw read; runtime Windows validation of enumeration remains required. Multi-volume VSS sets and writer state are not implemented, so application consistency is not established. Target multi-extent/dynamic layouts fail closed rather than being treated as a basic partition.

A byte-correct disk image does not imply a bootable P2V result. BIOS/UEFI, EFI disk, Secure Boot, TPM, BitLocker, storage drivers, NIC identity, boot order, source Windows version, and stable machine identity are not modeled.

## Preserve before fixes

Do not replace FIDX with DIDX; remove raw gaps; remove VSS padding; force every chunk to 4 MiB; remove ordered checksum calculation; replace Windows size IOCTL with seek; move disk reading outside VSS snapshot lifetime; make NBD writable; or delete apparently dead fields before tests and history establish their purpose.

## Recommended sequence

1. Establish the in-memory correctness harness and protocol recorder.
2. Preserve offset-addressed fixed-index assignments; the official PBS implementation does not require monotonic arrival order.
3. Make every protocol response explicit and fail closed.
4. Preserve the Phase 2B structured cancellation and deterministic worker lifecycle while retaining parallel hash/compression/upload.
5. Replace ad-hoc Windows discovery with a validated immutable disk plan. The current `DiskLayout` is only a synthetic, testable model for that future phase and is not connected to real Windows disk acquisition.
6. Add multi-volume VSS set/writer-state policy and guaranteed cleanup.
7. Populate existing upstream statistics with distinct logical, compressed, transmitted, and reused measures.
8. Separate canonical host imaging from optional P2V profile generation.
9. Verify newly written snapshots by parsing FIDX and reconstructing deterministic samples or complete synthetic images.

## Upstream evidence limits

Local Git history references issue #31 and historical PRs, while issue #72 and #75 are supplied by the SBAM requirements. Direct GitHub issue/PR retrieval was blocked in the audit environment, so their exact upstream discussions remain to be archived when network access is available.
