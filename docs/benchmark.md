# Benchmark

The benchmark measures the quality of Sift's findings: how many real
problems the reviewers find, how many false ones they raise, and how well
Pruv and the evidence gate separate the two. It runs live audits over
fixture repositories with known contents and scores the findings against
an answer key per fixture. Decisions are recorded in
[ADR 0008](decisions/0008-benchmark.md).

Each fixture plants two kinds of item:

- **Defects**: real problems. A reviewer should report them and Pruv
  should accept them.
- **Decoys**: code that looks wrong but is shown correct by the source
  itself. A reviewer may flag them; Pruv or the gate should reject them.

Scoring (`internal/bench`) is offline and deterministic: the same findings,
keys and rules always give the same scores.

## Running

The live benchmark is a Go test, `TestBenchLive` in `internal/bench`,
gated by `SIFT_BENCH=1` like `make e2e`. It makes real model calls, so it
costs money (Claude) or Kiro credits, and it spends nothing until you give
it a budget:

```sh
make bench                    # print the estimate only; spends $0
make bench BENCH_BUDGET=26    # run within a $26 budget (see Budget)
```

Without a budget it probes the harness (free), materializes every fixture,
prints the estimate, and skips with "estimate only; nothing spent", naming
the minimum budget and the recommended one for a run that must complete
(see Budget). With
one, it audits each fixture once per trial through the real `audit.Run`,
prints a summary table, writes the results file, and compares it with the
committed baseline when there is one. A plain `go test ./...` never
reaches a harness.

Ctrl-C, SIGTERM, or the test deadline (`make bench` sets `-timeout 6h`)
cancels the audit in flight, which kills its harness processes. The runs
not finished are recorded as interrupted (incomplete), the results are
still written, and the test then fails. A second Ctrl-C quits at once,
without writing results. The live test stops 10 minutes before the
deadline (a tenth of the time left, if that is less), so the results are
written before `go test` panics.

| Variable | Make variable | Default | Meaning |
|---|---|---|---|
| `SIFT_BENCH` | (set by `make bench`) | unset | `1` enables the live test. |
| `SIFT_BENCH_BUDGET_USD` | `BENCH_BUDGET` | unset (estimate only) | Spend limit in USD for the whole benchmark: a finite number, at least 0. |
| `SIFT_BENCH_TRIALS` | `BENCH_TRIALS` | `1` | Audits per fixture, 1 to 10. The estimate multiplies by it. |
| `SIFT_BENCH_FIXTURES` | `BENCH_FIXTURES` | all | Comma-separated fixture names to run. |
| `SIFT_BENCH_UPDATE_BASELINE` | `BENCH_UPDATE_BASELINE` | unset | `1` rewrites `bench/baseline.json` (see Baseline). |
| `SIFT_BENCH_HARNESS` | | `claude-code` | `claude-code` or `kiro`. |
| `SIFT_BENCH_MODEL` | | `sonnet` (`claude-sonnet-5.5` for Kiro) | Model passed to the harness, as in `make e2e`. |
| `SIFT_BENCH_OUT` | | `bench/results/<UTC stamp>.json` | Results file; a relative path is relative to the repository root. It may not be `bench/baseline.json` (checked before any spend). `bench/results/` is gitignored. |
| `SIFT_BENCH_RUNS_DIR` | | `$TMPDIR/sift-runs-<stamp>` | Where the audit runs are kept. Agents see their working directory (`<runs_dir>/<id>/source`), so the default does not mention the benchmark; fixture repositories are likewise created as `$TMPDIR/sift-fixture-*`. |
| `SIFT_BENCH_RESCORE` | `RESULTS` | | Results file for `TestBenchRescore` (see Rescoring); a relative path is relative to the repository root. |

`make bench` passes each make variable only when it is set, so a
`SIFT_BENCH_*` value already in the environment is not overridden by an
empty one. The flip side: a `SIFT_BENCH_*` variable exported in your shell
applies to every `make bench`, so check for a lingering
`SIFT_BENCH_UPDATE_BASELINE=1` or `SIFT_BENCH_FIXTURES`.

The audit configuration is `config.Default()` with the harness, model and
runs directory above: every registered reviewer runs, with the default
`max_findings_per_reviewer`, `max_parallel_agents` and
`agents.max_budget_usd`. The driver's logic lives in `internal/bench`
(`Run`, `Compare`, `ReadResults`), not in the test, so a CLI can use it
later.

