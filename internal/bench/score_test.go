package bench

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
)

func scoreKey() Key {
	return Key{Version: 1, Fixture: "s", Entries: []Entry{
		entry("D1", KindDefect, contract.CategoryCorrectness, loc("a.go", 10, 14)),
		entry("D2", KindDefect, contract.CategoryCorrectness, loc("b.go", 5, 5)),
		entry("D3", KindDefect, contract.CategoryArchitecture, loc("c.go", 10, 10)),
		entry("X1", KindDecoy, contract.CategoryCorrectness, loc("a.go", 30, 32)),
		entry("X2", KindDecoy, contract.CategoryArchitecture, loc("c.go", 30, 30)),
	}}
}

// decided sets an observation's validation outcome. Accept with
// needs_investigation is a gate demotion.
func decided(o Observation, v contract.Verdict, s contract.ValidationState) Observation {
	o.Verdict, o.State = v, s
	return o
}

func wantRate(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: got nil, want %.4f", name, want)
	} else if math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s: got %.4f, want %.4f", name, *got, want)
	}
}

func wantNil(t *testing.T, name string, got *float64) {
	t.Helper()
	if got != nil {
		t.Errorf("%s: got %.4f, want nil", name, *got)
	}
}

func wantInt(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %d, want %d", name, got, want)
	}
}

