package bench

import (
	"encoding/json"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/report"
)

func item(id string, state contract.ValidationState, verdict contract.Verdict) report.Item {
	f := contract.Finding{ID: id, Category: contract.CategoryCorrectness, ReportedBy: "correctness", State: state}
	f.Title = "t-" + id
	f.Locations = []contract.Location{{Path: "a.go", StartLine: 3, EndLine: 4}}
	it := report.Item{Finding: f}
	if verdict != "" {
		it.Record = &contract.DecisionRecord{FindingID: id, Decision: &contract.ValidationDecision{Verdict: verdict}, FinalState: state}
	} else if state == contract.StateUnvalidated {
		// Validation failed: a record without a decision.
		it.Record = &contract.DecisionRecord{FindingID: id, FinalState: state}
	}
	return it
}

func TestFromReport(t *testing.T) {
	d := &report.Data{Items: []report.Item{
		item("CORR-001", contract.StateConfirmed, contract.VerdictAccept),
		item("CORR-002", contract.StateSupportedRisk, contract.VerdictAccept),
		item("CORR-003", contract.StateRejected, contract.VerdictReject),
		item("CORR-004", contract.StateNeedsInvestigation, contract.VerdictInvestigate),
		item("CORR-005", contract.StateNeedsInvestigation, contract.VerdictAccept),
		item("CORR-006", contract.StateUnvalidated, ""),
		item("CORR-007", contract.StateCandidate, ""),
	}}
	got := FromReport(d)
	want := []struct {
		verdict  contract.Verdict
		accepted bool
	}{
		{contract.VerdictAccept, true},
		{contract.VerdictAccept, true},
		{contract.VerdictReject, false},
		{contract.VerdictInvestigate, false},
		{contract.VerdictAccept, false}, // gate demoted
		{"", false},
		{"", false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d observations", len(got))
	}
	for i, w := range want {
		o := got[i]
		if o.Verdict != w.verdict || o.Accepted() != w.accepted {
			t.Errorf("%s: verdict %q accepted %v, want %q %v", o.ID, o.Verdict, o.Accepted(), w.verdict, w.accepted)
		}
		if o.State != d.Items[i].State || o.ReportedBy != "correctness" || o.Category != contract.CategoryCorrectness || o.Title != "t-"+o.ID {
			t.Errorf("%s: fields not copied: %+v", o.ID, o)
		}
		if len(o.Locations) != 1 || o.Locations[0] != (Location{Path: "a.go", StartLine: 3, EndLine: 4}) {
			t.Errorf("%s: locations %+v", o.ID, o.Locations)
		}
	}
	if FromReport(nil) != nil {
		t.Error("nil report should give no observations")
	}
}

func TestObservationJSONRoundTrip(t *testing.T) {
	in := FromReport(&report.Data{Items: []report.Item{item("CORR-005", contract.StateNeedsInvestigation, contract.VerdictAccept)}})
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out []Observation
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != contract.VerdictAccept || out[0].State != contract.StateNeedsInvestigation || out[0].Locations[0].StartLine != 3 {
		t.Fatalf("round trip lost data: %s", b)
	}
}