## Fixture layout

```text
<fixtures dir>/
  <name>/
    key.yaml      answer key; its `fixture` must equal <name>
    repo/         the source tree audited, committed as plain files
      ...
```

At test time each `repo/` tree is copied into a fresh git repository and
audited there. Every entry under `repo/` must be a regular file; symlinks
are rejected. Each fixture has two digests, recorded with results:

- `source_digest`: SHA-256 over every (path, content) pair, sorted by path.
- `key_digest`: SHA-256 of the `key.yaml` bytes.

A digest change shows that a fixture or its key changed between two runs.
A comparison between two results includes only fixtures whose
`source_digest` is the same on both sides; the others are left out, with a
note saying so. Fixture files are marked `-text` in `.gitattributes`, so
line-ending conversion on checkout never changes their bytes; digests and
anchors work on the raw bytes.

`internal/bench/testdata/selftest/` is a minimal fixture used by offline
tests. It is not part of the benchmark.

## Fixtures

The benchmark fixtures live in `internal/bench/testdata/fixtures/`. Each
is a small, self-contained project of about ten files. Their keys are the
source of truth for what each one plants.

| Fixture | Language | Description | Defects | Decoys |
|---|---|---|---|---|
| `ledger` | Go | In-memory accounts ledger: domain package, HTTP API, server command, tests | 4 (3 correctness, 1 architecture) | 3 (2 correctness, 1 architecture) |
| `invoicer` | Python | SQLite-backed invoicing CLI: models, tax, repository, reports, tests | 4 (3 correctness, 1 architecture) | 2 (1 correctness, 1 architecture) |
| `jobrunner` | TypeScript | In-process job queue with retries and an Express API | 4 (2 correctness, 2 architecture) | 3 (2 correctness, 1 architecture) |

In total there are 12 defects and 8 decoys, plus one `known` entry in
`invoicer`. `fixtures_test.go` checks offline that every key loads against
its source, that every reviewer category has at least one defect and one
decoy, and these totals. It also checks that `ledger` passes `go vet` and,
when `python3` or `tsc` is on the PATH, that `invoicer` compiles and
`jobrunner` type-checks. The `jobrunner` check supplies minimal Express and
Node declarations, since the fixture's dependencies are not installed.

Known adjacency: two pairs of entries sit close enough that a finding
about one can be credited to the other. In `ledger`, A-D2's balance check
(`transfer.go` 40-46) and A-D1's debit and credit (55-60) are in one
function, so a race finding that cites the debit call matches A-D1. In
`invoicer`, B-D2's effect shows in the unpaid count in `summarize`
(`reports.py` 34), inside B-D3's window. When triaging results, check
matches on these entries for cross-credit.

## Answer key

```yaml
version: 1
fixture: ledger
entries:
  - id: A-D1
    kind: defect
    category: correctness
    title: Transfer leaves the debit applied when the credit fails
    locations: [{path: ledger/transfer.go, start_line: 31, end_line: 44}]
    anchor: "if err := s.credit(to, amt); err != nil {"
    notes: why it is a defect
```

| Field | Required | Meaning |
|---|---|---|
| `version` | yes | Key schema version. Only `1` is supported. |
| `fixture` | yes | Fixture name; must equal the directory name. |
| `entries[].id` | yes | Unique within the key. Convention: `<fixture letter>-D<n>` for defects, `-X<n>` for decoys, `-K<n>` for known issues. |
| `entries[].kind` | yes | `defect`, `decoy`, or `known` (a real issue that was not planted; matching it is neutral). |
| `entries[].category` | yes | A registered reviewer category (`architecture`, `correctness`): the reviewer expected to find it. |
| `entries[].title` | yes | One line describing the item. |
| `entries[].locations` | yes | One or more `{path, start_line, end_line}`; path relative to `repo/`, lines 1-based and inclusive. |
| `entries[].anchor` | yes | Text copied verbatim from the file; its first occurrence in the file must start within the first location. |
| `entries[].notes` | for `known` | Why it is a defect, why the decoy is wrong, or why a known issue is real. Required for `known`, recommended for the rest. |

Unknown fields and more than one YAML document are rejected. A key is
checked on load:

- On its own: the version, the enums and category, unique ids, a title and
  a non-blank anchor, at least one location, a clean repository-relative
  path, `start_line` at least 1 and `end_line` not before it, notes on
  every `known` entry, and no overlap between any two entries' locations
  in the same file once each is widened by 3 lines on both sides (so
  10–12 and 15–16 conflict; 10–12 and 19–20 do not).
