// Package contract defines Sift's versioned stage-boundary artifacts.
//
// The Go types in this package are the single source of truth: the JSON
// Schemas handed to harnesses and published under schemas/ are generated
// from them, and every artifact read from disk or from an agent is decoded
// strictly and passed through Validate.
package contract

import (
	"encoding/json"
	"time"
)

// Artifact kinds and their current schema versions.
const (
	KindManifest   = "manifest"
	KindDiscovery  = "discovery"
	KindReviewPlan = "review_plan"
	KindReview     = "review"
	KindFindings   = "findings"
	KindDecisions  = "validation_decisions"
	KindExecution  = "agent_execution"
	KindReportMeta = "report_metadata"

	SchemaVersion = 1
)

// Header identifies a persisted artifact.
type Header struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
}

func NewHeader(kind, runID string) Header {
	return Header{Kind: kind, SchemaVersion: SchemaVersion, RunID: runID}
}

// ---------------------------------------------------------------------------
// Shared building blocks

// Location is a line range in the audited source snapshot.
type Location struct {
	Path      string `json:"path" desc:"Path relative to the repository root, using forward slashes."`
	StartLine int    `json:"start_line" desc:"First line, 1-based."`
	EndLine   int    `json:"end_line" desc:"Last line, inclusive."`
}

// Evidence supports a claim. Source evidence must quote the cited lines
// exactly; execution evidence records what was run and observed.
type Evidence struct {
	Kind        EvidenceKind `json:"kind"`
	Description string       `json:"description" desc:"What this evidence shows and why it supports the claim."`
	Path        string       `json:"path,omitempty" desc:"Source evidence: file path relative to the repository root."`
	StartLine   int          `json:"start_line,omitempty" desc:"Source evidence: first cited line, 1-based."`
	EndLine     int          `json:"end_line,omitempty" desc:"Source evidence: last cited line, inclusive."`
	Excerpt     string       `json:"excerpt,omitempty" desc:"Source evidence: the cited lines copied verbatim from the file."`
	Command     string       `json:"command,omitempty" desc:"Execution evidence: the exact command run."`
	Environment string       `json:"environment,omitempty" desc:"Execution evidence: relevant environment."`
	Result      string       `json:"result,omitempty" desc:"Execution evidence: observed result."`
	Limitations string       `json:"limitations,omitempty" desc:"What this evidence does not establish."`
}

// ---------------------------------------------------------------------------
// Discovery

type Module struct {
	Path           string `json:"path"`
	Name           string `json:"name"`
	Responsibility string `json:"responsibility"`
}

type EntryPoint struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

type Dependency struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
	Source  string `json:"source" desc:"Manifest file that declares it, e.g. go.mod."`
}

// DiscoveryReport is the Discovery agent's output.
type DiscoveryReport struct {
	Summary          string       `json:"summary" desc:"Two to four sentences: what the system is and does."`
	Purpose          string       `json:"purpose" desc:"Primary purpose and workflows, as evidenced by the repository."`
	Languages        []string     `json:"languages"`
	Frameworks       []string     `json:"frameworks"`
	EntryPoints      []EntryPoint `json:"entry_points"`
	Modules          []Module     `json:"modules"`
	Dependencies     []Dependency `json:"dependencies"`
	BuildConventions []string     `json:"build_conventions"`
	TestConventions  []string     `json:"test_conventions"`
	CriticalPaths    []string     `json:"critical_paths" desc:"Important execution paths worth reviewing."`
	Unknowns         []string     `json:"unknowns" desc:"Requirements, constraints, or areas that could not be determined."`
	Completeness     Completeness `json:"completeness"`
}

// Discovery is the persisted system discovery context.
type Discovery struct {
	Header
	SourceRevision string          `json:"source_revision"`
	Report         DiscoveryReport `json:"report"`
}

// ---------------------------------------------------------------------------
// Review plan

// ReviewTask is one reviewer invocation, derived deterministically from
// configuration and discovery.
type ReviewTask struct {
	ID       string   `json:"id"`
	Agent    string   `json:"agent"`
	Category Category `json:"category"`
	Focus    []string `json:"focus" desc:"Critical paths and modules highlighted by discovery."`
}

type ReviewPlan struct {
	Header
	Tasks             []ReviewTask `json:"tasks"`
	MaxFindingsPerRun int          `json:"max_findings_per_reviewer"`
}

// ---------------------------------------------------------------------------
// Reviews and findings

