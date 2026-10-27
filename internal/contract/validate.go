package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
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
	return p.err()
}

func (r ReportMeta) Validate() error {
	var p problems
	r.Header.validate(&p, KindReportMeta)
	checkEnum(&p, "run_status", r.RunStatus)
	if len(r.Files) == 0 {
		p.add("files", "at least one report file is required")
	}
	return p.err()
}
