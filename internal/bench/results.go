package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/cost"
	"github.com/olddognewflex/sift/internal/source"
	"github.com/olddognewflex/sift/internal/storage"
)

// ResultsVersion is the benchmark results file version (bench_version).
const ResultsVersion = 1

// Statuses of runs that never started, beyond contract.RunStatus: stopped
// by the spend guard, or by an interruption.
const (
	StatusSkipped     = "skipped"
	StatusInterrupted = "interrupted"
)

// Results is one benchmark run: its setup, every audit's raw observations,
// and the scores derived from them. Observations are kept so the file can
// be rescored with later keys and rules (docs/benchmark.md).
type Results struct {
	BenchVersion int                  `json:"bench_version"`
	CreatedAt    time.Time            `json:"created_at"`
	Sift         SiftInfo             `json:"sift"`
	Harness      HarnessInfo          `json:"harness"`
	Agents       map[string]AgentInfo `json:"agents"`
	Config       ConfigInfo           `json:"config"`
	Matching     MatchingInfo         `json:"matching"`
	Trials       int                  `json:"trials"`
	Cost         CostInfo             `json:"cost"`
	Fixtures     []FixtureResult      `json:"fixtures"`
	Warnings     []string             `json:"warnings,omitempty"`
	Scores       Scores               `json:"scores"`
	Comparison   *Comparison          `json:"comparison,omitempty"`
}

// SiftInfo is the Sift tree the benchmark ran from.
type SiftInfo struct {
	Commit string `json:"commit"`
	Dirty  bool   `json:"dirty"`
}

type HarnessInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Model   string `json:"model"`
}

type AgentInfo struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type ConfigInfo struct {
	Reviewers              []string `json:"reviewers"`
	MaxFindingsPerReviewer int      `json:"max_findings_per_reviewer"`
}

type MatchingInfo struct {
	Rules           string `json:"rules"`
	LineTolerance   int    `json:"line_tolerance"`
	MaxLocationSpan int    `json:"max_location_span"`
}

// CostInfo is the whole benchmark's estimate (every fixture × trial), its
// budget, and its spend: ActualUSD as the harness reported it, ChargedUSD
// as the budget guard counted it (an execution that reported no cost on a
// harness that enforces caps is charged its whole cap). Executions counts
// the harness executions started; refused ones never started.
type CostInfo struct {
	Estimate           contract.CostEstimate `json:"estimate"`
	BudgetUSD          float64               `json:"budget_usd"`
	ActualUSD          float64               `json:"actual_usd"`
	ChargedUSD         float64               `json:"charged_usd"`
	Executions         int                   `json:"executions"`
	ReportedExecutions int                   `json:"reported_executions"`
	CappedExecutions   int                   `json:"capped_executions"`
	RefusedExecutions  int                   `json:"refused_executions"`
}

type FixtureResult struct {
	Name         string      `json:"name"`
	SourceDigest string      `json:"source_digest"`
	KeyDigest    string      `json:"key_digest"`
	Runs         []RunResult `json:"runs"`
}

// RunResult is one audit of a fixture. Status is the run's manifest
// status, "failed" when no run was created, or "skipped" or "interrupted"
// when it never started. ActualUSD comes from the run's manifest;
// ChargedUSD and Executions are the budget guard's, so Executions counts
// the executions started (the manifest also counts refused attempts).
type RunResult struct {
	Trial        int                   `json:"trial"`
	RunID        string                `json:"run_id,omitempty"`
	RunsDir      string                `json:"runs_dir,omitempty"`
	Status       string                `json:"status"`
	Complete     bool                  `json:"complete"`
	Approval     contract.CostApproval `json:"approval,omitempty"`
	ActualUSD    float64               `json:"actual_usd"`
	ChargedUSD   float64               `json:"charged_usd"`
	Executions   int                   `json:"executions"`
	Error        string                `json:"error,omitempty"`
	Observations []Observation         `json:"observations"`
}

