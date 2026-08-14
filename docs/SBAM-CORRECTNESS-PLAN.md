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
| UploadBlob non-2xx | False success | 400/401/403/500 | Current nil return demonstrated | CHARACTERIZED | Production bug intentionally unfixed |
| UploadManifest non-2xx | False success | 400/401/403/500 | Current nil return demonstrated | CHARACTERIZED | Delegates to UploadBlob |
| Finish non-2xx | False job success | 400/401/403/500 | Current nil return demonstrated | CHARACTERIZED | Production bug intentionally unfixed |
| AssignFixedChunks non-2xx | Missing index entries | 400/401/403/500 | Current nil return demonstrated | CHARACTERIZED | Production bug intentionally unfixed |
| CloseFixedIndex non-2xx | Invalid/incomplete FIDX | 400/401/403/500 | Current nil return demonstrated | CHARACTERIZED | Manifest mutation also needs later assertion |
| Fingerprint mismatch | MITM/certificate trust | Self-signed mismatching DER | Current acceptance demonstrated | CHARACTERIZED | Production bug intentionally unfixed |
| PhysicalDrive casing | Issue #75 routing | Four case combinations | Canonical form recognized; current rejection of other casing demonstrated | CHARACTERIZED | Pure string test; no device open; production fix deferred |
| Layout ordering | Incorrect gap plan | Ordered and shuffled partitions | Normalize to identical ordered coverage | PASS | GPT and MBR |
| Layout overlap | Ambiguous reads | Overlapping partitions | Reject before acquisition | PASS | Synthetic model |
| Layout beyond disk | Issue #72/source corruption | End > physical size | Reject as invalid source layout | PASS | Distinct from writer overrun |
| Layout gaps | Lost boot/GPT metadata | Before/between/after gaps | Full contiguous coverage | PASS | Synthetic model |
| Writer true overrun | Producer emits > declared size | Extra synthetic block | Writer returns fatal overrun | PLANNED | Current goroutine error path can deadlock; requires safe cancellation seam |
| `newchunk` | Incorrect metrics | New and duplicate chunks | New count increments only uploads | PLANNED | Known current omission; do not fix in Phase 1 |
| `reusechunk` | Incorrect metrics | Previous/in-job duplicate | Reuse count semantics documented | PLANNED | Distinguish prior vs same-job reuse if required |
| `ChunkUploadStats` | Misleading manifest | Mixed compressed/reused data | Existing fields populated with verified semantics | PLANNED | No replacement stats structure |
| `digests` | Apparently dead state | Forced reordered chunks | Establish historical/current purpose | PLANNED | Retain until fix design |
| Volume/mount mapping | Live-read consistency | GUID, drive letter, directory mount, no mount | Correct volume-to-extent plan or fail closed | PLANNED | Requires Windows API adapter |
| VSS multi-volume | Cross-volume consistency | Two synthetic volumes | One policy-controlled snapshot set | PLANNED | Current library seam insufficient |
| VSS writer state | Application consistency | Writer success/failure | Fail or downgrade per explicit policy | PLANNED | Not currently queried |
| Snapshot cleanup | Leaked shadow copies | Callback success/error/panic | Release exactly once | PLANNED | Needs injectable snapshotter |
| VSS padding | Partition-size preservation | Snapshot shorter than partition | Zero-pad exact deficit only | PLANNED | Preserve historical behavior |
| Official PBS semantics | Non-monotonic fixed assignments | Official `fixed_append` and `fixed_writer_append_chunk` behavior | Digest is written at the position calculated from offset and size; monotonic arrival is not required | PASS | Verified separately against current PBS `src/api2/backup/mod.rs` and `environment.rs` |


## Validation report

The Phase 1 validation was run with module-specific commands rather than a repository-root `go test ./...` claim:

- `machinebackup`: tests passed; `go vet` passed; Linux build passed; Windows amd64 cross-build passed.
- `pbscommon`: characterization tests passed with `-vet=off`. Standard `go test` does not pass because `go vet` detects the pre-existing `pbsapi.go` `fmt.Errorf` call with arguments but no formatting directives; that production defect is intentionally not fixed in this phase.
- `directorybackup`: Linux and Windows amd64 builds passed.
- `nbd`: Linux build passed.
- `git diff --check`: passed.

No backup, restore, or real `PhysicalDrive` access was performed.

## Synthetic DiskLayout boundary

`DiskLayout` is deliberately disconnected from the production Windows acquisition path. It is a synthetic, deterministic model used to specify ordering, overlap, bounds, MBR/GPT, and gap behavior for the future disk-layout validation phase. It does not inspect, validate, size, or acquire any real disk today.
