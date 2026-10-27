# ADR 0001: Audit a snapshot; refuse dirty trees by default

**Status:** accepted

## Context

Findings cite file paths and line numbers. These are only meaningful
against a fixed, recorded revision. A Git commit doesn't describe
uncommitted changes. Also, running agents inside the live repository would
let a misbehaving harness touch it.

## Decision

1. Sift exports the audited content into `<run>/source` and runs every agent
   there. Files are written read-only. The live repository is never an
   agent's working directory.
2. Committed content is read from the object database (`git ls-tree` +
   `git cat-file`), not `git archive`, because `export-ignore` and
   `export-subst` attributes would change what is audited.
3. Symlinks and submodules are skipped and listed in the manifest. A
   symlink could point outside the snapshot.
4. Dirty working trees (any `git status --porcelain` output, including
   untracked files) are handled by an explicit policy:

   | Policy | Audits | Source revision recorded |
   |---|---|---|
   | `refuse` (default) | nothing; stops with an actionable error before creating a run | n/a |
   | `head` | the last commit; uncommitted changes excluded and a warning recorded | `<sha>` |
   | `include` | tracked and untracked, non-ignored working-tree files | `<sha>+worktree.<digest12>` |

   With `include`, `git diff --binary HEAD` is saved as `source.diff` and its
   hash is recorded. Untracked files aren't in the diff; they are preserved
   in the snapshot.
5. Every snapshot gets a content digest (SHA-256 over sorted path and
   content hashes).
6. All Git calls set `GIT_OPTIONAL_LOCKS=0`, so `git status` doesn't
   refresh the repository's index. A fingerprint of HEAD, status, and diff
   is compared before and after the audit and recorded as `integrity`.
7. The runs directory must be outside the repository. Sift refuses
   otherwise.

## Consequences

- Evidence is reproducible from the commit for `refuse` and `head` runs. For
  `include` runs it is reproducible from the run's snapshot (or commit plus
  `source.diff` plus the untracked files), not from the commit alone. The
  report says so.
- Snapshots duplicate the repository on disk per run. That's acceptable for
  an MVP; deduplication can come later.
- Sift requires Git. Non-Git directories are refused, because the revision
  could not be recorded.
