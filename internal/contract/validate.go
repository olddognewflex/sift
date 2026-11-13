package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
)

// Validator is implemented by every artifact type.
type Validator interface {
	Validate() error
}

// Decode strictly decodes a single JSON value into v (unknown fields are
// rejected) and then validates it.
func Decode(data []byte, v Validator) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if dec.More() {
		return errors.New("decode: trailing data after JSON value")
	}
	if err := v.Validate(); err != nil {
		return fmt.Errorf("invalid %T: %w", v, err)
	}
	return nil
}

// problems accumulates validation errors with field paths.
type problems []string

func (p *problems) add(field, format string, args ...any) {
	*p = append(*p, field+": "+fmt.Sprintf(format, args...))
}

func (p *problems) required(field, value string) {
	if strings.TrimSpace(value) == "" {
		p.add(field, "required")
	}
}

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return errors.New(strings.Join(p, "; "))
}

func checkEnum[T enum](p *problems, field string, v T) {
	if !Valid(v) {
		p.add(field, "%q is not one of %v", string(v), v.Enum())
	}
}

func (h Header) validate(p *problems, kind string) {
	if h.Kind != kind {
		p.add("kind", "got %q, want %q", h.Kind, kind)
	}
	if h.SchemaVersion != SchemaVersion {
		p.add("schema_version", "unsupported version %d (supported: %d)", h.SchemaVersion, SchemaVersion)
	}
	p.required("run_id", h.RunID)
}

// ValidRelPath reports whether p is a clean, relative, forward-slash path
// that stays inside the repository root.
func ValidRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	c := path.Clean(p)
	return c == p && c != "." && c != ".." && !strings.HasPrefix(c, "../")
}

func (l Location) validate(p *problems, field string) {
	if !ValidRelPath(l.Path) {
		p.add(field+".path", "%q is not a clean repository-relative path", l.Path)
	}
	if l.StartLine < 1 || l.EndLine < l.StartLine {
		p.add(field, "invalid line range %d-%d", l.StartLine, l.EndLine)
	}
}

func (e Evidence) validate(p *problems, field string) {
	checkEnum(p, field+".kind", e.Kind)
	p.required(field+".description", e.Description)
	switch e.Kind {
	case EvidenceSource:
		Location{e.Path, e.StartLine, e.EndLine}.validate(p, field)
		p.required(field+".excerpt", e.Excerpt)
	case EvidenceExecution:
		p.required(field+".command", e.Command)
		p.required(field+".result", e.Result)
	}
}

func (d DiscoveryReport) Validate() error {
	var p problems
	p.required("summary", d.Summary)
	checkEnum(&p, "completeness", d.Completeness)
	for i, m := range d.Modules {
		p.required(fmt.Sprintf("modules[%d].path", i), m.Path)
	}
	return p.err()
}

func (d Discovery) Validate() error {
	var p problems
	d.Header.validate(&p, KindDiscovery)
	p.required("source_revision", d.SourceRevision)
	if err := d.Report.Validate(); err != nil {
		p.add("report", "%v", err)
	}
	return p.err()
}

func (r ReviewPlan) Validate() error {
	var p problems
	r.Header.validate(&p, KindReviewPlan)
	seen := map[string]bool{}
	for i, t := range r.Tasks {
		f := fmt.Sprintf("tasks[%d]", i)
		p.required(f+".id", t.ID)
		p.required(f+".agent", t.Agent)
		checkEnum(&p, f+".category", t.Category)
		if seen[t.ID] {
			p.add(f+".id", "duplicate %q", t.ID)
		}
		seen[t.ID] = true
	}
	if r.MaxFindingsPerRun < 1 {
		p.add("max_findings_per_reviewer", "must be positive")
	}
	return p.err()
}

func (f FindingDraft) validate(p *problems, field string) {
	p.required(field+".title", f.Title)
	p.required(field+".claim", f.Claim)
	p.required(field+".triggering_conditions", f.TriggeringConditions)
	p.required(field+".expected_impact", f.ExpectedImpact)
	p.required(field+".remediation", f.Remediation)
	p.required(field+".verification_criteria", f.VerificationCriteria)
	checkEnum(p, field+".severity", f.Severity)
	checkEnum(p, field+".confidence", f.Confidence)
	if len(f.Locations) == 0 {
		p.add(field+".locations", "at least one location is required")
	}
	for i, l := range f.Locations {
		l.validate(p, fmt.Sprintf("%s.locations[%d]", field, i))
	}
	if len(f.Evidence) == 0 {
		p.add(field+".evidence", "at least one evidence item is required")
	}
	for i, e := range f.Evidence {
		e.validate(p, fmt.Sprintf("%s.evidence[%d]", field, i))
	}
}

