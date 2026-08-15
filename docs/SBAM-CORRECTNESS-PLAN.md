# SBAM Phase 1 — Correctness plan

Status values: **PASS** means implemented and passing locally; **CHARACTERIZED** means a current production defect is intentionally asserted/documented without a fix; **PLANNED** means the test requires a later seam or authoritative external evidence.

| TEST | RISCHIO COPERTO | INPUT | RISULTATO ATTESO | STATO | NOTE |
|---|---|---|---|---|---|
| FIDX empty | Empty image handling | 0 bytes | Close with zero size/count; reconstruct empty | PASS | In-memory only |
| FIDX one byte | Minimum short final chunk | 1 byte | Exact reconstruction | PASS | In-memory only |
| FIDX sub-chunk | Short final chunk | 12,345 bytes | Exact reconstruction | PASS | In-memory only |
| FIDX exact chunk | Boundary handling | 4 MiB | One exact chunk | PASS | In-memory only |
| FIDX chunk + 1 | Final chunk handling | 4 MiB + 1 | Two chunks; exact reconstruction | PASS | In-memory only |
| FIDX multiple | Index/checksum/order | 3×4 MiB + 99 | Exact reconstruction and ordered checksum | PASS | In-memory only |
| FIDX zeros | Zero fast-path integrity | 8 MiB zeros | Exact reconstruction | PASS | Does not benchmark optimization |
| FIDX duplicates | In-job dedup | Two identical 4 MiB chunks | One upload may satisfy two offsets; exact reconstruction | PASS | Uses recorded digest store |
| Worker reorder | Concurrent completion ordering | Three chunks released through explicit barriers in reverse order | Non-monotonic assignments, exact reconstruction, and correct ordered checksum | PASS | Deterministic; official PBS writes each digest at the chunk position derived from offset and size |
| PBS 200 recorder | Call coverage | Create/upload/assign/close/blob/manifest/finish | All calls recorded | PASS | `httptest`, no PBS server |
| UploadBlob non-2xx | False success | 400/401/403/500 | Non-nil error with method, path, status, and bounded body | PASS | Regression coverage added in Phase 2A |
| UploadManifest non-2xx | False success | 400/401/403/500 | Non-nil error with method, path, status, and bounded body | PASS | Delegates to UploadBlob |
| Finish non-2xx | False job success | 400/401/403/500 | Non-nil error with method, path, status, and bounded body | PASS | Regression coverage added in Phase 2A |
| AssignFixedChunks non-2xx | Missing index entries | 400/401/403/500 | Non-nil error with method, path, status, and bounded body | PASS | Regression coverage added in Phase 2A |
| CloseFixedIndex non-2xx | Invalid/incomplete FIDX | 400/401/403/500 | Non-nil error; manifest state not finalized | PASS | Regression coverage added in Phase 2A |
| Fingerprint mismatch | MITM/certificate trust | Correct, normalized, incorrect, and insecure+pinned fingerprints | Correct pins accepted; mismatches rejected | PASS | Regression coverage added in Phase 2A |
| PhysicalDrive casing | Issue #75 routing | Four case combinations | All valid casing variants resolve to the same index | PASS | Pure string test; no device open |
| Layout ordering | Incorrect gap plan | Ordered and shuffled partitions | Normalize to identical ordered coverage | PASS | GPT and MBR |
| Layout overlap | Ambiguous reads | Overlapping partitions | Reject before acquisition | PASS | Synthetic model |
| Layout beyond disk | Issue #72/source corruption | End > physical size | Reject as invalid source layout | PASS | Distinct from writer overrun |
| Layout gaps | Lost boot/GPT metadata | Before/between/after gaps | Full contiguous coverage | PASS | Synthetic model |
| Writer true overrun | Producer emits > declared size | Extra synthetic block | Error returned; no assign/close | PASS | Phase 2B cancellation regression |
| Eight-worker lifecycle | Orphaned workers on success | Eight uploads held at an explicit barrier | All eight start, complete, and reconstruct byte-identically | PASS | No sleep; timeout is deadlock guard only |
| Upload failure positions | Error/nil double result and false finalization | First, middle, and final source chunks fail | Original error returned; producer/workers end; no assign/close | PASS | Phase 2B deterministic regression |
| Cancellation of queued work | Producer/dispatcher deadlock and excess PBS calls | 16 blocks; eight in flight; one forced failure | Queued work canceled; only in-flight calls finish; all goroutines joined | PASS | Explicit gates; no timing orchestration |
| Producer failure | Panic or read error escapes/lost | Returned error and recovered synthetic panic | Useful error returned; no assign/close; workers end | PASS | Production producers now return errors through blockProducer |
| `newchunk` | Incorrect metrics | New and duplicate chunks | New count increments only uploads | PLANNED | Known current omission; do not fix in Phase 1 |
| `reusechunk` | Incorrect metrics | Previous/in-job duplicate | Reuse count semantics documented | PLANNED | Distinguish prior vs same-job reuse if required |
| `ChunkUploadStats` | Misleading manifest | Mixed compressed/reused data | Existing fields populated with verified semantics | PLANNED | No replacement stats structure |
| `digests` | Apparently dead state | Forced reordered chunks | Establish historical/current purpose | PLANNED | Retain until fix design |
| Volume/mount mapping | Live-read consistency | GUID, drive letter, directory mount, no mount | Correct volume-to-extent plan or fail closed | PLANNED | Requires Windows API adapter |
| VSS multi-volume | Cross-volume consistency | Two synthetic volumes | One policy-controlled snapshot set | PLANNED | Current library seam insufficient |
| VSS writer state | Application consistency | Writer success/failure | Fail or downgrade per explicit policy | PLANNED | Not currently queried |
| Snapshot cleanup | Leaked shadow copies | Callback success/error/panic | Release exactly once | PLANNED | Needs injectable snapshotter |
| VSS padding | Partition-size preservation | Snapshot shorter than partition | Zero-pad exact deficit only | PLANNED | Preserve historical behavior |
| Windows MULTI_SZ | Wrong mount source | Zero, drive root, directory, multiple, malformed | Preserve every path or reject malformed data | PASS | Pure Linux-runnable parser; Phase 2D |
| Windows volume extents | Truncated/first-extent-only mapping | Single, multi, short, inconsistent, overflow | Parse bounded data; target multi-extent fails closed | PASS | Variable IOCTL buffer in production; Phase 2D |
| Windows volume mapping | Live-read of mounted filesystem | Other disk, duplicate, unknown, directory-only, no-mount | Explicit VSS mapping or fail closed | PASS | Drive-root VSS source is the supported MVP |
| Official PBS semantics | Non-monotonic fixed assignments | Official `fixed_append` and `fixed_writer_append_chunk` behavior | Digest is written at the position calculated from offset and size; monotonic arrival is not required | PASS | Verified separately against current PBS `src/api2/backup/mod.rs` and `environment.rs` |


## Validation report

The Phase 1 validation was run with module-specific commands rather than a repository-root `go test ./...` claim:

- `machinebackup`: tests passed; `go vet` passed; Linux build passed; Windows amd64 cross-build passed.
- `pbscommon`: regression tests, `go vet`, and build pass after Phase 2A fail-closed HTTP, response-body lifecycle, and TLS pinning fixes.
- `directorybackup`: Linux and Windows amd64 builds passed.
- `nbd`: Linux build passed.
- `git diff --check`: passed.

No backup, restore, or real `PhysicalDrive` access was performed.

## Synthetic DiskLayout boundary

`DiskLayout` is deliberately disconnected from the production Windows acquisition path. It is a synthetic, deterministic model used to specify ordering, overlap, bounds, MBR/GPT, and gap behavior for the future disk-layout validation phase. It does not inspect, validate, size, or acquire any real disk today.