func single(t *testing.T, k Key, runs ...RunObs) Scores {
	t.Helper()
	s, err := Score([]FixtureRuns{{Key: k, Runs: runs}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestScoreWorkedExample(t *testing.T) {
	run := RunObs{Trial: 1, Complete: true, Observations: []Observation{
		decided(obs("CORR-001", loc("a.go", 11, 11)), contract.VerdictAccept, contract.StateConfirmed),
		decided(obs("CORR-002", loc("a.go", 31, 31)), contract.VerdictReject, contract.StateRejected),
		decided(obs("CORR-003", loc("a.go", 50, 50)), contract.VerdictAccept, contract.StateSupportedRisk),
		decided(obs("CORR-004", loc("a.go", 12, 12)), contract.VerdictReject, contract.StateRejected),
		decided(obs("ARCH-001", loc("c.go", 30, 30)), contract.VerdictAccept, contract.StateNeedsInvestigation),
	}}
	s := single(t, scoreKey(), run)
	a := s.Aggregate

	corr := a.Candidate.Reviewers["correctness"]
	wantInt(t, "corr findings", corr.Findings, 4)
	wantInt(t, "corr tp", corr.TP, 1)
	wantInt(t, "corr decoys", corr.DecoysFlagged, 1)
	wantInt(t, "corr unkeyed", corr.Unkeyed, 1)
	wantInt(t, "corr duplicates", corr.Duplicates, 1)
	wantRate(t, "corr precision", corr.Precision, 1.0/3)
	wantRate(t, "corr recall", corr.Recall, 1.0/2)

	arch := a.Candidate.Reviewers["architecture"]
	wantInt(t, "arch tp", arch.TP, 0)
	wantInt(t, "arch fp", arch.DecoysFlagged+arch.Unkeyed, 1)
	wantRate(t, "arch precision", arch.Precision, 0)
	wantRate(t, "arch recall", arch.Recall, 0)

	wantRate(t, "union recall", a.Candidate.Overall.Recall, 1.0/3)

	wantRate(t, "final corr precision", a.Final.Reviewers["correctness"].Precision, 1.0/2)
	wantNil(t, "final arch precision", a.Final.Reviewers["architecture"].Precision)

	p := a.Pruv
	wantRate(t, "pruv precision", p.Precision, 1.0/2)
	wantRate(t, "pruv recall", p.Recall, 1.0/3)
	wantRate(t, "pruv retention", p.Retention, 1)
	wantInt(t, "defects found", p.DefectsFound, 1)
	wantInt(t, "defects accepted", p.DefectsAccepted, 1)
	wantInt(t, "decoys flagged", p.DecoysFlagged, 2)
	wantInt(t, "decoys rejected", p.DecoysRejected, 2)
	wantInt(t, "rejected by pruv_reject", p.DecoysRejectedBy.PruvReject, 1)
	wantInt(t, "rejected by gate", p.DecoysRejectedBy.Gate, 1)
	wantRate(t, "decoy rejection rate", p.DecoyRejectionRate, 1)
	wantInt(t, "unvalidated", p.Unvalidated, 0)
	wantInt(t, "decoys not flagged", p.DecoysNotFlagged, 0)

	if s.RulesVersion != RulesVersion || s.Trials != 1 || len(s.TrialRange) != 0 {
		t.Errorf("header: %+v", s)
	}
	if f, ok := s.Fixtures["s"]; !ok || f.Pruv.DecoysRejected != 2 || f.Runs != 1 {
		t.Errorf("per-fixture score missing or wrong: %+v", s.Fixtures)
	}
}

func TestScoreDefectsLost(t *testing.T) {
	run := RunObs{Trial: 1, Complete: true, Observations: []Observation{
		decided(obs("CORR-001", loc("a.go", 11, 11)), contract.VerdictReject, contract.StateRejected),
		decided(obs("CORR-002", loc("b.go", 5, 5)), contract.VerdictAccept, contract.StateNeedsInvestigation),
		// D3: one rejection and one unvalidated: not attributable to Pruv.
		decided(obs("ARCH-001", loc("c.go", 10, 10)), contract.VerdictReject, contract.StateRejected),
		decided(obs("ARCH-002", loc("c.go", 11, 11)), "", contract.StateUnvalidated),
	}}
	p := single(t, scoreKey(), run).Aggregate.Pruv
	wantInt(t, "found", p.DefectsFound, 3)
	wantInt(t, "accepted", p.DefectsAccepted, 0)
	wantRate(t, "retention", p.Retention, 0)
	wantInt(t, "lost pruv_reject", p.DefectsLostBy.PruvReject, 1)
	wantInt(t, "lost gate", p.DefectsLostBy.Gate, 1)
	wantInt(t, "lost unvalidated", p.DefectsLostBy.Unvalidated, 1)
	wantInt(t, "unvalidated", p.Unvalidated, 1)
	wantInt(t, "decoys not flagged", p.DecoysNotFlagged, 2)
	wantNil(t, "decoy rejection rate", p.DecoyRejectionRate)
}

func TestScoreZeroDenominatorsAreNil(t *testing.T) {
	s := single(t, Key{Version: 1, Fixture: "empty"}, RunObs{Trial: 1, Complete: true})
	a := s.Aggregate
	for name, r := range map[string]*float64{
		"cand corr precision": a.Candidate.Reviewers["correctness"].Precision,
		"cand corr recall":    a.Candidate.Reviewers["correctness"].Recall,
		"union recall":        a.Candidate.Overall.Recall,
		"final precision":     a.Final.Overall.Precision,
		"pruv precision":      a.Pruv.Precision,
		"pruv recall":         a.Pruv.Recall,
		"retention":           a.Pruv.Retention,
		"decoy rejection":     a.Pruv.DecoyRejectionRate,
	} {
		wantNil(t, name, r)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"precision":null`) {
		t.Errorf("nil rates should encode as null: %s", b)
	}
	if strings.Count(string(b), `"unvalidated":0`) < 2 {
		t.Errorf("loss breakdowns should always include unvalidated: %s", b)
	}
}

func TestScoreUnresolvedDecoyExcluded(t *testing.T) {
	run := RunObs{Trial: 1, Complete: true, Observations: []Observation{
		decided(obs("CORR-001", loc("a.go", 30, 30)), contract.VerdictReject, contract.StateRejected),
		decided(obs("CORR-002", loc("a.go", 31, 31)), "", contract.StateUnvalidated),
		decided(obs("ARCH-001", loc("c.go", 30, 30)), contract.VerdictInvestigate, contract.StateNeedsInvestigation),
	}}
	p := single(t, scoreKey(), run).Aggregate.Pruv
	wantInt(t, "flagged", p.DecoysFlagged, 2)
	wantInt(t, "unresolved", p.DecoysUnresolved, 1)
	wantInt(t, "rejected", p.DecoysRejected, 1)
	wantInt(t, "rejected by investigate", p.DecoysRejectedBy.PruvInvestigate, 1)
	wantRate(t, "rate", p.DecoyRejectionRate, 1)
	wantInt(t, "unvalidated", p.Unvalidated, 1)
}

func TestScoreAcceptedDecoy(t *testing.T) {
	run := RunObs{Trial: 1, Complete: true, Observations: []Observation{
		decided(obs("CORR-001", loc("a.go", 30, 30)), contract.VerdictAccept, contract.StateSupportedRisk),
		decided(obs("CORR-002", loc("a.go", 31, 31)), "", contract.StateUnvalidated),
	}}
	p := single(t, scoreKey(), run).Aggregate.Pruv
	wantInt(t, "accepted", p.DecoysAccepted, 1)
	wantInt(t, "unresolved", p.DecoysUnresolved, 0)
	wantRate(t, "rate", p.DecoyRejectionRate, 0)
}

func TestScoreKnownIsNeutral(t *testing.T) {
	k := scoreKey()
	k.Entries[3].Kind = KindKnown // X1 becomes a known issue
	only := RunObs{Trial: 1, Complete: true, Observations: []Observation{
		decided(obs("CORR-001", loc("a.go", 31, 31)), contract.VerdictAccept, contract.StateConfirmed),
	}}
	c := single(t, k, only).Aggregate.Candidate.Reviewers["correctness"]
	wantInt(t, "known", c.Known, 1)
	wantNil(t, "precision with only a known match", c.Precision)

	only.Observations = append(only.Observations, decided(obs("CORR-002", loc("a.go", 10, 10)), contract.VerdictAccept, contract.StateConfirmed))
	a := single(t, k, only).Aggregate
	wantRate(t, "precision", a.Candidate.Reviewers["correctness"].Precision, 1)
	wantRate(t, "final precision", a.Final.Overall.Precision, 1)
	wantInt(t, "decoys not flagged", a.Pruv.DecoysNotFlagged, 1)
}

func TestScoreSumsTrialsAndRanges(t *testing.T) {
	t1 := RunObs{Trial: 1, Complete: true, Observations: []Observation{
		decided(obs("CORR-001", loc("a.go", 11, 11)), contract.VerdictAccept, contract.StateConfirmed),
	}}
	t2 := RunObs{Trial: 2, Complete: true, Observations: []Observation{
		decided(obs("CORR-001", loc("b.go", 5, 5)), contract.VerdictAccept, contract.StateConfirmed),
		decided(obs("CORR-002", loc("z.go", 1, 1)), contract.VerdictReject, contract.StateRejected),
	}}
	other := scoreKey()
	other.Fixture = "t"
	s, err := Score([]FixtureRuns{{Key: scoreKey(), Runs: []RunObs{t1, t2}}, {Key: other, Runs: []RunObs{{Trial: 1, Complete: true}, {Trial: 2, Complete: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Trials != 2 {
		t.Fatalf("trials %d", s.Trials)
	}
	c := s.Aggregate.Candidate.Reviewers["correctness"]
	wantInt(t, "runs", s.Aggregate.Runs, 4)
	wantInt(t, "tp", c.TP, 2)
	wantInt(t, "unkeyed", c.Unkeyed, 1)
	wantInt(t, "defects", c.Defects, 8)
	wantRate(t, "precision", c.Precision, 2.0/3)
	wantRate(t, "recall", c.Recall, 2.0/8)
	wantRate(t, "fixture s recall", s.Fixtures["s"].Candidate.Reviewers["correctness"].Recall, 2.0/4)
	wantRate(t, "fixture t recall", s.Fixtures["t"].Candidate.Reviewers["correctness"].Recall, 0)

	r, ok := s.TrialRange["candidate.correctness.precision"]
	if !ok || r.Min != 0.5 || r.Max != 1 {
		t.Errorf("precision range %+v", r)
	}
	r, ok = s.TrialRange["candidate.correctness.recall"]
	if !ok || r.Min != 0.25 || r.Max != 0.25 {
		t.Errorf("recall range %+v", r)
	}
	if _, ok := s.TrialRange["final.architecture.precision"]; ok {
		t.Error("a rate that is nil in every trial has no range")
	}
}

func TestScoreIncompleteRunCountsMisses(t *testing.T) {
	done := RunObs{Trial: 1, Complete: true, Observations: []Observation{
		decided(obs("CORR-001", loc("a.go", 11, 11)), contract.VerdictAccept, contract.StateConfirmed),
	}}
	failed := RunObs{Trial: 2, Complete: false}
	s := single(t, scoreKey(), done, failed)
	wantInt(t, "incomplete runs", s.Aggregate.IncompleteRuns, 1)
	wantInt(t, "fixture incomplete runs", s.Fixtures["s"].IncompleteRuns, 1)
	wantRate(t, "union recall", s.Aggregate.Candidate.Overall.Recall, 1.0/6)
	wantRate(t, "pruv recall", s.Aggregate.Pruv.Recall, 1.0/6)
}

func TestScoreOffCategoryIsPerEntry(t *testing.T) {
	arch := decided(obs("ARCH-001", loc("a.go", 11, 11)), contract.VerdictAccept, contract.StateSupportedRisk)
	corr := decided(obs("CORR-001", loc("a.go", 12, 12)), contract.VerdictAccept, contract.StateConfirmed)
	for name, in := range map[string][]Observation{"arch first": {arch, corr}, "corr first": {corr, arch}} {
		t.Run(name, func(t *testing.T) {
			a := single(t, scoreKey(), RunObs{Trial: 1, Complete: true, Observations: in}).Aggregate
			for stage, st := range map[string]Stages{"candidate": a.Candidate, "final": a.Final} {
				// D1 is correctness and CORR-001 matched it, so it is not
				// off-category overall even though ARCH-001 claims it.
				wantInt(t, stage+" overall tp", st.Overall.TP, 1)
				wantInt(t, stage+" overall duplicates", st.Overall.Duplicates, 1)
				wantInt(t, stage+" overall off-category", st.Overall.OffCategoryTP, 0)
				wantInt(t, stage+" architecture off-category", st.Reviewers["architecture"].OffCategoryTP, 1)
				wantInt(t, stage+" correctness off-category", st.Reviewers["correctness"].OffCategoryTP, 0)
			}
		})
	}
	only := single(t, scoreKey(), RunObs{Trial: 1, Complete: true, Observations: []Observation{arch}}).Aggregate
	wantInt(t, "off-category with only the other reviewer", only.Candidate.Overall.OffCategoryTP, 1)
}

func TestScoreRejectsUnevenTrials(t *testing.T) {
	other := scoreKey()
	other.Fixture = "t"
	for name, in := range map[string][]FixtureRuns{
		"missing trial": {
			{Key: scoreKey(), Runs: []RunObs{{Trial: 1}, {Trial: 2}}},
			{Key: other, Runs: []RunObs{{Trial: 1}}},
		},
		"repeated trial": {
			{Key: scoreKey(), Runs: []RunObs{{Trial: 1}, {Trial: 1}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Score(in); err == nil || !strings.Contains(err.Error(), "trial") {
				t.Fatalf("want trial error, got %v", err)
			}
		})
	}
}

func TestScoreUnattributed(t *testing.T) {
	stray := decided(obs("SEC-001", loc("a.go", 11, 11)), contract.VerdictAccept, contract.StateConfirmed)
	stray.ReportedBy, stray.Category = "security", "security"
	run := RunObs{Trial: 1, Complete: true, Observations: []Observation{
		stray,
		decided(obs("CORR-001", loc("b.go", 5, 5)), contract.VerdictReject, contract.StateRejected),
	}}
	a := single(t, scoreKey(), run).Aggregate
	wantInt(t, "unattributed", a.Unattributed, 1)
	sum := a.Unattributed
	for _, st := range a.Candidate.Reviewers {
		sum += st.Findings
	}
	wantInt(t, "reviewers + unattributed", sum, a.Candidate.Overall.Findings)
	wantInt(t, "overall tp", a.Candidate.Overall.TP, 2)
}