// FindingDraft is a reviewer's candidate finding, before Sift assigns
// identity and checks evidence.
type FindingDraft struct {
	Title                string     `json:"title"`
	Claim                string     `json:"claim" desc:"Precise, falsifiable statement of the problem."`
	Locations            []Location `json:"locations"`
	Evidence             []Evidence `json:"evidence"`
	TriggeringConditions string     `json:"triggering_conditions" desc:"Concrete conditions under which the problem manifests."`
	ExpectedImpact       string     `json:"expected_impact"`
	Severity             Severity   `json:"severity"`
	Confidence           Confidence `json:"confidence"`
	Remediation          string     `json:"remediation"`
	Alternatives         string     `json:"alternatives,omitempty" desc:"Other options and their trade-offs."`
	VerificationCriteria string     `json:"verification_criteria" desc:"How to verify that remediation worked."`
}

type Strength struct {
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Locations   []Location `json:"locations"`
}

// ReviewReport is a specialist reviewer's output.
type ReviewReport struct {
	Findings        []FindingDraft `json:"findings"`
	Strengths       []Strength     `json:"strengths"`
	ReviewedScope   []string       `json:"reviewed_scope" desc:"Paths and concerns actually examined."`
	UnreviewedScope []string       `json:"unreviewed_scope" desc:"Relevant areas not examined."`
	Limitations     []string       `json:"limitations"`
}

// Review is a persisted reviewer output.
type Review struct {
	Header
	TaskID   string       `json:"task_id"`
	Agent    string       `json:"agent"`
	Category Category     `json:"category"`
	Report   ReviewReport `json:"report"`
}

