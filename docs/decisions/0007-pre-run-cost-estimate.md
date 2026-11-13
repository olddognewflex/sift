# ADR 0007: Estimate a run's cost before any agent runs

**Status:** accepted (the spend gate lands in a follow-up change)

## Context

Sift gives no idea of cost until the money is spent. `agents.max_budget_usd`
caps one agent execution, not a run, and a run makes 1 discovery execution,
one per reviewer, and up to `reviewers × max_findings_per_reviewer` Pruv
executions. Kiro can't enforce even the per-execution cap (ADR 0005), so
there only `agents.timeout` bounds spend. Users need a whole-run figure
before they commit, and Sift needs a record to tell whether that figure
was right.

## Decision

1. **One shared model** in `internal/cost`, pure and versioned
   (`files_x_reviewers_plus_pruv/v1`). `cap(agent)` is the agent's
   effective `agents.max_budget_usd` (per-agent overrides included) when
   the harness advertises `budget-limit`, and unlimited otherwise.

   ```text
   discovery = min(discovery_usd, cap(discovery))
   reviewers = Σ over reviewers r: min(files × file_review_usd, cap(r))
   low/high  = discovery + reviewers
             + pruv_calls_{low,high} × min(pruv_call_usd, cap(pruv))
   pruv_calls_high = reviewers × max_findings_per_reviewer
   pruv_calls_low  = reviewers × min(expected_findings_per_reviewer, max)
   ```

   `files` is what the snapshot will hold (`source.CountFiles`, which
   shares its listing with `Export`). Amounts are rounded to cents.
   `ForAudit` estimates an audit. `ForVerify` estimates a verify run as one
   Pruv execution per selected finding, with no discovery or reviewers
   (ADR 0006 §3). `Total` and `OverBudget` serve a benchmark across runs.

2. **A whole-run budget**, `cost.budget_usd` (default $15; 0 disables it),
   checked against the high estimate. It is separate from
   `agents.max_budget_usd`. Rates (`cost.rates`) and
   `cost.expected_findings_per_reviewer` (default 4) are configurable.
   With the default settings the high estimate is $5.30 + $0.04 per file.
   On Claude Code each term is capped by the $3 per-execution limit, so the
   estimate never exceeds $11.30 and default Claude Code runs are never
   stopped. On Kiro nothing is capped, so the budget trips at about 243
   files, which is where unbounded spend needs a decision.

3. **The gate.** The orchestrator makes the budget check itself and records
   `no_budget` (budget 0) or `within_budget` without asking anyone. Only an
   over-budget estimate goes to `audit.Options.Preflight`, after setup
   succeeds (and after the repository fingerprint is taken, so edits made
   while a person decides are caught) and before a run directory is
   created. It may answer only `yes_flag` or `prompt`; an error or any
   other answer aborts with no run created. The CLI will ask on a TTY,
   refuse non-interactively with exit 1 and no run directory, and proceed
   with `--yes`. **This change adds only the hook; the CLI gate follows
   separately.** Until it lands, nothing sets `Preflight`, so an
   over-budget run proceeds and records `unchecked`, although the
   `sift init` template already documents the gate. Verify will use the
   same hook with `ForVerify`.

4. **Calibration record.** The manifest gains an optional `cost` field: the
   estimate with its breakdown, the budget, how the run was approved
   (`yes_flag`, `prompt`, `within_budget`, `no_budget`, `unchecked`), and
   the actual spend: reported USD, executions (every attempt), executions
   that reported a cost, Pruv executions, and candidates. It is updated
   after every execution, so an interrupted run keeps what it spent.
   Validation rejects an approval that contradicts the budget check:
   `no_budget` exactly when the budget is 0, `within_budget` only when the
   high estimate doesn't exceed it, and `yes_flag` or `prompt` only when it
   does. `unchecked` is valid in any state.

## Consequences

- The rates are guesses until calibrated by comparing `cost.estimate` with
  `cost.actual` across real runs. The estimate is labelled "configured
  rates, not a quote".
- Retries are not modelled, so a run with retries can exceed the high
  estimate.
- Kiro reports no USD, so Kiro runs record `usd` 0 and can't calibrate the
  rates. A harness's "reported" cost is detected as `cost_usd > 0`; a
  genuine $0 report counts as unreported.
- Older Sift binaries reject manifests with `cost`, because strict decoding
  rejects the unknown field, as ADR 0006 accepts for `verifies`. The field
  is additive, so `schema_version` stays 1.
