# SBAM Repository Rules

- `master` is the single active branch of this fork. Do not create branches or pull requests unless explicitly requested.
- Codex may use a local workspace branch such as `work`; that local name is not a GitHub branch and must not be treated as project state.
- Do not publish, push, merge, reset, rebase, force-push, or rewrite history from Codex unless explicitly instructed.
- A change exists as project state only after it has been reviewed and is present on GitHub `master`.
- Preserve upstream compatibility unless a documented, tested migration requires otherwise.
- Write or update deterministic tests before/with critical fixes.
- Prefer integrity, correctness and fail-closed behavior over convenience or performance.
- Tests must be non-destructive.
- Never access a real Windows `PhysicalDrive` from tests or CI unless a dedicated controlled runtime gate explicitly authorizes it.
- Never run a real backup or restore from CI.
- A Windows amd64 build is mandatory before considering a Windows change valid.
- Investigate apparently dead code, including its Git history and upstream intent, before removing it.
- Do not introduce new statistics when an equivalent upstream structure already exists and can be completed.
- Always distinguish logical bytes, compressed bytes, bytes actually transmitted, and reused chunks.
- Keep warnings explicit. Green tests do not by themselves mean production-ready.
