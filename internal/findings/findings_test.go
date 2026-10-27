package findings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
)

const sample = `package stats

// Max returns the largest value.
func Max(xs []float64) float64 {
	m := xs[0]
	for i := 1; i < len(xs)-1; i++ {
		if xs[i] > m {
			m = xs[i]
		}
	}
	return m
}
`

func snapshot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stats.go"), []byte(sample), 0o444); err != nil {
		t.Fatal(err)
	}
	return dir
}

func src(path string, start, end int, excerpt string) contract.Evidence {
	return contract.Evidence{Kind: contract.EvidenceSource, Description: "d", Path: path, StartLine: start, EndLine: end, Excerpt: excerpt}
}

func TestCheck(t *testing.T) {
	dir := snapshot(t)
	cases := []struct {
		name string
		ev   contract.Evidence
		want contract.CheckStatus
	}{
		{"exact line", src("stats.go", 6, 6, "for i := 1; i < len(xs)-1; i++ {"), contract.CheckVerified},
		{"indentation differs", src("stats.go", 5, 6, "m := xs[0]\n  for i := 1; i < len(xs)-1; i++ {"), contract.CheckVerified},
		{"partial first and last lines", src("stats.go", 4, 5, "Max(xs []float64) float64 {\n\tm :="), contract.CheckVerified},
		{"blank lines ignored", src("stats.go", 5, 6, "m := xs[0]\n\n\tfor i := 1; i < len(xs)-1; i++ {"), contract.CheckVerified},
		{"wrong lines but present", src("stats.go", 1, 2, "for i := 1; i < len(xs)-1; i++ {"), contract.CheckRelocated},
		{"short excerpt not relocated", src("stats.go", 1, 2, "return m"), contract.CheckMismatch},
		{"fabricated code", src("stats.go", 6, 6, "for i := 0; i < len(xs); i++ {"), contract.CheckMismatch},
		{"paraphrase", src("stats.go", 5, 11, "loops over xs skipping the last element"), contract.CheckMismatch},
		{"inner line differs", src("stats.go", 5, 7, "m := xs[0]\nfor i := 1; i < n; i++ {\nif xs[i] > m {"), contract.CheckMismatch},
		{"missing file", src("nope.go", 1, 1, "x"), contract.CheckMissingFile},
		{"escaping path", src("../stats.go", 1, 1, "x"), contract.CheckMissingFile},
		{"range past end", src("stats.go", 40, 41, "return m"), contract.CheckInvalidRange},
		{"execution", contract.Evidence{Kind: contract.EvidenceExecution, Description: "d", Command: "go test", Result: "FAIL"}, contract.CheckNotVerifiable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Check(dir, c.ev)
			if got.Status != c.want {
				t.Fatalf("status = %s (%s), want %s", got.Status, got.Detail, c.want)
			}
		})
	}
	reloc := Check(dir, src("stats.go", 1, 2, "for i := 1; i < len(xs)-1; i++ {"))
	if reloc.ActualStartLine != 6 || reloc.ActualEndLine != 6 {
		t.Errorf("relocated to %d-%d, want 6-6", reloc.ActualStartLine, reloc.ActualEndLine)
	}
}

