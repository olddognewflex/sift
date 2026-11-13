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

(added in the driver change)

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

(added in the driver change)

## Results, baseline, and comparison

(added in the driver change)
