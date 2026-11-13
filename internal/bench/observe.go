package bench

import (
	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/report"
)

// Observation is the part of one candidate finding that scoring needs. It
// is stored raw in benchmark results so a comparison can rescore both
// sides with the current keys and rules.
type Observation struct {
	ID         string                   `json:"id"`
	ReportedBy string                   `json:"reported_by"`
	Category   contract.Category        `json:"category"`
	Title      string                   `json:"title"`
	Locations  []Location               `json:"locations"`
	State      contract.ValidationState `json:"state"`
	// Verdict is Pruv's verdict, or "" when there was no decision. An
	// accept verdict on a finding that is not Accepted was demoted by the
	// gate (Sift's evidence or location checks failed).
	Verdict contract.Verdict `json:"verdict,omitempty"`
}

// Accepted reports whether the finding survived validation.
func (o Observation) Accepted() bool {
	return o.State == contract.StateConfirmed || o.State == contract.StateSupportedRisk
}

// FromReport extracts one observation per candidate finding of a loaded
// run.
func FromReport(d *report.Data) []Observation {
	if d == nil {
		return nil
	}
	out := make([]Observation, 0, len(d.Items))
	for _, it := range d.Items {
		o := Observation{
			ID: it.ID, ReportedBy: it.ReportedBy, Category: it.Category,
			Title: it.Title, State: it.State,
		}
		for _, l := range it.Locations {
			o.Locations = append(o.Locations, Location(l))
		}
		if r := it.Record; r != nil && r.Decision != nil {
			o.Verdict = r.Decision.Verdict
		}
		out = append(out, o)
	}
	return out
}