func TestAssign(t *testing.T) {
	dir := snapshot(t)
	d := contract.FindingDraft{Title: "t",
		Locations: []contract.Location{{Path: "stats.go", StartLine: 4, EndLine: 12}, {Path: "x.go", StartLine: 1, EndLine: 1}},
		Evidence:  []contract.Evidence{src("stats.go", 5, 5, "m := xs[0]"), src("x.go", 1, 1, "y")}}
	got := Assign([]contract.FindingDraft{d, d}, contract.CategoryCorrectness, "correctness", "rev", dir)
	if got[0].ID != "CORR-001" || got[1].ID != "CORR-002" {
		t.Fatalf("ids = %s, %s", got[0].ID, got[1].ID)
	}
	f := got[0]
	if f.State != contract.StateCandidate || f.SourceRevision != "rev" || f.ReportedBy != "correctness" {
		t.Fatalf("unexpected finding %+v", f)
	}
	if len(f.EvidenceChecks) != 2 || f.EvidenceChecks[0].Status != contract.CheckVerified ||
		f.EvidenceChecks[1].Status != contract.CheckMissingFile || f.EvidenceChecks[1].Index != 1 {
		t.Fatalf("checks = %+v", f.EvidenceChecks)
	}
	if len(f.LocationChecks) != 2 || f.LocationChecks[0].Status != contract.CheckVerified ||
		f.LocationChecks[1].Status != contract.CheckMissingFile || f.LocationChecks[1].Index != 1 {
		t.Fatalf("location checks = %+v", f.LocationChecks)
	}
}

func TestCheckLocation(t *testing.T) {
	dir := snapshot(t)
	if err := os.Mkdir(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		loc  contract.Location
		want contract.CheckStatus
	}{
		{"whole file", contract.Location{Path: "stats.go", StartLine: 1, EndLine: 12}, contract.CheckVerified},
		{"single line", contract.Location{Path: "stats.go", StartLine: 6, EndLine: 6}, contract.CheckVerified},
		{"end past last line", contract.Location{Path: "stats.go", StartLine: 10, EndLine: 13}, contract.CheckInvalidRange},
		{"start past last line", contract.Location{Path: "stats.go", StartLine: 40, EndLine: 41}, contract.CheckInvalidRange},
		{"reversed range", contract.Location{Path: "stats.go", StartLine: 6, EndLine: 5}, contract.CheckInvalidRange},
		{"zero start", contract.Location{Path: "stats.go", StartLine: 0, EndLine: 1}, contract.CheckInvalidRange},
		{"missing file", contract.Location{Path: "nope.go", StartLine: 1, EndLine: 1}, contract.CheckMissingFile},
		{"directory", contract.Location{Path: "pkg", StartLine: 1, EndLine: 1}, contract.CheckMissingFile},
		{"escaping path", contract.Location{Path: "../stats.go", StartLine: 1, EndLine: 1}, contract.CheckMissingFile},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CheckLocation(dir, c.loc)
			if got.Status != c.want || got.Detail == "" {
				t.Fatalf("status = %s (%s), want %s", got.Status, got.Detail, c.want)
			}
		})
	}
}