func (r ReviewReport) Validate() error {
	var p problems
	for i, f := range r.Findings {
		f.validate(&p, fmt.Sprintf("findings[%d]", i))
	}
	for i, s := range r.Strengths {
		p.required(fmt.Sprintf("strengths[%d].title", i), s.Title)
		for j, l := range s.Locations {
			l.validate(&p, fmt.Sprintf("strengths[%d].locations[%d]", i, j))
		}
	}
	return p.err()
}

func (r Review) Validate() error {
	var p problems
	r.Header.validate(&p, KindReview)
	p.required("task_id", r.TaskID)
	p.required("agent", r.Agent)
	checkEnum(&p, "category", r.Category)
	if err := r.Report.Validate(); err != nil {
		p.add("report", "%v", err)
	}
	return p.err()
}

func (f Finding) validate(p *problems, field string) {
	p.required(field+".id", f.ID)
	p.required(field+".source_revision", f.SourceRevision)
	p.required(field+".reported_by", f.ReportedBy)
	checkEnum(p, field+".category", f.Category)
	checkEnum(p, field+".validation_state", f.State)
	f.FindingDraft.validate(p, field)
	if len(f.EvidenceChecks) != len(f.Evidence) {
		p.add(field+".evidence_checks", "have %d checks for %d evidence items", len(f.EvidenceChecks), len(f.Evidence))
	}
	for i, c := range f.EvidenceChecks {
		checkEnum(p, fmt.Sprintf("%s.evidence_checks[%d].status", field, i), c.Status)
	}
	// Runs recorded before locations were checked have none; read them as
	// unchecked.
	if f.LocationChecks != nil && len(f.LocationChecks) != len(f.Locations) {
		p.add(field+".location_checks", "have %d checks for %d locations", len(f.LocationChecks), len(f.Locations))
	}
	for i, c := range f.LocationChecks {
		checkEnum(p, fmt.Sprintf("%s.location_checks[%d].status", field, i), c.Status)
	}
}

func (s FindingSet) Validate() error {
	var p problems
	s.Header.validate(&p, KindFindings)
	seen := map[string]bool{}
	for i, f := range s.Findings {
		f.validate(&p, fmt.Sprintf("findings[%d]", i))
		if seen[f.ID] {
			p.add(fmt.Sprintf("findings[%d].id", i), "duplicate %q", f.ID)
		}
		seen[f.ID] = true
	}
	return p.err()
}

func (d ValidationDecision) Validate() error {
	var p problems
	checkEnum(&p, "verdict", d.Verdict)
	p.required("evidence_assessment", d.EvidenceAssessment)
	p.required("contradictory_evidence", d.ContradictoryEvidence)
	p.required("trigger_assessment", d.TriggerAssessment)
	p.required("impact_assessment", d.ImpactAssessment)
	p.required("remediation_assessment", d.RemediationAssessment)
	p.required("rationale", d.Rationale)
	switch {
	case d.Verdict == VerdictAccept:
		checkEnum(&p, "classification", d.Classification)
	case d.Classification != "":
		p.add("classification", "only allowed when verdict is accept")
	}
	if d.AdjustedSeverity != "" {
		checkEnum(&p, "adjusted_severity", d.AdjustedSeverity)
	}
	if d.AdjustedConfidence != "" {
		checkEnum(&p, "adjusted_confidence", d.AdjustedConfidence)
	}
	for i, e := range d.AdditionalEvidence {
		e.validate(&p, fmt.Sprintf("additional_evidence[%d]", i))
	}
	return p.err()
}

func (s DecisionSet) Validate() error {
	var p problems
	s.Header.validate(&p, KindDecisions)
	for i, d := range s.Decisions {
		f := fmt.Sprintf("decisions[%d]", i)
		p.required(f+".finding_id", d.FindingID)
		p.required(f+".validator", d.Validator)
		p.required(f+".gate_reason", d.GateReason)
		checkEnum(&p, f+".final_state", d.FinalState)
		if d.FinalState == StateCandidate {
			p.add(f+".final_state", "candidate is not a final state")
		}
		if d.Decision == nil && d.Error == "" {
			p.add(f, "a record needs a decision or an error")
		}
		if d.Decision != nil {
			if err := d.Decision.Validate(); err != nil {
				p.add(f+".decision", "%v", err)
			}
		}
	}
	return p.err()
}

func (r Relocation) validate(p *problems, field string) {
	checkEnum(p, field+".status", r.Status)
	if r.ActualPath != "" && !ValidRelPath(r.ActualPath) {
		p.add(field+".actual_path", "%q is not a clean repository-relative path", r.ActualPath)
	}
}