func currentMatching() MatchingInfo {
	return MatchingInfo{Rules: RulesVersion, LineTolerance: LineTolerance, MaxLocationSpan: MaxLocationSpan}
}

func (f FixtureResult) runObs() []RunObs {
	out := make([]RunObs, len(f.Runs))
	for i, r := range f.Runs {
		out[i] = RunObs{Trial: r.Trial, Complete: r.Complete, Observations: r.Observations}
	}
	return out
}

// scorable pairs each result fixture whose source is unchanged with the
// current fixture's key. The notes say which were left out, and why.
func scorable(rs []FixtureResult, current []Fixture) (map[string]FixtureRuns, []string) {
	byName := map[string]Fixture{}
	for _, f := range current {
		byName[f.Name] = f
	}
	out := map[string]FixtureRuns{}
	var notes []string
	for _, fr := range rs {
		switch f, ok := byName[fr.Name]; {
		case !ok:
			notes = append(notes, fmt.Sprintf("fixture %s no longer exists; left out", fr.Name))
		case f.SourceDigest != fr.SourceDigest:
			notes = append(notes, fmt.Sprintf("fixture %s: source changed (source_digest differs from the current fixture); left out", fr.Name))
		default:
			out[fr.Name] = FixtureRuns{Key: f.Key, Runs: fr.runObs()}
		}
	}
	return out, notes
}

// Rescore recomputes the scores from the stored observations with the
// given fixtures' keys and the current matching rules. Fixtures that no
// longer exist or whose source changed are left out; the notes say which.
// The comparison is cleared: it was computed from the old scores.
func (r *Results) Rescore(fixtures []Fixture) ([]string, error) {
	ok, notes := scorable(r.Fixtures, fixtures)
	var in []FixtureRuns
	for i, fr := range r.Fixtures {
		if runs, found := ok[fr.Name]; found {
			r.Fixtures[i].KeyDigest = keyDigest(fixtures, fr.Name)
			in = append(in, runs)
		}
	}
	s, err := Score(in)
	if err != nil {
		return notes, err
	}
	r.Scores, r.Matching, r.Comparison = s, currentMatching(), nil
	return notes, nil
}

func keyDigest(fixtures []Fixture, name string) string {
	for _, f := range fixtures {
		if f.Name == name {
			return f.KeyDigest
		}
	}
	return ""
}

// Incomplete counts runs that did not complete.
func (r *Results) Incomplete() int {
	n := 0
	for _, f := range r.Fixtures {
		for _, run := range f.Runs {
			if !run.Complete {
				n++
			}
		}
	}
	return n
}

// WriteResults writes r as indented JSON, atomically.
func WriteResults(path string, r *Results) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteAtomic(path, append(data, '\n'))
}

