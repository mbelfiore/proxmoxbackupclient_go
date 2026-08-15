# SBAM Repository Rules

- Never modify `master` directly; work on a dedicated branch.
- Preserve upstream compatibility unless a documented, tested migration requires otherwise.
- Write the test first, then implement the fix.
- Tests must be non-destructive.
- Never access a real Windows `PhysicalDrive` from tests or CI.
- Never run real backup or restore operations from CI.
- A Windows amd64 build is mandatory before considering a change valid.
- Investigate apparently dead code, including its Git history and upstream intent, before removing it.
- Do not introduce new statistics when an equivalent upstream structure already exists and can be completed.
- Always distinguish logical bytes, compressed bytes, bytes actually transmitted, and reused chunks.