func (r VerificationRecord) validate(p *problems, f string) {
	p.required(f+".finding_id", r.FindingID)
	p.required(f+".validator", r.Validator)
	p.required(f+".validator_version", r.ValidatorVersion)
	// Only established findings are verified (ADR 0006 §8).
	if r.OriginalState != StateConfirmed && r.OriginalState != StateSupportedRisk {
		p.add(f+".original_state", "%q is not confirmed or supported_risk", string(r.OriginalState))
	}
	checkEnum(p, f+".severity", r.Severity)
	checkEnum(p, f+".confidence", r.Confidence)
	checkEnum(p, f+".outcome", r.Outcome)
	p.required(f+".outcome_reason", r.OutcomeReason)
	for i, c := range r.Relocations {
		c.validate(p, fmt.Sprintf("%s.relocations[%d]", f, i))
	}
	if r.Outcome == OutcomeUnverified {
		// Unverified means no decision was produced; the error says why.
		p.required(f+".error", r.Error)
		if r.Decision != nil {
			p.add(f+".decision", "not allowed when outcome is unverified")
		}
	}
	if r.Decision == nil {
		// The only decided outcome without a validator decision is the
		// identical-snapshot short circuit, where no agent runs.
		if r.Outcome != OutcomeUnverified && r.Outcome != OutcomeStillPresent {
			p.add(f+".decision", "required when outcome is %s", r.Outcome)
		}
		if r.Outcome == OutcomeStillPresent && r.Error != "" {
			p.add(f+".error", "not allowed on a still_present outcome without a decision")
		}
		if len(r.AdditionalChecks) > 0 || len(r.AdditionalPriorMatches) > 0 {
			p.add(f, "additional evidence checks and prior matches need a decision")
		}
		return
	}
	if err := r.Decision.Validate(); err != nil {
		p.add(f+".decision", "%v", err)
	}
	n := len(r.Decision.AdditionalEvidence)
	if len(r.AdditionalChecks) != n {
		p.add(f+".additional_evidence_checks", "have %d checks for %d additional evidence items", len(r.AdditionalChecks), n)
	}
	if len(r.AdditionalPriorMatches) != n {
		p.add(f+".additional_evidence_prior_matches", "have %d counts for %d additional evidence items", len(r.AdditionalPriorMatches), n)
	}
	for i, c := range r.AdditionalChecks {
		c.validate(p, fmt.Sprintf("%s.additional_evidence_checks[%d]", f, i))
	}
	for i, m := range r.AdditionalPriorMatches {
		if m < -1 {
			p.add(fmt.Sprintf("%s.additional_evidence_prior_matches[%d]", f, i), "%d is not a match count or -1", m)
		}
	}
}

func (s VerificationSet) Validate() error {
	var p problems
	s.Header.validate(&p, KindVerification)
	seen := map[string]bool{}
	for i, r := range s.Records {
		f := fmt.Sprintf("records[%d]", i)
		r.validate(&p, f)
		if seen[r.FindingID] {
			p.add(f+".finding_id", "duplicate %q", r.FindingID)
		}
		seen[r.FindingID] = true
	}
	return p.err()
}

func (v VerifyTarget) validate(p *problems) {
	p.required("verifies.run", v.Run)
	p.required("verifies.source_revision", v.SourceRevision)
	p.required("verifies.snapshot_digest", v.SnapshotDigest)
	p.required("verifies.findings_sha256", v.FindingsSHA256)
	if len(v.Findings) == 0 {
		p.add("verifies.findings", "at least one finding is required")
	}
	for i, id := range v.Findings {
		f := fmt.Sprintf("verifies.findings[%d]", i)
		p.required(f, id)
		if i > 0 && id <= v.Findings[i-1] {
			p.add(f, "%q is not after %q; findings must be sorted and unique", id, v.Findings[i-1])
		}
	}
}

func (e ExecutionResult) Validate() error {
	var p problems
	e.Header.validate(&p, KindExecution)
	p.required("task_id", e.TaskID)
	p.required("agent", e.Agent)
	p.required("harness", e.Harness)
	if e.Attempt < 1 {
		p.add("attempt", "must be positive")
	}
	if !e.Succeeded && e.Error == "" {
		p.add("error", "required when the execution did not succeed")
	}
	return p.err()
}