// ReadResults reads a results file. Everything recorded (setup, runs,
// observations) is decoded strictly: unknown fields, trailing data, and an
// unsupported bench_version are errors. Scores and the comparison are
// derived and recomputed by Rescore, so they are decoded leniently: unknown
// fields are ignored, and a part that no longer fits is dropped.
func ReadResults(path string) (*Results, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Results
		Scores     json.RawMessage `json:"scores"`
		Comparison json.RawMessage `json:"comparison"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("results %s: %w", path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("results %s: trailing data after the results object", path)
	}
	r := f.Results
	if r.BenchVersion != ResultsVersion {
		return nil, fmt.Errorf("results %s: unsupported bench_version %d (supported: %d)", path, r.BenchVersion, ResultsVersion)
	}
	if json.Unmarshal(f.Scores, &r.Scores) != nil {
		r.Scores = Scores{}
	}
	if len(f.Comparison) > 0 && string(f.Comparison) != "null" {
		if r.Comparison = new(Comparison); json.Unmarshal(f.Comparison, r.Comparison) != nil {
			r.Comparison = nil
		}
	}
	return &r, nil
}

// samePath reports whether a and b name the same file. Two existing files
// are compared by identity. Otherwise the parent directories are compared
// by identity (after resolving symlinks) and the names ignoring case, which
// errs toward "same" on a case-sensitive filesystem; with a missing parent
// the cleaned absolute paths are compared.
func samePath(a, b string) bool {
	if fa, err := os.Stat(a); err == nil {
		if fb, err := os.Stat(b); err == nil {
			return os.SameFile(fa, fb)
		}
	}
	da, err1 := filepath.EvalSymlinks(filepath.Dir(a))
	db, err2 := filepath.EvalSymlinks(filepath.Dir(b))
	if err1 == nil && err2 == nil {
		fa, err1 := os.Stat(da)
		fb, err2 := os.Stat(db)
		return err1 == nil && err2 == nil && os.SameFile(fa, fb) && strings.EqualFold(filepath.Base(a), filepath.Base(b))
	}
	aa, err1 := filepath.Abs(a)
	ab, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && aa == ab
}

// SiftTree returns the root of the Git repository containing dir and its
// state. Paths under bench/results/, where results are written, do not
// make the tree dirty; anything else uncommitted does, untracked files
// included.
func SiftTree(ctx context.Context, dir string) (string, SiftInfo, error) {
	st, err := source.Inspect(ctx, dir)
	if err != nil {
		return "", SiftInfo{}, err
	}
	info := SiftInfo{Commit: st.Commit}
	for _, e := range st.Porcelain {
		// Entries are "XY path" or "XY orig -> path"; a rename touches both.
		for p := range strings.SplitSeq(e[min(3, len(e)):], " -> ") {
			if !strings.HasPrefix(p, "bench/results/") {
				info.Dirty = true
			}
		}
	}
	return st.Root, info, nil
}

// Summary writes a human-readable table of the results.
func (r *Results) Summary(w io.Writer) {
	fmt.Fprintf(w, "benchmark: harness %s %s (model %s), %d fixture(s) × %d trial(s), rules %s\n",
		r.Harness.Name, r.Harness.Version, orDash(r.Harness.Model), len(r.Fixtures), r.Trials, r.Matching.Rules)
	c := r.Cost
	fmt.Fprintf(w, "cost: estimate %s, budget $%.2f; reported $%.2f, charged $%.2f over %d executions (%d charged at cap, %d refused)\n",
		cost.USDRange(c.Estimate.LowUSD, c.Estimate.HighUSD), c.BudgetUSD, c.ActualUSD, c.ChargedUSD, c.Executions, c.CappedExecutions, c.RefusedExecutions)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "fixture\ttrial\tstatus\tfindings\tactual\trun")
	for _, f := range r.Fixtures {
		for _, run := range f.Runs {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t$%.2f\t%s\n", f.Name, run.Trial, run.Status, len(run.Observations), run.ActualUSD, orDash(run.RunID))
		}
	}
	tw.Flush()
	a := r.Scores.Aggregate
	fmt.Fprintf(w, "scores (%d runs, %d incomplete, %d unattributed findings):\n", a.Runs, a.IncompleteRuns, a.Unattributed)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "stage\treviewer\tfindings\tprecision\trecall")
	for _, st := range []struct {
		name string
		s    Stages
	}{{"candidate", a.Candidate}, {"final", a.Final}} {
		for _, rv := range contract.ReviewerNames() {
			if s := st.s.Reviewers[rv]; s != nil {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", st.name, rv, s.Findings, precisionOf(*s), recallOf(*s))
			}
		}
		fmt.Fprintf(tw, "%s\toverall\t%d\t%s\t%s\n", st.name, st.s.Overall.Findings, precisionOf(st.s.Overall), recallOf(st.s.Overall))
	}
	tw.Flush()
	p := a.Pruv
	fmt.Fprintf(w, "pruv: retention %s, decoy rejection %s, decoys accepted %d, unvalidated %d\n",
		frac{p.DefectsAccepted, p.DefectsFound}, decoyFrac(p), p.DecoysAccepted, p.Unvalidated)
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
	if r.Comparison != nil {
		r.Comparison.summary(w)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