- Against `repo/`: every path exists, every range is within its file, and
  each anchor's first occurrence in its file starts within the first
  location. If the code moves, the anchor check fails and the key must be
  updated.
- Source hints: no file under `repo/` may contain planted, decoy(s), or
  answer key (any case), or the markers BUG, FIXME, or XXX (capitals, as
  whole words, so `debug` or `bugReport` pass).

## Authoring checklist

- Write the code as a real project would. A defect should be a mistake a
  competent developer could make; a decoy should look suspicious at first
  glance but be refutable from the source alone (a guard elsewhere, an
  invariant established by the constructor), with no runtime knowledge
  needed.
- No hints in the source: no comments pointing at the item, and none of
  the words planted, decoy, BUG, FIXME, or XXX anywhere under `repo/`.
- Set each location to the lines a reviewer would cite, and copy an anchor
  from the first one that does not occur earlier in the file.
- Keep entries more than 6 lines apart in the same file, so their widened
  windows do not overlap.
- Write `notes` for every entry: they are what a reviewer of the fixture
  checks the entry against.
- Add new fixtures over time rather than editing old ones, so earlier
  results stay comparable; when a fixture must change, its digests show it.
- A `known` entry records a real issue found in a fixture that was not
  planted. It needs notes and is reviewed in the change that adds it.

## Matching rules (`match/v1`)

An observation is one candidate finding with its validation outcome. It is
matched against a fixture's key by location only.

- **M1, location.** Observation O matches entry E when some location of O
  and some location of E have the same repository-relative path and O's
  line range overlaps E's range widened by T = 3 lines on each side. An
  observation location spanning more than 60 lines is ignored: it is too
  vague to credit.
- **M2, category.** Category is ignored for matching. Whether O's category
  equals E's is recorded (`category_match`). A matched defect is counted
  as `off_category_tp` when no matching finding in the scope, duplicates
  included, has the defect's category.
- **M3, one entry per observation.** If O's locations hit several entries,
  the entry hit by O's earliest-listed location wins. Key validation means
  a single-line location can hit only one entry; if a multi-line location
  hits several, the first in key order wins.
- **M4, one observation per entry.** Within a scope, observations are
  processed in ID order: by reviewer registry order (ARCH before CORR),
  then by number. The first match of an entry claims it; later matches are
  duplicates.
- **M5, outcomes.** The first match of a defect is a true positive (`tp`),
  of a decoy `decoy`, of a known issue `known`. An observation matching
  nothing is `unkeyed`. Decoys and unkeyed observations are false
  positives; known and duplicate matches are neutral.

Any change to these rules changes `RulesVersion`.

## Metrics

Counts are summed over fixtures (and trials) first; rates are computed
from the sums. A rate whose denominator is zero is reported as `null`,
never as 0 or 1. Every run counts all the defects in its key, so an
incomplete run (an audit that failed or stopped) scores what it did not
report as misses, and is counted in `incomplete_runs`. Every fixture must
have the same set of trials, each once (scoring refuses otherwise), so the
driver records a run for each (fixture, trial), marking a failed audit
incomplete rather than leaving it out.

An observation is **accepted** when its final state is `confirmed` or
`supported_risk`.

**Candidate stage, per reviewer R.** Scope: the observations R reported,
matched among themselves.

- `findings`, `tp`, `decoys_flagged`, `unkeyed`, `known`, `duplicates`,
  `off_category_tp`.
- precision = tp / (tp + decoys_flagged + unkeyed).
- recall = defects of category R matched by R / defects of category R.

**Candidate union.** Scope: all observations together. Recall = defects
matched by any candidate / all defects. Findings from a reporter that is
not a registered reviewer are in this scope but in no reviewer's, and are
counted as `unattributed`, so the reviewers' findings plus `unattributed`
equal the union's.

**Final stage, per reviewer and overall.** The same definitions over
accepted observations only.

**Pruv.**

- precision and recall: the final-stage overall values.
- retention = defects accepted / defects found by any candidate.
- `defects_lost_by`: defects found but not accepted, by what stopped them:
  `pruv_reject`, `pruv_investigate`, `gate` (Pruv accepted but Sift's
  evidence or location checks failed), or `unvalidated` (some matching
  finding has no decision). When several findings match one defect, an
  unvalidated one decides; otherwise the mechanism closest to acceptance
  does (gate, then investigate, then reject).
