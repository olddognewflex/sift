package bench

import (
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
)

// Comparison verdicts, per metric and overall (docs/benchmark.md).
const (
	VerdictRegressed    = "regressed"
	VerdictImproved     = "improved"
	VerdictUnchanged    = "unchanged"
	VerdictNA           = "n/a" // the rate is null on either side
	VerdictNew          = "new" // the reviewer did not run in the baseline
	VerdictNoRegression = "no_regression"
	// VerdictIncomplete: a compared run on either side is incomplete, so
	// the rates do not show what the code would do.
	VerdictIncomplete = "incomplete"
)

// MinTolerance is the smallest change in a rate that counts as a
// regression or an improvement; with few items 1/n is larger.
const MinTolerance = 0.10

// Comparison is a result compared with a baseline, both rescored with the
// current keys and rules over the fixtures they share unchanged.
type Comparison struct {
	BaselineCreatedAt string   `json:"baseline_created_at"`
	BaselineCommit    string   `json:"baseline_commit"`
	Fixtures          []string `json:"fixtures"`
	Notes             []string `json:"notes,omitempty"`
	Metrics           []Delta  `json:"metrics"`
	Verdict           string   `json:"verdict"`
}

// Delta compares one rate. Tolerance is max(0.10, 1/n), n the larger of
// the two sides' denominators.
type Delta struct {
	Metric    string   `json:"metric"`
	Baseline  *float64 `json:"baseline"`
	Current   *float64 `json:"current"`
	Delta     *float64 `json:"delta,omitempty"`
	Tolerance float64  `json:"tolerance,omitempty"`
	Verdict   string   `json:"verdict"`
}

// frac is a rate with its numerator and denominator.
type frac struct{ n, d int }

func (f frac) rate() *float64 { return ratio(f.n, f.d) }

func (f frac) String() string {
	if f.d == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.2f (%d/%d)", float64(f.n)/float64(f.d), f.n, f.d)
}

func precisionFrac(s Stage) frac { return frac{s.TP, s.TP + s.DecoysFlagged + s.Unkeyed} }
func recallFrac(s Stage) frac    { return frac{s.DefectsFound, s.Defects} }
func precisionOf(s Stage) string { return precisionFrac(s).String() }
func recallOf(s Stage) string    { return recallFrac(s).String() }

// Compare rescores baseline and current from their stored observations
// with the given (current) fixtures' keys and rules, over the fixtures
// whose source_digest is the same in both results and in the current
// fixture, and judges each guarded rate. An incomplete run on either side
// makes the verdict incomplete. Neither result is modified.
func Compare(baseline, current *Results, fixtures []Fixture) (*Comparison, error) {
	c := &Comparison{BaselineCreatedAt: baseline.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), BaselineCommit: baseline.Sift.Commit}
	bok, bnotes := scorable(baseline.Fixtures, fixtures)
	cok, cnotes := scorable(current.Fixtures, fixtures)
	for _, n := range bnotes {
		c.Notes = append(c.Notes, "baseline: "+n)
	}
	for _, n := range cnotes {
		c.Notes = append(c.Notes, "current: "+n)
	}
	inBaseline := map[string]bool{}
	for _, f := range baseline.Fixtures {
		inBaseline[f.Name] = true
	}
	inCurrent := map[string]bool{}
	var bin, cin []FixtureRuns
	for _, f := range current.Fixtures {
		inCurrent[f.Name] = true
		b, okb := bok[f.Name]
		cur, okc := cok[f.Name]
		switch {
		case !inBaseline[f.Name]:
			c.Notes = append(c.Notes, fmt.Sprintf("fixture %s is not in the baseline; left out", f.Name))
		case okb && okc:
			c.Fixtures = append(c.Fixtures, f.Name)
			bin, cin = append(bin, b), append(cin, cur)
		}
	}
	for _, f := range baseline.Fixtures {
		if !inCurrent[f.Name] {
			c.Notes = append(c.Notes, fmt.Sprintf("fixture %s is only in the baseline; left out", f.Name))
		}
	}
	if len(c.Fixtures) == 0 {
		return nil, errors.New("no fixture is comparable with the baseline")
	}
	cn, ct := incomplete(cin)
	bn, bt := incomplete(bin)
	if cn > 0 || bn > 0 {
		c.Notes = append(c.Notes, fmt.Sprintf("current: %d of %d runs incomplete; baseline: %d of %d", cn, ct, bn, bt))
	}
	if bh, ch := baseline.Harness, current.Harness; bh.Name != ch.Name || bh.Version != ch.Version || bh.Model != ch.Model {
		c.Notes = append(c.Notes, fmt.Sprintf("not like-for-like: baseline ran %s %s (model %s), this run %s %s (model %s)",
			bh.Name, orDash(bh.Version), orDash(bh.Model), ch.Name, orDash(ch.Version), orDash(ch.Model)))
	}
	bs, err := Score(bin)
	if err != nil {
		return nil, fmt.Errorf("rescore baseline: %w", err)
	}
	cs, err := Score(cin)
	if err != nil {
		return nil, fmt.Errorf("rescore current: %w", err)
	}
	b, cur := bs.Aggregate, cs.Aggregate
	for _, r := range current.Config.Reviewers {
		for _, m := range []struct {
			stage string
			b, c  Stages
		}{{"candidate", b.Candidate, cur.Candidate}, {"final", b.Final, cur.Final}} {
			bst, cst := m.b.Reviewers[r], m.c.Reviewers[r]
			if bst == nil || cst == nil {
				continue // not a registered reviewer
			}
			for _, d := range []Delta{
				judge(m.stage+"."+r+".precision", precisionFrac(*bst), precisionFrac(*cst)),
				judge(m.stage+"."+r+".recall", recallFrac(*bst), recallFrac(*cst)),
			} {
				if !slices.Contains(baseline.Config.Reviewers, r) {
					d = Delta{Metric: d.Metric, Current: d.Current, Verdict: VerdictNew}
				}
				c.Metrics = append(c.Metrics, d)
			}
		}
	}
	c.Metrics = append(c.Metrics,
		judge("pruv.precision", precisionFrac(b.Final.Overall), precisionFrac(cur.Final.Overall)),
		judge("pruv.recall", recallFrac(b.Final.Overall), recallFrac(cur.Final.Overall)),
		judge("pruv.decoy_rejection_rate", decoyFrac(b.Pruv), decoyFrac(cur.Pruv)))
	c.Verdict = VerdictNoRegression
	if cn > 0 || bn > 0 {
		c.Verdict = VerdictIncomplete
		return c, nil
	}
	for _, d := range c.Metrics {
		switch {
		case d.Verdict == VerdictRegressed:
			c.Verdict = VerdictRegressed
		case d.Verdict == VerdictImproved && c.Verdict != VerdictRegressed:
			c.Verdict = VerdictImproved
		}
	}
	return c, nil
}

