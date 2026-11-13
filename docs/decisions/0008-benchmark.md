# ADR 0008: Benchmark finding quality against planted defects and decoys

**Status:** accepted. The scoring core lands first; the fixtures, the
driver, and the baseline land in follow-up changes.

## Context

Changes to agent prompts, Pruv, the evidence gate, or harness settings
change the findings Sift reports, and nothing measures whether they got
better or worse. The end-to-end suite checks that an audit completes, not
that its findings are right. We need a repeatable measure of reviewer
precision and recall, and of how well Pruv and the gate stop plausible but
wrong findings, cheap enough to run before such changes merge.

## Decision

1. **A Go test, not a subcommand.** The benchmark is a Go test gated by
   `SIFT_BENCH=1`, like `make e2e` and `make conformance`. It is a
   development tool for Sift itself, so it does not become part of the
   `sift` CLI surface.

2. **Fixtures are committed trees.** Each fixture is a directory holding a
   `key.yaml` answer key and a `repo/` tree of plain files, copied into a
   fresh git repository at test time. Keeping them as plain files (not
   nested repositories or archives) keeps them reviewable in diffs.
   Each fixture plants defects, which should be confirmed, and decoys,
   which look wrong but are refutable from the source and should be
   rejected by Pruv or the gate.

3. **Matching is by location, not category.** A finding matches a key
   entry when they share a path and the finding's lines overlap the entry's
   lines widened by 3 on each side. Finding locations spanning more than
   60 lines are ignored. Category is recorded but not required, so a real
   defect reported by the "wrong" reviewer still counts as found. Matching
   is one-to-one per scope, in finding ID order. The rules are versioned
   (`match/v1`) and specified in `docs/benchmark.md`.

4. **Results keep raw findings.** A result stores each run's observations
   (ID, reviewer, category, title, locations, state, verdict), not only
   scores; a gate demotion is an accept verdict on a finding that was not
   accepted. A comparison rescores both sides with the current keys and
   rules, so fixing a key or a rule never compares old scores with new. It
   includes only fixtures whose `source_digest` is the same on both sides;
   the others are left out, with a note saying so.

5. **A committed baseline.** `bench/baseline.json` is the reference
   result. A comparison flags a rate as regressed when it falls by more
   than max(0.10, 1/n), where n is the larger of the two results'
   denominators for that rate (baseline and current). The verdict is not
   fatal: a regression means rerun once before acting on it.

6. **A budget with no default.** `SIFT_BENCH_BUDGET_USD` sets the spend
   limit. Unset, the benchmark only prints the estimate and spends $0. The
   `cost.Total` estimate for the whole benchmark is printed before any
   agent runs (ADR 0007).

7. **One trial by default.** The number of trials per fixture is
   configurable. With more than one, counts are summed across trials and
   each rate's per-trial minimum and maximum is reported. Every fixture
   must have the same trials, so the driver records a run for each
   (fixture, trial), marking a failed audit incomplete.

8. **Known issues are allowed.** A real issue found in a fixture but not
   planted is recorded as a `known` entry, which requires notes and is
   reviewed in the change that adds it. Matching it is neutral, neither a
   true nor a false positive.

## Consequences

- **Single-trial noise.** Agent output varies between runs, so one trial
  is a noisy sample, especially with few fixtures. The tolerance and the
  rerun-once rule absorb some of it; more trials cost proportionally more.
- **Overfitting.** Prompts tuned against the fixtures may do better on
  them than on real code. Mitigations: fixtures are written as realistic
  code with no hint comments or leak words, new fixtures are added over
  time, and benchmark gains are read alongside real audits.
- **Unplanted real issues.** A fixture can contain a genuine issue nobody
  planted, which then scores as an unkeyed false positive until it is
  recorded as `known`.
- **Line drift.** Editing a fixture moves lines out from under its key.
  Anchors (verbatim text whose first occurrence in the file must start
  within an entry's first location) make this fail validation instead of
  silently mis-scoring, and source and key digests in every result show
  when a fixture changed between two results. Fixture files are marked
  `-text` in `.gitattributes` so line-ending conversion on checkout never
  changes their bytes or digests.