// EvidenceCheck is Sift's own mechanical verification of one evidence item
// against the audited snapshot.
type EvidenceCheck struct {
	Index  int         `json:"index"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
	// Actual line range when the excerpt was found elsewhere in the file.
	ActualStartLine int `json:"actual_start_line,omitempty"`
	ActualEndLine   int `json:"actual_end_line,omitempty"`
}

// Finding is a candidate finding with Sift-assigned identity and its current
// validation state.
type Finding struct {
	ID             string   `json:"id"`
	SourceRevision string   `json:"source_revision"`
	Category       Category `json:"category"`
	ReportedBy     string   `json:"reported_by"`
	FindingDraft
	EvidenceChecks []EvidenceCheck `json:"evidence_checks"`
	State          ValidationState `json:"validation_state"`
	StateReason    string          `json:"validation_state_reason,omitempty"`
}

// FindingSet is a persisted collection of findings (candidates or final).
type FindingSet struct {
	Header
	Findings []Finding `json:"findings"`
}

// ---------------------------------------------------------------------------
// Validation

// ValidationDecision is Pruv's output for one finding.
type ValidationDecision struct {
	Verdict               Verdict        `json:"verdict"`
	Classification        Classification `json:"classification,omitempty" desc:"Required when verdict is accept."`
	EvidenceAssessment    string         `json:"evidence_assessment" desc:"Whether the cited evidence exists and says what the finding claims."`
	ContradictoryEvidence string         `json:"contradictory_evidence" desc:"What you searched for that could refute the claim, and what you found."`
	TriggerAssessment     string         `json:"trigger_assessment" desc:"Whether the triggering conditions are reachable in practice."`
	ImpactAssessment      string         `json:"impact_assessment"`
	RemediationAssessment string         `json:"remediation_assessment"`
	AdjustedSeverity      Severity       `json:"adjusted_severity,omitempty"`
	AdjustedConfidence    Confidence     `json:"adjusted_confidence,omitempty"`
	AdditionalEvidence    []Evidence     `json:"additional_evidence,omitempty"`
	OpenQuestions         []string       `json:"open_questions,omitempty"`
	Rationale             string         `json:"rationale"`
}

// DecisionRecord is a persisted validation outcome for one finding. The
// original finding is never modified; the gate result is recorded here.
type DecisionRecord struct {
	FindingID        string              `json:"finding_id"`
	Validator        string              `json:"validator"`
	ValidatorVersion string              `json:"validator_version"`
	Decision         *ValidationDecision `json:"decision,omitempty"`
	AdditionalChecks []EvidenceCheck     `json:"additional_evidence_checks,omitempty"`
	FinalState       ValidationState     `json:"final_state"`
	GateReason       string              `json:"gate_reason"`
	Error            string              `json:"error,omitempty"`
}

type DecisionSet struct {
	Header
	Decisions []DecisionRecord `json:"decisions"`
}

// ---------------------------------------------------------------------------
// Execution

// ExecutionResult records one agent execution: who ran, how, and what
// happened. It is persisted for every attempt, successful or not.
type ExecutionResult struct {
	Header
	TaskID            string    `json:"task_id"`
	Agent             string    `json:"agent"`
	AgentVersion      string    `json:"agent_version"`
	Attempt           int       `json:"attempt"`
	Succeeded         bool      `json:"succeeded"`
	StartedAt         time.Time `json:"started_at"`
	EndedAt           time.Time `json:"ended_at"`
	DurationMS        int64     `json:"duration_ms"`
	Harness           string    `json:"harness"`
	HarnessVersion    string    `json:"harness_version"`
	Model             string    `json:"model,omitempty"`
	SessionID         string    `json:"session_id,omitempty"`
	CostUSD           float64   `json:"cost_usd,omitempty"`
	NumTurns          int       `json:"num_turns,omitempty"`
	PermissionDenials int       `json:"permission_denials"`
	ExitCode          int       `json:"exit_code"`
	ErrorKind         string    `json:"error_kind,omitempty"`
	Error             string    `json:"error,omitempty"`
	Dir               string    `json:"dir" desc:"Run-relative directory holding prompt and raw output."`
}

// ---------------------------------------------------------------------------
// Manifest

type Repository struct {
	Path            string      `json:"path"`
	Commit          string      `json:"commit"`
	Branch          string      `json:"branch,omitempty"`
	Dirty           bool        `json:"dirty"`
	StatusPorcelain []string    `json:"status_porcelain,omitempty"`
	DirtyPolicy     DirtyPolicy `json:"dirty_policy"`
	DiffSHA256      string      `json:"diff_sha256,omitempty"`
	// SourceRevision identifies exactly what was audited: the commit, plus a
	// snapshot digest suffix when uncommitted content was included.
	SourceRevision string   `json:"source_revision"`
	SnapshotDigest string   `json:"snapshot_digest"`
	SnapshotFiles  int      `json:"snapshot_files"`
	SkippedPaths   []string `json:"skipped_paths,omitempty"`
}

type HarnessInfo struct {
	Name         string   `json:"name"`
	Binary       string   `json:"binary"`
	Version      string   `json:"version"`
	Model        string   `json:"model,omitempty"`
	Capabilities []string `json:"capabilities"`
}

type AgentRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Source  string `json:"source"`
	Model   string `json:"model,omitempty"`
}

// Policy records the execution policy and how it was enforced.
type Policy struct {
	Tools            []string `json:"tools"`
	CommandExecution bool     `json:"command_execution"`
	Network          bool     `json:"network"`
	Enforcement      []string `json:"enforcement"`
	Unenforced       []string `json:"unenforced"`
}

type TaskState struct {
	ID         string     `json:"id"`
	DependsOn  []string   `json:"depends_on,omitempty"`
	Status     TaskStatus `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	Artifacts  []string   `json:"artifacts,omitempty"`
	Executions []string   `json:"executions,omitempty" desc:"Run-relative paths of execution results."`
}

type Integrity struct {
	RepositoryUnchanged bool   `json:"repository_unchanged"`
	Detail              string `json:"detail"`
}

type Manifest struct {
	Header
	SiftVersion string          `json:"sift_version"`
	Status      RunStatus       `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	PID         int             `json:"pid"`
	Host        string          `json:"host"`
	Repository  Repository      `json:"repository"`
	Config      json.RawMessage `json:"config"`
	Harness     HarnessInfo     `json:"harness"`
	Agents      []AgentRef      `json:"agents"`
	Policy      Policy          `json:"policy"`
	Tasks       []TaskState     `json:"tasks"`
	Integrity   *Integrity      `json:"integrity,omitempty"`
	Warnings    []string        `json:"warnings,omitempty"`
}

// Task returns the named task state, or nil.
func (m *Manifest) Task(id string) *TaskState {
	for i := range m.Tasks {
		if m.Tasks[i].ID == id {
			return &m.Tasks[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Report

type ReportMeta struct {
	Header
	GeneratedAt    time.Time               `json:"generated_at"`
	RunStatus      RunStatus               `json:"run_status"`
	Partial        bool                    `json:"partial"`
	SourceRevision string                  `json:"source_revision"`
	Files          []string                `json:"files"`
	StateCounts    map[ValidationState]int `json:"state_counts"`
	Limitations    []string                `json:"limitations"`
}