- decoy rejection: a decoy flagged by any reviewer is **rejected** when
  every matching finding has a decision and none is accepted, with the
  same breakdown by mechanism. A flagged decoy with no accepted match but
  an unvalidated one is **unresolved** and left out of the denominator.
  `decoy_rejection_rate` = rejected / (flagged − unresolved).
- `decoys_accepted`, `decoys_not_flagged`, and `unvalidated` (findings with
  no decision) are reported alongside.

Scores are reported for each fixture and in aggregate. With more than one
trial, each rate's minimum and maximum across per-trial aggregates is
reported too.

## Budget

`SIFT_BENCH_BUDGET_USD` has no default; unset or 0 means estimate only.
Three checks stand between the budget and the spend:

1. **The estimate.** The harness is probed (free: Claude Code's probe only
   runs `--version` and `--help`), every fixture is materialized, and one
   audit of each is estimated with `cost.ForAudit` exactly as `audit.Run`
   will (ADR 0007). The total over every (fixture, trial) is printed, with
   the budget labelled `SIFT_BENCH_BUDGET_USD`. No budget: skip. A high
   estimate above the budget: fail. Neither runs an agent.
2. **Per audit.** Before each audit, the spend so far (as charged below)
   plus that audit's high estimate must fit the budget; otherwise the
   remaining runs are skipped. Each audit runs with `cost.budget_usd` set
   to the budget left, and a preflight that always refuses, so an audit
   whose own estimate exceeds what is left never starts. Its manifest
   records what it was allowed.
3. **Per execution.** Every agent execution of every audit goes through
   one guard. On a harness that enforces a per-execution cap (Claude Code,
   via `--max-budget-usd`, set from `agents.max_budget_usd`), an execution
   starts only if the amount charged so far, plus the caps of the
   executions in flight, plus its own cap fit the budget. Its cap is
   reserved while it runs. When it ends it is charged its reported cost,
   or its whole cap if it reported none (a timeout, a crash, a missing
   result). Retries are separate executions and are guarded the same way.

So on Claude Code the budget is a hard cap, as far as Claude Code holds
each execution to its `--max-budget-usd`: nothing starts unless its worst
case fits. An execution that does not fit is refused. Its audit then ends
partial, so that run is incomplete, and the results say how many
executions were refused. The results record the reported spend
(`actual_usd`) and the guard's count (`charged_usd`) separately, with a
warning when any execution was charged at its cap.

**Headroom.** Near the end of the budget, executions are refused while
the worst case of those in flight is reserved. The reserve is up to
`max_parallel_agents` × `agents.max_budget_usd` ($6 by default). The
estimate also leaves out retries, and its rates are configured, not
measured. For a baseline run, where an incomplete run would be refused,
use a budget of at least the larger of the high estimate plus the reserve
and 1.5 times the high estimate, rounded up to a whole dollar. The factor
of 1.5 is a margin for retries and estimate error, a judgement rather
than a measurement; revisit it once results record actual spend against
the estimate. The estimate-only run prints this figure. For the current
fixtures it is $26: a $17.14 high estimate, and 1.5 × $17.14 = $25.71
beats $17.14 + $6. On a capped harness, a budget below the high estimate
plus the reserve gets a warning before any spend that the run is likely
to end incomplete.

**Kiro** enforces no per-execution cap and reports no cost (ADR 0005).
There a cap is no ceiling, so the guard charges only reported cost and
the estimate is the only guard on spend. The results carry a warning
saying so.

