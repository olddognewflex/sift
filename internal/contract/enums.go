package contract

import "slices"

// Enumerated string types implement Enum so that validation and generated
// JSON Schemas share one definition of the allowed values.

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

func (Severity) Enum() []string { return []string{"critical", "high", "medium", "low", "info"} }

// Rank orders severities for prioritization; lower is more severe.
func (s Severity) Rank() int { return rank(s.Enum(), string(s)) }

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

func (Confidence) Enum() []string { return []string{"high", "medium", "low"} }

func (c Confidence) Rank() int { return rank(c.Enum(), string(c)) }

// Category is a finding category. Each category is produced by one
// specialist reviewer of the same name.
type Category string

const (
	CategoryArchitecture Category = "architecture"
	CategoryCorrectness  Category = "correctness"
)

// Reviewer registers a specialist reviewer. Its category is also its config
// name and its agent definition (agents/<category>.md); Prefix starts its
// finding IDs (ARCH-001).
type Reviewer struct {
	Category Category
	Prefix   string
}

// Reviewers lists every specialist reviewer. To add one, add it here and add
// agents/<category>.md; tests check that the rest of Sift agrees.
var Reviewers = []Reviewer{
	{CategoryArchitecture, "ARCH"},
	{CategoryCorrectness, "CORR"},
}

// ReviewerNames returns the registered reviewer names in order.
func ReviewerNames() []string {
	out := make([]string, len(Reviewers))
	for i, r := range Reviewers {
		out[i] = string(r.Category)
	}
	return out
}

func (Category) Enum() []string { return ReviewerNames() }

// Prefix returns the finding ID prefix for c, or "" if c is not registered.
func (c Category) Prefix() string {
	for _, r := range Reviewers {
		if r.Category == c {
			return r.Prefix
		}
	}
	return ""
}

type EvidenceKind string

const (
	// EvidenceSource cites lines of the audited source snapshot.
	EvidenceSource EvidenceKind = "source"
	// EvidenceExecution records a command and its observed result.
	EvidenceExecution EvidenceKind = "execution"
)

func (EvidenceKind) Enum() []string { return []string{"source", "execution"} }

type Verdict string

const (
	VerdictAccept      Verdict = "accept"
	VerdictReject      Verdict = "reject"
	VerdictInvestigate Verdict = "investigate"
)

func (Verdict) Enum() []string { return []string{"accept", "reject", "investigate"} }

// Classification distinguishes a demonstrated defect from a supported risk
// whose triggering conditions are plausible but not demonstrated.
type Classification string

const (
	ClassDefect Classification = "defect"
	ClassRisk   Classification = "risk"
)

func (Classification) Enum() []string { return []string{"defect", "risk"} }

type ValidationState string

const (
	StateCandidate          ValidationState = "candidate"
	StateConfirmed          ValidationState = "confirmed"
	StateSupportedRisk      ValidationState = "supported_risk"
	StateRejected           ValidationState = "rejected"
	StateNeedsInvestigation ValidationState = "needs_investigation"
	// StateUnvalidated means validation could not be performed (e.g. the
	// validator failed). It is never presented as accepted.
	StateUnvalidated ValidationState = "unvalidated"
)

func (ValidationState) Enum() []string {
	return []string{"candidate", "confirmed", "supported_risk", "rejected", "needs_investigation", "unvalidated"}
}

type CheckStatus string

const (
	CheckVerified      CheckStatus = "verified"
	CheckRelocated     CheckStatus = "relocated"
	CheckMismatch      CheckStatus = "mismatch"
	CheckMissingFile   CheckStatus = "missing_file"
	CheckInvalidRange  CheckStatus = "invalid_range"
	CheckNotVerifiable CheckStatus = "not_verifiable"
)

func (CheckStatus) Enum() []string {
	return []string{"verified", "relocated", "mismatch", "missing_file", "invalid_range", "not_verifiable"}
}

// Supports reports whether the check substantiates the evidence item.
func (c CheckStatus) Supports() bool { return c == CheckVerified || c == CheckRelocated }

// Contradicts reports whether the check shows the evidence is wrong about the
// audited revision.
func (c CheckStatus) Contradicts() bool {
	return c == CheckMismatch || c == CheckMissingFile || c == CheckInvalidRange
}

type Completeness string

const (
	CompletenessComplete Completeness = "complete"
	CompletenessPartial  Completeness = "partial"
)

func (Completeness) Enum() []string { return []string{"complete", "partial"} }

type RunStatus string

const (
	RunRunning     RunStatus = "running"
	RunCompleted   RunStatus = "completed"
	RunPartial     RunStatus = "partial"
	RunFailed      RunStatus = "failed"
	RunInterrupted RunStatus = "interrupted"
)

func (RunStatus) Enum() []string {
	return []string{"running", "completed", "partial", "failed", "interrupted"}
}

type TaskStatus string

const (
	TaskPending     TaskStatus = "pending"
	TaskRunning     TaskStatus = "running"
	TaskSucceeded   TaskStatus = "succeeded"
	TaskFailed      TaskStatus = "failed"
	TaskSkipped     TaskStatus = "skipped"
	TaskInterrupted TaskStatus = "interrupted"
)

func (TaskStatus) Enum() []string {
	return []string{"pending", "running", "succeeded", "failed", "skipped", "interrupted"}
}

func (s TaskStatus) Done() bool { return s != TaskPending && s != TaskRunning }

type DirtyPolicy string

const (
	DirtyRefuse  DirtyPolicy = "refuse"
	DirtyHead    DirtyPolicy = "head"
	DirtyInclude DirtyPolicy = "include"
)

func (DirtyPolicy) Enum() []string { return []string{"refuse", "head", "include"} }

type enum interface {
	~string
	Enum() []string
}

// Valid reports whether v is one of its type's enumerated values.
func Valid[T enum](v T) bool { return slices.Contains(v.Enum(), string(v)) }

func rank(values []string, v string) int {
	if i := slices.Index(values, v); i >= 0 {
		return i
	}
	return len(values)
}
