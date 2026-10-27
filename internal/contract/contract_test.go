package contract

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validDraft() FindingDraft {
	return FindingDraft{
		Title: "t", Claim: "c", TriggeringConditions: "x", ExpectedImpact: "y",
		Remediation: "r", VerificationCriteria: "v",
		Severity: SeverityHigh, Confidence: ConfidenceMedium,
		Locations: []Location{{Path: "a/b.go", StartLine: 1, EndLine: 2}},
		Evidence:  []Evidence{{Kind: EvidenceSource, Description: "d", Path: "a/b.go", StartLine: 1, EndLine: 2, Excerpt: "x := 1"}},
	}
}

func TestDecodeStrict(t *testing.T) {
	var d ValidationDecision
	good := `{"verdict":"reject","evidence_assessment":"a","contradictory_evidence":"b","trigger_assessment":"c","impact_assessment":"d","remediation_assessment":"e","rationale":"f"}`
	if err := Decode([]byte(good), &d); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"unknown field": strings.Replace(good, `"rationale"`, `"surprise":1,"rationale"`, 1),
		"trailing data": good + `{}`,
		"invalid enum":  strings.Replace(good, `"reject"`, `"maybe"`, 1),
		"missing field": strings.Replace(good, `"rationale":"f"`, `"rationale":""`, 1),
		"not json":      `verdict: reject`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			var d ValidationDecision
			if err := Decode([]byte(in), &d); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestDecisionClassificationRules(t *testing.T) {
	d := ValidationDecision{Verdict: VerdictAccept, EvidenceAssessment: "a", ContradictoryEvidence: "b",
		TriggerAssessment: "c", ImpactAssessment: "d", RemediationAssessment: "e", Rationale: "f"}
	if d.Validate() == nil {
		t.Error("accept without classification must be invalid")
	}
	d.Classification = ClassRisk
	if err := d.Validate(); err != nil {
		t.Error(err)
	}
	d.Verdict = VerdictReject
	if d.Validate() == nil {
		t.Error("classification on reject must be invalid")
	}
}

func TestFindingDraftValidation(t *testing.T) {
	if err := (ReviewReport{Findings: []FindingDraft{validDraft()}}).Validate(); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*FindingDraft){
		"no evidence":    func(f *FindingDraft) { f.Evidence = nil },
		"no locations":   func(f *FindingDraft) { f.Locations = nil },
		"escaping path":  func(f *FindingDraft) { f.Locations[0].Path = "../etc/passwd" },
		"absolute path":  func(f *FindingDraft) { f.Evidence[0].Path = "/etc/passwd" },
		"reversed range": func(f *FindingDraft) { f.Locations[0].StartLine = 5 },
		"no excerpt":     func(f *FindingDraft) { f.Evidence[0].Excerpt = " " },
		"bad severity":   func(f *FindingDraft) { f.Severity = "huge" },
		"execution no cmd": func(f *FindingDraft) {
			f.Evidence[0] = Evidence{Kind: EvidenceExecution, Description: "d", Result: "ok"}
		},
		"missing claim":      func(f *FindingDraft) { f.Claim = "" },
		"missing trigger":    func(f *FindingDraft) { f.TriggeringConditions = "" },
		"missing remedation": func(f *FindingDraft) { f.Remediation = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := validDraft()
			mutate(&f)
			if err := (ReviewReport{Findings: []FindingDraft{f}}).Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidRelPath(t *testing.T) {
	for p, want := range map[string]bool{
		"a.go": true, "dir/a.go": true, ".github/ci.yml": true,
		"": false, ".": false, "..": false, "../a": false, "a/../../b": false,
		"/abs": false, "a//b": false, "./a": false, `a\b`: false,
	} {
		if got := ValidRelPath(p); got != want {
			t.Errorf("ValidRelPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestFindingSetRoundTrip(t *testing.T) {
	set := FindingSet{Header: NewHeader(KindFindings, "run"), Findings: []Finding{{
		ID: "ARCH-001", SourceRevision: "abc", Category: CategoryArchitecture, ReportedBy: "architecture",
		FindingDraft: validDraft(), EvidenceChecks: []EvidenceCheck{{Status: CheckVerified, Detail: "ok"}},
		State: StateCandidate,
	}}}
	data, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	// The embedded draft is flattened into the finding object.
	if !strings.Contains(string(data), `"claim":"c"`) || strings.Contains(string(data), "FindingDraft") {
		t.Fatalf("unexpected encoding: %s", data)
	}
	var back FindingSet
	if err := Decode(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Findings[0].Title != "t" || back.Findings[0].State != StateCandidate {
		t.Fatalf("round trip lost data: %+v", back.Findings[0])
	}

	dup := set
	dup.Findings = append(dup.Findings, set.Findings[0])
	if dup.Validate() == nil {
		t.Error("duplicate IDs must be invalid")
	}
	bad := set
	bad.Findings = []Finding{set.Findings[0]}
	bad.Findings[0].EvidenceChecks = nil
	if bad.Validate() == nil {
		t.Error("a finding needs one check per evidence item")
	}
}

func TestHeaderVersioning(t *testing.T) {
	m := Manifest{Header: NewHeader(KindManifest, "r"), Status: RunRunning,
		Repository: Repository{Path: "/r", Commit: "c", SourceRevision: "c", DirtyPolicy: DirtyRefuse},
		CreatedAt:  time.Now()}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m.SchemaVersion = 99
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("expected version error, got %v", err)
	}
	m.SchemaVersion, m.Kind = SchemaVersion, KindDiscovery
	if m.Validate() == nil {
		t.Fatal("wrong kind must be rejected")
	}
}

func TestManifestRejectsUnknownDependency(t *testing.T) {
	m := Manifest{Header: NewHeader(KindManifest, "r"), Status: RunRunning,
		Repository: Repository{Path: "/r", Commit: "c", SourceRevision: "c", DirtyPolicy: DirtyRefuse},
		Tasks:      []TaskState{{ID: "a", Status: TaskPending, DependsOn: []string{"ghost"}}}}
	if m.Validate() == nil {
		t.Fatal("expected error")
	}
}

func TestDecisionSetRequiresOutcome(t *testing.T) {
	s := DecisionSet{Header: NewHeader(KindDecisions, "r"), Decisions: []DecisionRecord{{
		FindingID: "X-1", Validator: "pruv", FinalState: StateUnvalidated, GateReason: "g",
	}}}
	if s.Validate() == nil {
		t.Error("record without decision or error must be invalid")
	}
	s.Decisions[0].Error = "boom"
	if err := s.Validate(); err != nil {
		t.Error(err)
	}
	s.Decisions[0].FinalState = StateCandidate
	if s.Validate() == nil {
		t.Error("candidate is not a final state")
	}
}