func (m Manifest) Validate() error {
	var p problems
	m.Header.validate(&p, KindManifest)
	checkEnum(&p, "status", m.Status)
	p.required("repository.path", m.Repository.Path)
	p.required("repository.commit", m.Repository.Commit)
	p.required("repository.source_revision", m.Repository.SourceRevision)
	checkEnum(&p, "repository.dirty_policy", m.Repository.DirtyPolicy)
	seen := map[string]bool{}
	for i, t := range m.Tasks {
		f := fmt.Sprintf("tasks[%d]", i)
		p.required(f+".id", t.ID)
		checkEnum(&p, f+".status", t.Status)
		seen[t.ID] = true
	}
	for i, t := range m.Tasks {
		for _, d := range t.DependsOn {
			if !seen[d] {
				p.add(fmt.Sprintf("tasks[%d].depends_on", i), "unknown task %q", d)
			}
		}
	}
	if m.Verifies != nil {
		m.Verifies.validate(&p)
	}
	if m.Cost != nil {
		m.Cost.validate(&p, "cost")
	}
	return p.err()
}

func (c RunCost) validate(p *problems, field string) {
	e, a := c.Estimate, c.Actual
	switch e.Kind {
	case "audit", "verify", "total":
	default:
		p.add(field+".estimate.kind", "%q is not one of [audit verify total]", e.Kind)
	}
	p.required(field+".estimate.model", e.Model)
	nonNegative := func(name string, v float64) {
		if v < 0 {
			p.add(field+"."+name, "must not be negative")
		}
	}
	for _, n := range []struct {
		name string
		v    int
	}{
		{"estimate.files", e.Files}, {"estimate.reviewers", e.Reviewers},
		{"estimate.pruv_calls_low", e.PruvCallsLow}, {"estimate.pruv_calls_high", e.PruvCallsHigh},
		{"actual.executions", a.Executions}, {"actual.reported_executions", a.ReportedExecutions},
		{"actual.pruv_executions", a.PruvExecutions}, {"actual.candidates", a.Candidates},
	} {
		nonNegative(n.name, float64(n.v))
	}
	for _, n := range []struct {
		name string
		v    float64
	}{
		{"estimate.rates.discovery_usd", e.Rates.DiscoveryUSD}, {"estimate.rates.file_review_usd", e.Rates.FileReviewUSD},
		{"estimate.rates.pruv_call_usd", e.Rates.PruvCallUSD}, {"estimate.discovery_usd", e.DiscoveryUSD},
		{"estimate.reviewers_usd", e.ReviewersUSD}, {"estimate.pruv_low_usd", e.PruvLowUSD},
		{"estimate.pruv_high_usd", e.PruvHighUSD}, {"estimate.low_usd", e.LowUSD}, {"estimate.high_usd", e.HighUSD},
		{"budget_usd", c.BudgetUSD}, {"actual.usd", a.USD},
	} {
		nonNegative(n.name, n.v)
	}
	if e.PruvCallsLow > e.PruvCallsHigh {
		p.add(field+".estimate", "pruv_calls_low %d exceeds pruv_calls_high %d", e.PruvCallsLow, e.PruvCallsHigh)
	}
	if e.LowUSD > e.HighUSD {
		p.add(field+".estimate", "low_usd %.2f exceeds high_usd %.2f", e.LowUSD, e.HighUSD)
	}
	checkEnum(p, field+".approval", c.Approval)
	// The approval must match the budget check it records (the same rule
	// as cost.OverBudget, inlined: internal/cost imports this package).
	// unchecked fits any state.
	over := c.BudgetUSD > 0 && e.HighUSD > c.BudgetUSD
	switch c.Approval {
	case ApprovalNoBudget:
		if c.BudgetUSD != 0 {
			p.add(field+".approval", "no_budget requires budget_usd 0, got %.2f", c.BudgetUSD)
		}
	case ApprovalWithinBudget:
		if c.BudgetUSD == 0 || over {
			p.add(field+".approval", "within_budget requires a budget the high estimate does not exceed")
		}
	case ApprovalYesFlag, ApprovalPrompt:
		if !over {
			p.add(field+".approval", "%s is only recorded when the high estimate exceeds the budget", c.Approval)
		}
	}
	if a.ReportedExecutions > a.Executions || a.PruvExecutions > a.Executions {
		p.add(field+".actual", "reported_executions and pruv_executions cannot exceed executions")
	}
}

func (r ReportMeta) Validate() error {
	var p problems
	r.Header.validate(&p, KindReportMeta)
	checkEnum(&p, "run_status", r.RunStatus)
	if len(r.Files) == 0 {
		p.add("files", "at least one report file is required")
	}
	for _, o := range slices.Sorted(maps.Keys(r.OutcomeCounts)) {
		checkEnum(&p, "outcome_counts", o)
	}
	return p.err()
}