// incomplete counts the incomplete runs and all runs.
func incomplete(in []FixtureRuns) (n, total int) {
	for _, f := range in {
		for _, r := range f.Runs {
			total++
			if !r.Complete {
				n++
			}
		}
	}
	return n, total
}

func decoyFrac(p Pruv) frac { return frac{p.DecoysRejected, p.DecoysFlagged - p.DecoysUnresolved} }

// judge compares one rate. A change exactly equal to the tolerance is
// unchanged; the epsilon keeps float noise from deciding that.
func judge(metric string, b, c frac) Delta {
	d := Delta{Metric: metric, Baseline: b.rate(), Current: c.rate(), Verdict: VerdictNA}
	if d.Baseline == nil || d.Current == nil {
		return d
	}
	d.Tolerance = max(MinTolerance, 1/float64(max(b.d, c.d)))
	delta := *d.Current - *d.Baseline
	rounded := math.Round(delta*1e6) / 1e6
	d.Delta = &rounded
	const eps = 1e-9
	switch {
	case delta < -d.Tolerance-eps:
		d.Verdict = VerdictRegressed
	case delta > d.Tolerance+eps:
		d.Verdict = VerdictImproved
	default:
		d.Verdict = VerdictUnchanged
	}
	return d
}

func (c *Comparison) summary(w io.Writer) {
	fmt.Fprintf(w, "comparison with baseline %s (%s) over %v: %s\n", c.BaselineCreatedAt, orDash(c.BaselineCommit), c.Fixtures, c.Verdict)
	for _, d := range c.Metrics {
		if d.Verdict == VerdictUnchanged || d.Verdict == VerdictNA {
			continue
		}
		if d.Delta == nil {
			fmt.Fprintf(w, "  %s: %s\n", d.Metric, d.Verdict)
			continue
		}
		fmt.Fprintf(w, "  %s: %.2f → %.2f (Δ %+.2f, tolerance %.2f) %s\n", d.Metric, *d.Baseline, *d.Current, *d.Delta, d.Tolerance, d.Verdict)
	}
	for _, n := range c.Notes {
		fmt.Fprintf(w, "  note: %s\n", n)
	}
	if c.Verdict == VerdictIncomplete {
		fmt.Fprintln(w, "  an incomplete run on either side makes the rates incomparable; rerun before concluding")
	}
	if c.Verdict == VerdictRegressed {
		fmt.Fprintln(w, "  a regression in a single trial is noise until a rerun confirms it")
	}
}
