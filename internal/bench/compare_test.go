package bench

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
)

func TestJudge(t *testing.T) {
	for _, tc := range []struct {
		name      string
		b, c      frac
		tolerance float64
		want      string
	}{
		{"recall 6/10 → 5/10 is within 0.10", frac{6, 10}, frac{5, 10}, 0.10, VerdictUnchanged},
		{"recall 6/10 → 4/10", frac{6, 10}, frac{4, 10}, 0.10, VerdictRegressed},
		{"recall 6/10 → 8/10", frac{6, 10}, frac{8, 10}, 0.10, VerdictImproved},
		{"precision 0.50 → 0.38", frac{25, 50}, frac{19, 50}, 0.10, VerdictRegressed},
		{"recall over 4 defects 2 → 1 is within 1/4", frac{2, 4}, frac{1, 4}, 0.25, VerdictUnchanged},
		{"n is the larger denominator (4 vs 20)", frac{3, 4}, frac{12, 20}, 0.10, VerdictRegressed},
		{"float noise at exactly the tolerance", frac{8, 10}, frac{7, 10}, 0.10, VerdictUnchanged},
		{"null baseline", frac{0, 0}, frac{1, 2}, 0, VerdictNA},
		{"null current", frac{1, 2}, frac{0, 0}, 0, VerdictNA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := judge("m", tc.b, tc.c)
			if d.Verdict != tc.want || math.Abs(d.Tolerance-tc.tolerance) > 1e-9 {
				t.Errorf("verdict %s tolerance %v; want %s %v", d.Verdict, d.Tolerance, tc.want, tc.tolerance)
			}
		})
	}
	if d := judge("m", frac{6, 10}, frac{5, 10}); d.Delta == nil || *d.Delta != -0.1 {
		t.Errorf("delta %v, want -0.1", fmtRate(d.Delta))
	}
}

// compareKey has three correctness defects and one decoy in a.go.
func compareKey(name string) Key {
	e := func(id string, k Kind, line int) Entry {
		return Entry{ID: id, Kind: k, Category: contract.Category("correctness"), Title: id,
			Locations: []Location{{Path: "a.go", StartLine: line, EndLine: line}}, Anchor: "x"}
	}
	return Key{Version: 1, Fixture: name, Entries: []Entry{
		e("D1", KindDefect, 10), e("D2", KindDefect, 30), e("D3", KindDefect, 50), e("X1", KindDecoy, 70),
	}}
}

func cobs(id string, line int, state contract.ValidationState, v contract.Verdict) Observation {
	reporter := "correctness"
	if strings.HasPrefix(id, "ARCH") {
		reporter = "architecture"
	}
	return Observation{ID: id, ReportedBy: reporter, Category: contract.Category(reporter), Title: id,
		Locations: []Location{{Path: "a.go", StartLine: line, EndLine: line}}, State: state, Verdict: v}
}

func results(harnessName string, fixtures map[string][]Observation, digests map[string]string) *Results {
	r := &Results{BenchVersion: 1, Harness: HarnessInfo{Name: harnessName, Model: "sonnet"}, Trials: 1,
		Config: ConfigInfo{Reviewers: contract.ReviewerNames(), MaxFindingsPerReviewer: 8}}
	for _, name := range []string{"f1", "f2"} {
		if o, ok := fixtures[name]; ok {
			r.Fixtures = append(r.Fixtures, FixtureResult{Name: name, SourceDigest: digests[name],
				Runs: []RunResult{{Trial: 1, Complete: true, Status: "completed", Observations: o}}})
		}
	}
	return r
}

func metric(t *testing.T, c *Comparison, name string) Delta {
	t.Helper()
	for _, d := range c.Metrics {
		if d.Metric == name {
			return d
		}
	}
	t.Fatalf("no metric %s in %+v", name, c.Metrics)
	return Delta{}
}

var (
	confirmed = contract.StateConfirmed
	rejected  = contract.StateRejected
	accept    = contract.VerdictAccept
	reject    = contract.VerdictReject
)

