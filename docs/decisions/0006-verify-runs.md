# ADR 0006: `sift verify` re-checks findings in a new, linked run

**Status:** proposed

## Context

A confirmed finding is a claim about one snapshot (ADR 0001). After a fix,
users need to know whether it still holds at a later revision. The original
run can't record the answer. Final states are terminal within a run
(ADR 0003), and `findings.Transition` refuses any change from a state other
than `candidate` ("re-validation is not supported"). `artifacts/findings.json`
is the canonical record of a run's final states, and `report.Load` reads it.
It is already decided that `sift verify` creates a new run linked to the
original, and that the original run directory is never changed.

## Decision

1. **Link.** A verify run is an ordinary run directory with one new optional
   manifest field, `Manifest.Verifies` (`verifies`; nil for audits):

   ```json
   "verifies": {
     "run": "20261027T153012Z-a1b2c3",
     "source_revision": "<original repository.source_revision>",
     "snapshot_digest": "<original repository.snapshot_digest>",
     "findings": ["ARCH-002", "CORR-001"],
     "findings_sha256": "<sha256 of the original artifacts/findings.json>"
   }
   ```

   `findings` is sorted and de-duplicated. The field is additive, so
   `schema_version` stays 1. The selected entries of the original
   `findings.json` are copied verbatim into the verify run's
   `artifacts/findings.json`, which makes the run self-contained. Their
   `validation_state` and `source_revision` are the original's. Nothing is
   written to the original run. `sift status` on an audit run lists the runs
   that verify it by scanning manifests (`Store.List`). That list is
   derived, never stored.

