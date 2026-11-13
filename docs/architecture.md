# Sift Architecture (MVP)

This document describes what is implemented. The README describes product
intent. Significant decisions are recorded in [decisions/](decisions/).

## Execution flow

```text
sift audit <path>
  │
  ├─ probe harness ──────────────── fail before creating a run if unusable
  ├─ inspect repository (git) ───── commit, branch, porcelain status
  ├─ apply dirty policy ─────────── refuse | head | include      (ADR 0001)
  ├─ estimate cost ──────────────── internal/cost; over budget: confirm or refuse (ADR 0007)
  ├─ create run dir (outside repo), archive agent definitions
  ├─ export read-only snapshot ──── <run>/source, digest, diff
  │
  ├─ discovery ─────────── agent ── artifacts/discovery.json
  ├─ plan ──────────────── code ─── artifacts/review-plan.json
  ├─ review-architecture ─ agent ┐
  ├─ review-correctness ── agent ┴─ artifacts/review-<agent>.json
  ├─ validation
  │    ├─ assign IDs + mechanical evidence checks ── artifacts/candidates.json
  │    ├─ Pruv, one execution per finding ────────── artifacts/decisions.json
  │    └─ evidence gate (code) ───────────────────── artifacts/findings.json
  │
  ├─ integrity check ───────────── repository fingerprint before == after
  └─ report ────────────── code ─── report/*.md, report/report.json
```