func TestCompare(t *testing.T) {
	fixtures := []Fixture{{Name: "f1", Key: compareKey("f1"), SourceDigest: "s1"}, {Name: "f2", Key: compareKey("f2"), SourceDigest: "s2"}}
	digests := map[string]string{"f1": "s1", "f2": "s2"}
	baseObs := []Observation{cobs("CORR-001", 10, confirmed, accept), cobs("CORR-002", 30, confirmed, accept), cobs("CORR-003", 70, rejected, reject)}

	t.Run("regression in one fixture", func(t *testing.T) {
		// f1 recall 2/3 → 0/3 overall (f2 unchanged at 2/3): 4/6 → 2/6.
		base := results("claude-code", map[string][]Observation{"f1": baseObs, "f2": baseObs}, digests)
		cur := results("claude-code", map[string][]Observation{"f1": {cobs("CORR-003", 70, rejected, reject)}, "f2": baseObs}, digests)
		c, err := Compare(base, cur, fixtures)
		if err != nil {
			t.Fatal(err)
		}
		d := metric(t, c, "candidate.correctness.recall")
		if d.Verdict != VerdictRegressed || *d.Baseline != 4.0/6 || *d.Current != 2.0/6 {
			t.Errorf("recall %+v", d)
		}
		if dr := metric(t, c, "pruv.decoy_rejection_rate"); dr.Verdict != VerdictUnchanged || *dr.Current != 1 {
			t.Errorf("decoy rejection %+v", dr)
		}
		if ar := metric(t, c, "candidate.architecture.recall"); ar.Verdict != VerdictNA {
			t.Errorf("architecture recall (no architecture defects) %+v", ar)
		}
		if c.Verdict != VerdictRegressed || len(c.Fixtures) != 2 || len(c.Notes) != 0 {
			t.Errorf("verdict %s fixtures %v notes %q", c.Verdict, c.Fixtures, c.Notes)
		}
	})

	t.Run("improvement, no regression", func(t *testing.T) {
		base := results("claude-code", map[string][]Observation{"f1": {cobs("CORR-003", 70, rejected, reject)}}, digests)
		cur := results("claude-code", map[string][]Observation{"f1": baseObs}, digests)
		c, err := Compare(base, cur, fixtures)
		if err != nil {
			t.Fatal(err)
		}
		if c.Verdict != VerdictImproved || metric(t, c, "final.correctness.recall").Verdict != VerdictImproved {
			t.Errorf("verdict %s, metrics %+v", c.Verdict, c.Metrics)
		}
	})

	t.Run("identical results", func(t *testing.T) {
		base := results("claude-code", map[string][]Observation{"f1": baseObs}, digests)
		c, err := Compare(base, base, fixtures)
		if err != nil || c.Verdict != VerdictNoRegression {
			t.Errorf("verdict %v err %v", c, err)
		}
	})

	t.Run("changed source digest excludes the fixture", func(t *testing.T) {
		base := results("claude-code", map[string][]Observation{"f1": baseObs, "f2": baseObs}, map[string]string{"f1": "old", "f2": "s2"})
		cur := results("kiro", map[string][]Observation{"f1": nil, "f2": baseObs}, digests)
		c, err := Compare(base, cur, fixtures)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(c.Fixtures) != "[f2]" || c.Verdict != VerdictNoRegression {
			t.Errorf("fixtures %v verdict %s", c.Fixtures, c.Verdict)
		}
		notes := strings.Join(c.Notes, "\n")
		if !strings.Contains(notes, "baseline: fixture f1: source changed (source_digest differs from the current fixture); left out") || !strings.Contains(notes, "not like-for-like") {
			t.Errorf("notes %q", c.Notes)
		}
	})

	t.Run("no comparable fixture", func(t *testing.T) {
		base := results("claude-code", map[string][]Observation{"f1": baseObs}, map[string]string{"f1": "old"})
		cur := results("claude-code", map[string][]Observation{"f1": baseObs}, digests)
		if _, err := Compare(base, cur, fixtures); err == nil {
			t.Error("want an error")
		}
	})

	t.Run("new reviewer", func(t *testing.T) {
		base := results("claude-code", map[string][]Observation{"f1": baseObs}, digests)
		base.Config.Reviewers = []string{"correctness"}
		cur := results("claude-code", map[string][]Observation{"f1": baseObs}, digests)
		c, err := Compare(base, cur, fixtures)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range []string{"candidate.architecture.precision", "candidate.architecture.recall", "final.architecture.precision", "final.architecture.recall"} {
			if d := metric(t, c, m); d.Verdict != VerdictNew || d.Delta != nil {
				t.Errorf("%s %+v", m, d)
			}
		}
		if d := metric(t, c, "candidate.correctness.recall"); d.Verdict != VerdictUnchanged {
			t.Errorf("correctness recall %+v", d)
		}
	})

	t.Run("a known entry rescores both sides", func(t *testing.T) {
		// CORR-004 at line 90 is unkeyed until the current key records it.
		withExtra := append(append([]Observation{}, baseObs...), cobs("CORR-004", 90, confirmed, accept))
		base := results("claude-code", map[string][]Observation{"f1": withExtra}, digests)
		cur := results("claude-code", map[string][]Observation{"f1": withExtra}, digests)
		before, err := Compare(base, cur, fixtures)
		if err != nil {
			t.Fatal(err)
		}
		known := compareKey("f1")
		known.Entries = append(known.Entries, Entry{ID: "K1", Kind: KindKnown, Category: "correctness", Title: "k",
			Locations: []Location{{Path: "a.go", StartLine: 90, EndLine: 90}}, Anchor: "x", Notes: "real"})
		after, err := Compare(base, cur, []Fixture{{Name: "f1", Key: known, SourceDigest: "s1"}})
		if err != nil {
			t.Fatal(err)
		}
		b, a := metric(t, before, "candidate.correctness.precision"), metric(t, after, "candidate.correctness.precision")
		if *b.Baseline != 2.0/4 || *b.Current != 2.0/4 || *a.Baseline != 2.0/3 || *a.Current != 2.0/3 || a.Verdict != VerdictUnchanged {
			t.Errorf("before %+v after %+v", b, a)
		}
	})
}