Runs go trial by trial (every fixture's trial 1, then trial 2), so a stop
leaves the earlier trials complete.

## Results, baseline, and comparison

### Results file

A results file is JSON, written atomically. It keeps every run's raw
observations, so its scores can always be recomputed:

| Field | Meaning |
|---|---|
| `bench_version` | Results format version, `1`. |
| `created_at` | When the benchmark started (UTC). |
| `sift` | `commit` and `dirty` of the Sift tree the benchmark ran from. Anything uncommitted is dirty, untracked files included, except under `bench/results/`. A rename counts both its paths. Gitignored paths (`bench/results/`, `.claude/`) never show up. |
| `harness` | `name`, `version` (from the probe) and `model`. |
| `agents` | For each agent definition used: `version` and `sha256`. |
| `config` | `reviewers` and `max_findings_per_reviewer`. |
| `matching` | `rules` (`match/v1`), `line_tolerance`, `max_location_span`. |
| `trials` | Audits per fixture. |
| `cost` | `estimate` (the total), `budget_usd`, `actual_usd` (reported), `charged_usd` (counted by the guard), `executions` (started), `reported_executions`, `capped_executions` (charged at their cap), `refused_executions`. Refused executions never started, so they are not in `executions`. |
| `fixtures[]` | `name`, `source_digest`, `key_digest`, and `runs[]`: `trial`, `run_id`, `runs_dir`, `status` (the run's status; `failed` when no run was created; `skipped` or `interrupted` when it never started), `complete`, `approval` (the audit's cost approval), `actual_usd` (from the run's manifest), `charged_usd` and `executions` (from the guard: executions started, as in `cost.executions`; the run's manifest also counts refused attempts), `error`, `observations`. |
| `warnings` | Spend-guard stops, refused or cap-charged executions, interruptions, a harness that enforces no cap or reports no cost. |
| `scores` | `Score` over the observations (see Metrics). |
| `comparison` | The comparison with the baseline, when one was made. |

`ReadResults` is strict about what was recorded: an unknown field in the
setup, runs or observations is an error. `scores` and `comparison` are
derived, so they are read leniently: unknown fields are ignored, and a
part that no longer fits the current layout is dropped, to be recomputed
by rescoring. An older results file therefore stays rescorable after the
score layout changes.

### Comparison

A comparison rescores **both** results from their stored observations with
the current keys and matching rules, over the fixtures whose
`source_digest` is the same in the baseline, in the new result, and in the
current fixture. Other fixtures are left out with a note. A different
harness, harness version, or model adds a "not like-for-like" note, so a
change caused by a new Claude Code or Kiro release is not read as a change
in Sift.

Guarded rates: each reviewer's candidate and final precision and recall,
and Pruv precision, recall, and decoy rejection rate. For each, with n the
larger of the two sides' denominators:

- tolerance = max(0.10, 1/n);
- a fall of more than the tolerance is `regressed`, a rise of more than it
  `improved`, anything else `unchanged`;
- a rate that is null on either side is `n/a`; a reviewer that did not
  run in the baseline is `new`.

The overall verdict:

- `incomplete` when any compared run, on either side, is incomplete. The
  note says how many ("current: 1 of 3 runs incomplete; baseline: 0 of
  3"). An incomplete run scores what it never reported as misses, and a
  run whose validation failed can leave every rate unchanged or `n/a`, so
  its rates are not evidence either way.
- otherwise `regressed` if any rate regressed, `improved` if at least one
  improved and none regressed, and `no_regression` otherwise.

The verdict is printed and stored in the results; it never fails the test.
One trial is a noisy sample: a single-trial `regressed` means rerun once
before concluding anything, and so does `incomplete`.

### Baseline

`bench/baseline.json` is the committed reference result. Every live run
compares against it when it exists; a comparison that cannot be made (for
example, no fixture in common) is logged as a note. Recording it is a
separate, human-approved step: run

```sh
make bench BENCH_BUDGET=<n> BENCH_UPDATE_BASELINE=1
```

with the headroom described under Budget, and commit the file after
reviewing it. The update is refused before any spend when a fixture
filter is set or the Sift tree is dirty, and after the run, before
anything is written to `bench/baseline.json`, when any run is incomplete.
`SIFT_BENCH_OUT` can never point at the baseline.

### Rescoring

A change to a key or to the matching rules changes scores without any new
audit. `make bench-rescore RESULTS=<file>` (default: the baseline; it
stops with a message when the file does not exist) runs
`TestBenchRescore`. That test reads the file and rescores it with the
current keys and rules. It compares it with the baseline when there is
one and the file is not the baseline itself (checked by file identity).
It then prints the summary and rewrites the file's scores. It is offline
and free. Fixtures whose source changed, or that no longer exist, are left
out of the rescored scores with a note.

### Wording in tickets

A ticket that changes prompts, Pruv, the gate, or harness settings states
its effect as "improves the benchmark" or "does not regress the
benchmark", quoting the comparison verdict of a run against the committed
baseline (for example: "`make bench BENCH_BUDGET=26`: no_regression
against baseline 2026-11-20 (abc1234)"). A `regressed` or `incomplete`
verdict is rerun once before it is reported.