The task graph is fixed in code (`internal/audit`). `Needs` dependencies
must succeed, and a task whose `Needs` didn't succeed is skipped. `After`
dependencies only order execution. Validation runs after the reviewers
whatever their outcome, so one failed reviewer still leaves the other's
findings validated. Inside validation, each finding's Pruv execution runs
through the same scheduler, under the same `max_parallel_agents` limit;
each decision is saved as soon as it is made. The report always runs.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/sift` | Entry point; signal handling (first SIGINT cancels gracefully) |
| `internal/cli` | Cobra commands, exit codes, human/JSON output, the over-budget spend gate |
| `internal/config` | YAML config: defaults, strict parsing, validation, `sift init` template |
| `internal/contract` | Versioned artifact types, strict decoding, validation. The single source of truth |
| `internal/schema` | JSON Schema generation from contract types (for harness and `schemas/`), and validation for harnesses without native schema support |
| `internal/source` | Git inspection, snapshot export and file count, dirty-tree policy, repository fingerprint |
| `internal/cost` | Pure pre-run cost model shared by audit, verify, and benchmark: estimate, budget check, display lines (ADR 0007) |
| `internal/storage` | Run directories, atomic artifact writes, run lookup |
| `internal/agent` | Agent definition loading (embedded `agents/*.md` or override dir) |
| `internal/harness` | Harness interface, request/result, typed errors |
| `internal/harness/claudecode` | Claude Code adapter (ADR 0002) |
| `internal/harness/kiro` | Kiro CLI adapter: private home, empty workspace, schema validated by Sift (ADR 0005) |
| `internal/harness/internal/proc` | Shared harness subprocess lifecycle: timeout, cancellation, process-group kill |
| `internal/harness/fake` | Deterministic harness for tests |
| `internal/harness/conformance` | Live suite every adapter must pass: isolation, structured output, timeouts, budgets |
| `internal/findings` | Finding IDs, mechanical evidence checks, state transitions, evidence gate |
| `internal/audit` | Orchestrator: scheduler, task implementations, retries, run state |
| `internal/report` | Deterministic Markdown reports from persisted artifacts |
| `internal/bench` | Benchmark scoring, offline and pure: answer keys, fixture loading and digests, observations from a run, matching rules, precision and recall (ADR 0008, `docs/benchmark.md`) |

There is only one interface that real code substitutes:
`harness.Harness` (Claude Code or Kiro in production, the fake in tests).

## Contracts

All artifacts carry `kind`, `schema_version` (currently 1), and `run_id`.
They are written only after `Validate()` passes, and read with strict
decoding (unknown fields rejected) plus validation. Agent outputs pass the
same strict decode before Sift accepts them, even though the harness also
enforces the JSON Schema.

| Contract | Type | File |
|---|---|---|
| Audit manifest | `Manifest` | `manifest.json` |
| System discovery context | `Discovery` (wraps the agent's `DiscoveryReport`) | `artifacts/discovery.json` |
| Review plan | `ReviewPlan` | `artifacts/review-plan.json` |
| Reviewer output | `Review` (wraps `ReviewReport`) | `artifacts/review-<agent>.json` |
| Candidate findings | `FindingSet` of `Finding` | `artifacts/candidates.json` |
| Validation decisions | `DecisionSet` of `DecisionRecord` (wraps Pruv's `ValidationDecision`) | `artifacts/decisions.json` |
| Final findings | `FindingSet` | `artifacts/findings.json` |
| Agent execution result | `ExecutionResult` | `executions/<task>/…/attempt-N/result.json` |
| Report metadata | `ReportMeta` | `report/report.json` |

`artifacts/findings.json` is the canonical record of final finding states.
Reports, `sift findings`, and `sift status` read it. Validation writes it
once all findings have been attempted. A run whose validation did not
finish (interrupted, or still running) has no `findings.json`. Its states
are derived from `candidates.json` and the decisions recorded so far, and
any finding without a decision is `unvalidated`.

Published JSON Schemas live in `schemas/`, generated from the Go types (`make
schemas`). A test fails if they drift.

## Finding lifecycle

```text
reviewer draft ──► candidate ──(Pruv + gate)──► confirmed | supported_risk
                                                | needs_investigation
                                                | rejected | unvalidated
```

- Sift assigns IDs (`ARCH-001`, `CORR-001`) and the source revision.
  Agents can't choose them. Each reviewer's ID prefix comes from the
  reviewer registry, `contract.Reviewers`.
- To add a reviewer: add it to `contract.Reviewers`, add
  `agents/<name>.md`, and list it in the `sift init` config template.
  Config validation, the category enum, and ID prefixes follow from the
  registry, and tests fail if the agent file or template entry is missing.
- Every source evidence item is checked against the snapshot: verified,
  relocated (excerpt found at other lines), mismatch, missing_file,
  invalid_range. Execution evidence is `not_verifiable`, because agents
  can't run commands under the MVP policy.
- Every finding location is checked too: the file must exist and the line
  range must lie within it (verified, missing_file, invalid_range).
- The gate (ADR 0003) has the final say. A validator `accept` becomes
  `confirmed` (defect) or `supported_risk` (risk) only if at least one cited
  evidence item is verified, none is contradicted, and every location
  exists. Otherwise the finding is `needs_investigation`.
- A finding whose validation failed is `unvalidated`, never accepted.
- Candidates are never modified. The final state is derived once, from
  candidates plus decision records (`findings.Merge`), and stored in
  `findings.json`. If validation did not finish, the report derives it the
  same way from the decisions recorded so far, so the run still reports
  correctly.
- Final states never change after the run. `sift verify` (not implemented
  yet) re-checks findings at a later revision in a new run linked to the
  original, and records outcomes there (`fixed`, `still_present`,
  `cannot_tell`, `unverified`); see
  [ADR 0006](decisions/0006-verify-runs.md) (proposed).

## Run status

| Status | Meaning | `sift audit` exit |
|---|---|---|
| `completed` | Every task succeeded | 0 |
| `partial` | Discovery succeeded, some later task failed; report marked partial | 2 |
| `failed` | Discovery failed, or orchestration or reporting failed | 1 |
| `interrupted` | Canceled (SIGINT/SIGTERM); completed work and a partial report are kept | 130 |
| `running` | In progress. `sift status` reports `interrupted` if the recorded PID is gone | n/a |

## Error handling

| Failure | Handling |
|---|---|
| Harness missing, or missing a required flag | `Probe` fails; no run is created |
| Cost estimate over `cost.budget_usd` | Confirmed at a prompt (stdin and stderr both terminals) or with `--yes`; otherwise exit 1 and no run is created (ADR 0007) |
| Agent process failure | Retried (`agents.retries`, default 1); then the task fails |
| Invalid structured output | Retried with the validation error appended to the prompt; then the task fails |
| Timeout, budget exhausted, cancellation | Not retried; task fails or is interrupted |
| Pruv fails for a finding | That finding is `unvalidated`; the others continue; validation fails; run is `partial` |
| Missing or invalid artifact | Strict read error naming the file and run |
| Incomplete discovery | Warning plus report limitation; the review proceeds |
| Process killed | Atomic writes leave complete artifacts; `sift status` detects the dead PID; `sift report --regenerate` rebuilds reports |
| Out-of-scope tool call | Denied by the harness, counted per execution, and recorded as a warning |

## Testing

- Unit tests cover config, contracts, schema generation, evidence checks,
  the gate, state transitions, the scheduler, storage, snapshots, agent
  loading, and the Claude adapter. The adapter runs against a scripted fake
  `claude` binary, including timeout, process-group kill, and cancellation.
- Orchestrator and CLI tests use `harness/fake` with a fixture repository
  that has a real defect, a finding citing fabricated code (which the gate
  must catch), and a finding Pruv rejects. They cover the failure paths:
  reviewer failure, discovery failure, invalid output, Pruv failure,
  interruption, dirty tree, and misconfigured storage.
- `make e2e` runs a live audit of the fixture repository (`SIFT_E2E=1`)
  through each harness in `SIFT_E2E_HARNESS` (default `claude-code`; e.g.
  `claude-code,kiro`). It checks the planted defect is found and confirmed,
  every cited path is repository-relative, the run records its harness and
  unenforced guarantees, and the repository is unchanged. It is skipped by
  default.
- `make conformance` runs the harness conformance suite live
  (`SIFT_CONFORMANCE=1`). Each check runs the adapter in a hostile sandbox
  (planted instruction files, hooks, and MCP configs; a secret outside the
  snapshot; a secret environment variable) and passes only on hard evidence:
  no file changed, no secret in the output, no process left running, and the
  right error kind. The suite's own tests inject each violation into a
  scripted harness to prove it is caught.

## Known risks and limits

- **Isolation is enforced by the harness, not the OS.** See ADR 0002 for
  what is and isn't guaranteed.
- **Prompt injection:** agents read repository content. The evidence gate
  limits what injected text can achieve: it can't make a fabricated
  citation pass. It can still mislead analysis.
- **LLM nondeterminism:** runs aren't bit-reproducible. Reproducibility
  means every input (snapshot, digest, config, agent versions, harness
  version, prompts) and every output is recorded, so a result can be
  examined and re-checked.
- **Cost scales with findings and files:** one Pruv execution per finding,
  and reviewer cost grows with the snapshot. Before a run is created, Sift
  estimates its cost from configured rates (`internal/cost`, ADR 0007) and
  records the estimate and the actual reported spend in the manifest's
  `cost` field. The CLI prints the estimate and, when its high end exceeds
  `cost.budget_usd`, asks when stdin and stderr are both terminals and
  otherwise refuses, unless `--yes` is given. The rates are guesses until calibrated against those
  records, and retries are not modelled. The number of Pruv executions is
  bounded by `max_findings_per_reviewer`; each execution by
  `agents.max_budget_usd` (not enforceable on Kiro, ADR 0005).
- **Not implemented yet:** additional reviewers (performance, security,
  …), resuming an interrupted run, `sift verify`, and execution evidence
  (would need a sandboxed command policy).