func TestCompareIncompleteRuns(t *testing.T) {
	fixtures := []Fixture{{Name: "f1", Key: compareKey("f1"), SourceDigest: "s1"}}
	digests := map[string]string{"f1": "s1"}
	decoy := []Observation{cobs("CORR-001", 70, rejected, reject)}
	base := results("claude-code", map[string][]Observation{"f1": decoy}, digests)
	// Pruv failed entirely: the same finding, never validated, in a partial
	// run. Every guarded rate is unchanged or n/a, so without the check the
	// verdict would be no_regression.
	cur := results("claude-code", map[string][]Observation{"f1": {cobs("CORR-001", 70, contract.StateUnvalidated, "")}}, digests)
	cur.Fixtures[0].Runs[0].Complete, cur.Fixtures[0].Runs[0].Status = false, "partial"
	c, err := Compare(base, cur, fixtures)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range c.Metrics {
		if d.Verdict == VerdictRegressed || d.Verdict == VerdictImproved {
			t.Errorf("metric %+v", d)
		}
	}
	if c.Verdict != VerdictIncomplete || !slices.Contains(c.Notes, "current: 1 of 1 runs incomplete; baseline: 0 of 1") {
		t.Errorf("verdict %s notes %q", c.Verdict, c.Notes)
	}
	// An incomplete baseline is flagged the same way, even when the current
	// run would otherwise show a regression.
	base.Fixtures[0].Runs[0].Complete = false
	cur = results("claude-code", map[string][]Observation{"f1": {cobs("CORR-001", 70, confirmed, accept)}}, digests)
	if c, err = Compare(base, cur, fixtures); err != nil || c.Verdict != VerdictIncomplete || !slices.Contains(c.Notes, "current: 0 of 1 runs incomplete; baseline: 1 of 1") {
		t.Errorf("verdict %v notes %v err %v", c.Verdict, c.Notes, err)
	}
	var b strings.Builder
	c.summary(&b)
	if !strings.Contains(b.String(), ": incomplete") || !strings.Contains(b.String(), "baseline: 1 of 1") {
		t.Errorf("summary:\n%s", b.String())
	}
}
