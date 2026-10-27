package testutil

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/harness"
	"github.com/olddognewflex/sift/internal/harness/fake"
)

// StatsSource is the fixture repository's only source file.
const StatsSource = `package stats

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

// StatsRepo creates the fixture repository.
func StatsRepo(t *testing.T) string {
	return GitRepo(t, map[string]string{"go.mod": "module example.com/stats\n", "stats.go": StatsSource})
}

func Discovery() contract.DiscoveryReport {
	return contract.DiscoveryReport{
		Summary: "A tiny statistics library.", Purpose: "Compute statistics.",
		Languages: []string{"Go"}, Frameworks: []string{},
		EntryPoints:  []contract.EntryPoint{{Path: "stats.go", Description: "library API"}},
		Modules:      []contract.Module{{Path: ".", Name: "stats", Responsibility: "statistics"}},
		Dependencies: []contract.Dependency{}, BuildConventions: []string{"go build"}, TestConventions: []string{},
		CriticalPaths: []string{"stats.go Max"}, Unknowns: []string{"no tests"}, Completeness: contract.CompletenessComplete,
	}
}

func draft(title, excerpt string, line int) contract.FindingDraft {
	return contract.FindingDraft{
		Title: title, Claim: title + " claim", TriggeringConditions: "any input", ExpectedImpact: "wrong result",
		Severity: contract.SeverityHigh, Confidence: contract.ConfidenceHigh,
		Remediation: "fix it", VerificationCriteria: "a test passes",
		Locations: []contract.Location{{Path: "stats.go", StartLine: line, EndLine: line}},
		Evidence: []contract.Evidence{{Kind: contract.EvidenceSource, Description: "the code",
			Path: "stats.go", StartLine: line, EndLine: line, Excerpt: excerpt}},
	}
}

// ArchitectureReview has one well-evidenced finding (ARCH-001).
func ArchitectureReview() contract.ReviewReport {
	return contract.ReviewReport{
		Findings:        []contract.FindingDraft{draft("Max has no error contract", "func Max(xs []float64) float64 {", 4)},
		Strengths:       []contract.Strength{{Title: "Small API", Description: "One function.", Locations: []contract.Location{{Path: "stats.go", StartLine: 4, EndLine: 4}}}},
		ReviewedScope:   []string{"stats.go"},
		UnreviewedScope: []string{}, Limitations: []string{},
	}
}

// CorrectnessReview has a real defect (CORR-001), a finding citing code that
// does not exist (CORR-002), and a weak claim (CORR-003).
func CorrectnessReview() contract.ReviewReport {
	return contract.ReviewReport{
		Findings: []contract.FindingDraft{
			draft("Max skips the last element", "for i := 1; i < len(xs)-1; i++ {", 6),
			draft("Max divides by zero", "return m / float64(len(xs))", 11),
			draft("Max is slow", "if xs[i] > m {", 7),
		},
		Strengths: []contract.Strength{}, ReviewedScope: []string{"stats.go"}, UnreviewedScope: []string{"tests"}, Limitations: []string{},
	}
}

func decision(v contract.Verdict, c contract.Classification) contract.ValidationDecision {
	return contract.ValidationDecision{
		Verdict: v, Classification: c, EvidenceAssessment: "checked", ContradictoryEvidence: "searched callers; none",
		TriggerAssessment: "reachable", ImpactAssessment: "real", RemediationAssessment: "proportionate", Rationale: "because",
	}
}

// Decisions maps finding IDs to Pruv's scripted verdicts. CORR-002 is
// accepted even though its evidence is fabricated; Sift's gate must catch it.
var Decisions = map[string]contract.ValidationDecision{
	"ARCH-001": decision(contract.VerdictAccept, contract.ClassRisk),
	"CORR-001": decision(contract.VerdictAccept, contract.ClassDefect),
	"CORR-002": decision(contract.VerdictAccept, contract.ClassDefect),
	"CORR-003": decision(contract.VerdictReject, ""),
}

// FindingID extracts the finding a Pruv prompt is about.
func FindingID(req harness.Request) string {
	_, rest, _ := strings.Cut(req.Prompt, "validate finding ")
	id, _, _ := strings.Cut(rest, "\n")
	return id
}

// Pruv answers with Decisions.
func Pruv(req harness.Request, _ int) (json.RawMessage, error) {
	d, ok := Decisions[FindingID(req)]
	if !ok {
		return nil, &harness.Error{Kind: harness.ErrProcess, Msg: "unexpected finding " + FindingID(req)}
	}
	return json.Marshal(d)
}

// Harness returns a fake harness scripted for the fixture repository.
func Harness() *fake.Harness {
	return &fake.Harness{Responders: map[string]fake.Responder{
		"discovery":    fake.JSON(Discovery()),
		"architecture": fake.JSON(ArchitectureReview()),
		"correctness":  fake.JSON(CorrectnessReview()),
		"pruv":         Pruv,
	}}
}