func TestGate(t *testing.T) {
	ok := []contract.EvidenceCheck{{Status: contract.CheckVerified}}
	bad := []contract.EvidenceCheck{{Status: contract.CheckVerified}, {Status: contract.CheckMismatch}}
	none := []contract.EvidenceCheck{{Status: contract.CheckNotVerifiable}}
	accept := func(c contract.Classification) *contract.ValidationDecision {
		return &contract.ValidationDecision{Verdict: contract.VerdictAccept, Classification: c}
	}
	cases := []struct {
		name   string
		checks []contract.EvidenceCheck
		dec    *contract.ValidationDecision
		extra  []contract.EvidenceCheck
		want   contract.ValidationState
		locs   []contract.EvidenceCheck
	}{
		{"validator failed", ok, nil, nil, contract.StateUnvalidated, nil},
		{"accepted defect", ok, accept(contract.ClassDefect), nil, contract.StateConfirmed, nil},
		{"accepted risk", ok, accept(contract.ClassRisk), nil, contract.StateSupportedRisk, nil},
		{"rejected", ok, &contract.ValidationDecision{Verdict: contract.VerdictReject}, nil, contract.StateRejected, nil},
		{"investigate", ok, &contract.ValidationDecision{Verdict: contract.VerdictInvestigate}, nil, contract.StateNeedsInvestigation, nil},
		{"accepted despite fabricated evidence", bad, accept(contract.ClassDefect), nil, contract.StateNeedsInvestigation, nil},
		{"accepted with only unverifiable evidence", none, accept(contract.ClassDefect), nil, contract.StateNeedsInvestigation, nil},
		{"accepted citing fabricated extra evidence", ok, accept(contract.ClassDefect), []contract.EvidenceCheck{{Status: contract.CheckMissingFile}}, contract.StateNeedsInvestigation, nil},
		{"rejected with fabricated evidence", bad, &contract.ValidationDecision{Verdict: contract.VerdictReject}, nil, contract.StateRejected, nil},
		{"relocated evidence still supports", []contract.EvidenceCheck{{Status: contract.CheckRelocated}}, accept(contract.ClassDefect), nil, contract.StateConfirmed, nil},
		{"accepted with verified locations", ok, accept(contract.ClassDefect), nil, contract.StateConfirmed, ok},
		{"accepted with a missing location", ok, accept(contract.ClassDefect), nil, contract.StateNeedsInvestigation, []contract.EvidenceCheck{{Status: contract.CheckVerified}, {Status: contract.CheckMissingFile}}},
		{"accepted risk with an out-of-range location", ok, accept(contract.ClassRisk), nil, contract.StateNeedsInvestigation, []contract.EvidenceCheck{{Status: contract.CheckInvalidRange}}},
		{"rejected with a missing location", ok, &contract.ValidationDecision{Verdict: contract.VerdictReject}, nil, contract.StateRejected, []contract.EvidenceCheck{{Status: contract.CheckMissingFile}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := Gate(c.checks, c.locs, c.dec, c.extra)
			if got != c.want || reason == "" {
				t.Fatalf("Gate = %s (%q), want %s", got, reason, c.want)
			}
		})
	}
}

func TestTransition(t *testing.T) {
	if err := Transition(contract.StateCandidate, contract.StateConfirmed); err != nil {
		t.Fatal(err)
	}
	if Transition(contract.StateConfirmed, contract.StateRejected) == nil {
		t.Fatal("final states must be terminal")
	}
	if Transition(contract.StateCandidate, contract.StateCandidate) == nil {
		t.Fatal("candidate is not a target")
	}
	if Transition(contract.StateCandidate, "approved") == nil {
		t.Fatal("unknown state accepted")
	}
}

func TestMerge(t *testing.T) {
	cands := []contract.Finding{
		{ID: "A-1", State: contract.StateCandidate, EvidenceChecks: []contract.EvidenceCheck{{Status: contract.CheckVerified}}, LocationChecks: []contract.EvidenceCheck{{Status: contract.CheckVerified}}},
		{ID: "A-2", State: contract.StateCandidate},
	}
	got, err := Merge(cands, []contract.DecisionRecord{{FindingID: "A-1", FinalState: contract.StateRejected, GateReason: "nope"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].State != contract.StateRejected || got[0].StateReason != "nope" {
		t.Errorf("A-1 = %s %q", got[0].State, got[0].StateReason)
	}
	if got[1].State != contract.StateUnvalidated || !strings.Contains(got[1].StateReason, "no validation decision") {
		t.Errorf("A-2 = %s %q", got[1].State, got[1].StateReason)
	}
	if cands[0].State != contract.StateCandidate {
		t.Error("Merge must not modify candidates")
	}
	got[0].EvidenceChecks[0].Status = contract.CheckMismatch
	if cands[0].EvidenceChecks[0].Status != contract.CheckVerified {
		t.Error("Merge must not alias candidate evidence checks")
	}
	got[0].LocationChecks[0].Status = contract.CheckMissingFile
	if cands[0].LocationChecks[0].Status != contract.CheckVerified {
		t.Error("Merge must not alias candidate location checks")
	}
	if _, err := Merge([]contract.Finding{{ID: "B", State: contract.StateConfirmed}}, nil); err == nil {
		t.Error("merging a non-candidate must fail")
	}
}