2. **Snapshot.** Same as `audit`: `source.Inspect`, the dirty policy
   (`--dirty`, default `refuse`), `source.Export` into the verify run's
   `source/`, `source.Revision`, the repository fingerprint, and the
   runs-directory check (ADR 0001). The repository defaults to the original's
   `repository.path`. Whatever path is used must contain the original
   `repository.commit` (`git cat-file -e`), or Sift refuses. If the new
   snapshot digest equals `verifies.snapshot_digest`, no agent runs and
   every finding is `still_present` ("snapshot identical to the audited
   one").

3. **Scope and cost.** `sift verify [<path>] [--run ID] [--finding ID]...`
   takes `audit`'s `--dirty`, `--harness` and `--model` flags. Only Pruv
   runs, one execution per selected finding (`executions/verification/<ID>/`),
   through `Schedule` under `max_parallel_agents` with Pruv's timeout and
   budget. Discovery, the plan, and the reviewers don't run. The fixed task
   graph (ADR 0004) is `verification → report`. By default, every `confirmed`
   and `supported_risk` finding is selected. `--finding` narrows the
   selection. The prompt carries the finding, the original decision's
   rationale, the relocation results, both revisions, and the system summary
   from the original `discovery.json`. Pruv keeps its `ValidationDecision`
   schema. `agents/pruv.md` (version 3) gains a section that defines its
   verdicts for verification: `accept` means the claim still holds at this
   revision. `reject` means it no longer does, and Pruv cites the code that
   changed it in `additional_evidence`. `investigate` means Pruv cannot tell.

4. **Relocation** (`findings.Relocate`). Each cited source evidence item is
   checked against the new snapshot:
   1. `findings.Check` at the cited path gives `verified`, or `relocated`
      within the file (ADR 0003 rules, including the 20-character minimum).
   2. Otherwise Sift searches every snapshot file for the normalized
      excerpt. Exactly one match gives `relocated`, with the new path in
      `actual_path`. With zero or several matches, step 1's
      `mismatch`, `missing_file` or `invalid_range` stands, and the match
      count is noted in `detail`.

   Git rename detection is not used. It needs both revisions as commits,
   which `include` snapshots aren't, and it depends on similarity
   heuristics. The content search is a pure function of the excerpt and
   the snapshot. Execution evidence stays `not_verifiable`. Locations
   aren't relocated, because they carry no content to match. Reports show
   them as cited at the audit.

5. **Outcomes.** A new enum, `contract.VerifyOutcome`, has four values:
   `fixed`, `still_present`, `cannot_tell` and `unverified`. It is not a
   `ValidationState`. Verify doesn't call `Transition`, `Merge` or `Gate`,
   and none of them change. A deterministic `findings.VerifyGate` decides
   the outcome. Pruv's `additional_evidence` (*added* evidence) is checked
   against the new snapshot with the relocation procedure in step 4. An
   added item is *new code* only when it is verified or relocated in the
   new snapshot **and** its normalized excerpt has zero matches anywhere in
   the original snapshot, searched with the same whole-snapshot procedure,
   normalization and 20-character minimum. A path-only or same-file check
   against the original is not enough: code moved from another file would
   look new. One or more matches in the original means the code is not new.
   - `still_present`: Pruv returns `accept`, at least one cited or added
     item is verified or relocated in the new snapshot, and **no** cited or
     added item contradicts the new snapshot. As in ADR 0003's gate, an
     accept is not enough when part of the cited evidence no longer holds.
     Cited code that merely moved is `relocated` (step 4), not a
     contradiction. A cited item that changed or disappeared makes the
     outcome `cannot_tell` ("validator says still present, but N cited
     item(s) changed").
   - `fixed`: Pruv returns `reject`, no added item contradicts the new
     snapshot, and the change is visible mechanically. Either at least one
     cited item now contradicts the new snapshot, or at least one added
     item is new code (above).
   - `unverified`: Pruv produced no decision (it failed, timed out or was
     interrupted; `error` is set). The finding was not checked, and the
     remedy is to run verify again. This mirrors `unvalidated` in
     ADR 0003.
   - `cannot_tell`: every other case, where Pruv did check but the result
     is not actionable. That includes `investigate`, `accept` with no
     supported evidence, and `accept` with contradicted cited evidence. It
     also includes `reject` when all cited code is unchanged and Pruv cites
     no new code. That case is a disagreement with the original validation,
     not a fix, and the reason says so. The remedy is human review.

   As in ADR 0003, Pruv's verdict alone can't produce the outcome a user
   would act on. A `fixed` outcome requires code that changed.

6. **Artifact.** `artifacts/verification.json` (`kind: verification`,
   `VerificationSet`) holds one `VerificationRecord` per finding. Its fields
   are `finding_id`, `original_state`, `severity` and `confidence` (effective
   at the audit), `relocations` (`EvidenceCheck` plus `actual_path`),
   `validator`, `validator_version`, `decision`,
   `additional_evidence_checks` (new snapshot, with `actual_path`),
   `additional_evidence_prior_matches` (match count of each added excerpt
   across the whole original snapshot, persisted so the *new code* test can
   be audited), `outcome`,
   `outcome_reason`, and `error`. The file is rewritten as each record is
   decided, like `decisions.json`. A selected finding without a record is
   `unverified` ("not checked"). `verification.json` is the canonical
   outcome record, and `findings.json` stays the canonical validation state.

7. **Display.** `report.Load` branches on `Manifest.Verifies`. For a verify
   run it reads that run's `findings.json` and `verification.json`.
   `report.Item` gains a `verification` field, omitted for audits.
   - `sift findings` adds an `OUTCOME` column and an `--outcome` filter,
     which is an error on audit runs. `STATE` is the state at the audit.
   - `sift status` shows `Verifies:` (run, revision, count) and `Outcomes:`
     in place of `Findings:`.
   - `report.Generate` writes `report/verification.md` in place of the four
     audit reports. It shows both revisions, outcome counts, each finding's
     outcome and reason, its old and new evidence lines, Pruv's rationale,
     and the limitations. `ReportMeta` gains `verifies_run` and
     `outcome_counts`.
   - `--run` still defaults to the latest run of either kind. `sift verify`
     defaults to the latest audit run.
   - Run statuses and exit codes match `audit`. Any `unverified` finding
     makes the run `partial` (exit 2); `--outcome unverified` lists the
     findings to run again.

8. **Refusals.** Sift refuses before creating a run if any of these hold:
   - The original run is not found or is itself a verify run.
   - The original run is still `running` (the same PID check as `sift
     status`).
   - The original run has no `artifacts/findings.json` because its
     validation didn't finish, or it has no `source/`.
   - A finding ID is unknown, or the finding isn't `confirmed` or
     `supported_risk`. Verify asks whether an established finding still
     holds; a `needs_investigation`, `unvalidated` or `rejected` finding
     was never established, and its cited evidence may not have matched
     even the audited revision. The error names the state, e.g. "ARCH-004
     is needs_investigation; only confirmed or supported_risk findings can
     be verified".
   - Nothing is selected.
   - The repository lacks the original commit.
   - The working tree is dirty and the policy is `refuse`.

## Consequences

- An audit run stays an immutable record. A finding's history is a chain of
  runs, each tied to its own snapshot.
- Cost is one Pruv execution per selected finding. Kiro can't enforce a
  budget (ADR 0005), so with Kiro only `agents.timeout` bounds the cost.
  The harness may differ from the original's, and each manifest records its
  own.
- `fixed` is conservative. A fix made elsewhere, such as a guard in a
  caller, is `cannot_tell` unless Pruv cites the new code. The checks prove
  that code changed, not that the change fixed the problem. That judgment
  is Pruv's. Nothing is backed by a test run.
- Older Sift binaries reject verify manifests, because strict decoding
  rejects the unknown field `verifies`.
- New contract surface: `VerifyTarget`, `VerifyOutcome`, `VerificationSet`,
  `VerificationRecord`, `Relocation`, `schemas/verification.schema.json`,
  and additive fields on `Manifest`, `ReportMeta` and `report.Item`.

## Alternatives considered

- **Record outcomes in the original run.** Rejected. It breaks run
  immutability and ADR 0003's terminal states, and the original's line
  numbers refer to its own snapshot.
- **New `ValidationState` values, with a `Transition` from `confirmed`.**
  Rejected. It conflates "supported at revision R" with "changed between R
  and R′". `stateOrder`, `--state`, every report section, and the schemas
  would each need two lifecycles.
- **Re-run a full audit and diff the findings.** Rejected. It pays for
  discovery and every reviewer again. IDs are positional (`ARCH-001`), so
  findings can't be matched across runs. A finding missing from the new
  audit only means no reviewer reported it this time, possibly because of
  `max_findings_per_reviewer`.
- **Verify mechanically only (excerpt gone means fixed).** Rejected.
  Refactors remove excerpts without fixing anything.
- **Verify unresolved findings too.** Out of scope. Checking a
  `needs_investigation` or `unvalidated` finding at a new revision is
  re-validation: its result is a `ValidationState`, not a `VerifyOutcome`,
  and a `still_present` outcome would promote a finding that never passed
  `Gate`. Re-validating unresolved findings needs its own decision.
- **Fold Pruv failures into `cannot_tell`.** Rejected. "Not checked" and
  "checked, inconclusive" need different actions (run again versus human
  review), as ADR 0003 separates `unvalidated` from `needs_investigation`.
