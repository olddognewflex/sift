package contract

import (
	"encoding/json"
	"regexp"
	"slices"
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
		LocationChecks: []EvidenceCheck{{Status: CheckVerified, Detail: "ok"}},
		State:          StateCandidate,
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
	wrongLoc := set
	wrongLoc.Findings = []Finding{set.Findings[0]}
	wrongLoc.Findings[0].Locations = append(slices.Clone(wrongLoc.Findings[0].Locations), Location{Path: "c.go", StartLine: 1, EndLine: 1})
	if wrongLoc.Validate() == nil {
		t.Error("a finding with location checks needs one per location")
	}

	// Runs recorded before locations were checked must still load.
	old := strings.Replace(string(data), `"location_checks":[{"index":0,"status":"verified","detail":"ok"}],`, "", 1)
	if old == string(data) {
		t.Fatalf("test setup: location_checks not found in %s", data)
	}
	var legacy FindingSet
	if err := Decode([]byte(old), &legacy); err != nil {
		t.Errorf("finding without location checks: %v", err)
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

func TestReviewerRegistry(t *testing.T) {
	prefix := regexp.MustCompile(`^[A-Z]{2,6}$`)
	names, prefixes := map[Category]bool{}, map[string]bool{}
	for _, r := range Reviewers {
		if r.Category == "" || names[r.Category] {
			t.Errorf("reviewer %q: empty or duplicate name", r.Category)
		}
		if !prefix.MatchString(r.Prefix) || prefixes[r.Prefix] {
			t.Errorf("reviewer %q: prefix %q is invalid or duplicate", r.Category, r.Prefix)
		}
		if r.Category.Prefix() != r.Prefix {
			t.Errorf("%q.Prefix() = %q, want %q", r.Category, r.Category.Prefix(), r.Prefix)
		}
		names[r.Category], prefixes[r.Prefix] = true, true
	}
	for _, c := range []Category{CategoryArchitecture, CategoryCorrectness} {
		if !names[c] {
			t.Errorf("category %q is not registered", c)
		}
	}
	if Category("performance").Prefix() != "" {
		t.Error("an unregistered category must have no prefix")
	}
}

func validDecision(v Verdict, c Classification) *ValidationDecision {
	return &ValidationDecision{Verdict: v, Classification: c, EvidenceAssessment: "a", ContradictoryEvidence: "b",
		TriggerAssessment: "c", ImpactAssessment: "d", RemediationAssessment: "e", Rationale: "f"}
}

func validVerificationSet() VerificationSet {
	dec := validDecision(VerdictReject, "")
	dec.AdditionalEvidence = []Evidence{{Kind: EvidenceSource, Description: "the guard", Path: "a/c.go", StartLine: 3, EndLine: 3, Excerpt: "if len(xs) == 0 {"}}
	return VerificationSet{Header: NewHeader(KindVerification, "run"), Records: []VerificationRecord{{
		FindingID: "CORR-001", OriginalState: StateConfirmed, Severity: SeverityHigh, Confidence: ConfidenceMedium,
		Relocations: []Relocation{{
			EvidenceCheck: EvidenceCheck{Status: CheckRelocated, Detail: "moved", ActualStartLine: 4, ActualEndLine: 4},
			ActualPath:    "a/moved.go",
		}},
		Validator: "pruv", ValidatorVersion: "3", Decision: dec,
		AdditionalChecks:       []Relocation{{EvidenceCheck: EvidenceCheck{Status: CheckVerified, Detail: "ok"}}},
		AdditionalPriorMatches: []int{0},
		Outcome:                OutcomeFixed, OutcomeReason: "validator says fixed",
	}}}
}

func TestVerificationSetRoundTrip(t *testing.T) {
	set := validVerificationSet()
	data, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	// The embedded check is flattened next to actual_path, and an unset
	// actual_path is omitted.
	for _, want := range []string{`"status":"relocated","detail":"moved","actual_start_line":4,"actual_end_line":4,"actual_path":"a/moved.go"`, `"additional_evidence_prior_matches":[0]`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("encoding lacks %s: %s", want, data)
		}
	}
	if strings.Contains(string(data), "EvidenceCheck") || strings.Count(string(data), `"actual_path"`) != 1 {
		t.Fatalf("unexpected encoding: %s", data)
	}
	var back VerificationSet
	if err := Decode(data, &back); err != nil {
		t.Fatal(err)
	}
	r := back.Records[0]
	if r.Outcome != OutcomeFixed || r.Relocations[0].ActualPath != "a/moved.go" || r.Relocations[0].ActualStartLine != 4 ||
		r.AdditionalPriorMatches[0] != 0 || r.Decision == nil || len(r.Decision.AdditionalEvidence) != 1 {
		t.Fatalf("round trip lost data: %+v", r)
	}
	unknown := strings.Replace(string(data), `"outcome":`, `"surprise":1,"outcome":`, 1)
	if err := Decode([]byte(unknown), &back); err == nil {
		t.Error("strict decoding must reject unknown fields")
	}
}

func TestVerificationSetValidation(t *testing.T) {
	if err := validVerificationSet().Validate(); err != nil {
		t.Fatal(err)
	}
	// Records that must stay valid.
	valid := map[string]func(*VerificationRecord){
		"supported risk": func(r *VerificationRecord) { r.OriginalState = StateSupportedRisk },
		"unverified with error": func(r *VerificationRecord) {
			r.Decision, r.AdditionalChecks, r.AdditionalPriorMatches = nil, nil, nil
			r.Outcome, r.Error = OutcomeUnverified, "timed out"
		},
		"identical snapshot still present": func(r *VerificationRecord) {
			r.Decision, r.AdditionalChecks, r.AdditionalPriorMatches = nil, nil, nil
			r.Outcome, r.OutcomeReason = OutcomeStillPresent, "snapshot identical to the audited one"
		},
		"decision without additional evidence": func(r *VerificationRecord) {
			r.Decision.AdditionalEvidence, r.AdditionalChecks, r.AdditionalPriorMatches = nil, nil, nil
			r.Outcome = OutcomeCannotTell
		},
		"unsearchable prior match": func(r *VerificationRecord) { r.AdditionalPriorMatches = []int{-1} },
	}
	for name, mutate := range valid {
		t.Run(name, func(t *testing.T) {
			s := validVerificationSet()
			mutate(&s.Records[0])
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
	invalid := map[string]func(*VerificationSet){
		"wrong kind":           func(s *VerificationSet) { s.Kind = KindDecisions },
		"no finding id":        func(s *VerificationSet) { s.Records[0].FindingID = "" },
		"no validator":         func(s *VerificationSet) { s.Records[0].Validator = "" },
		"no validator version": func(s *VerificationSet) { s.Records[0].ValidatorVersion = " " },
		"unverified without validator": func(s *VerificationSet) {
			r := &s.Records[0]
			r.Decision, r.AdditionalChecks, r.AdditionalPriorMatches = nil, nil, nil
			r.Outcome, r.Error, r.Validator = OutcomeUnverified, "timed out", ""
		},
		"duplicate finding":     func(s *VerificationSet) { s.Records = append(s.Records, s.Records[0]) },
		"rejected original":     func(s *VerificationSet) { s.Records[0].OriginalState = StateRejected },
		"needs investigation":   func(s *VerificationSet) { s.Records[0].OriginalState = StateNeedsInvestigation },
		"candidate original":    func(s *VerificationSet) { s.Records[0].OriginalState = StateCandidate },
		"bad severity":          func(s *VerificationSet) { s.Records[0].Severity = "huge" },
		"bad confidence":        func(s *VerificationSet) { s.Records[0].Confidence = "" },
		"bad outcome":           func(s *VerificationSet) { s.Records[0].Outcome = "gone" },
		"empty outcome":         func(s *VerificationSet) { s.Records[0].Outcome = "" },
		"no outcome reason":     func(s *VerificationSet) { s.Records[0].OutcomeReason = " " },
		"bad relocation status": func(s *VerificationSet) { s.Records[0].Relocations[0].Status = "moved" },
		"escaping actual path":  func(s *VerificationSet) { s.Records[0].Relocations[0].ActualPath = "../x.go" },
		"unverified without error": func(s *VerificationSet) {
			r := &s.Records[0]
			r.Decision, r.AdditionalChecks, r.AdditionalPriorMatches, r.Outcome = nil, nil, nil, OutcomeUnverified
		},
		"unverified with decision": func(s *VerificationSet) {
			s.Records[0].Outcome, s.Records[0].Error = OutcomeUnverified, "boom"
		},
		"fixed without decision": func(s *VerificationSet) {
			r := &s.Records[0]
			r.Decision, r.AdditionalChecks, r.AdditionalPriorMatches = nil, nil, nil
		},
		"cannot tell without decision": func(s *VerificationSet) {
			r := &s.Records[0]
			r.Decision, r.AdditionalChecks, r.AdditionalPriorMatches, r.Outcome = nil, nil, nil, OutcomeCannotTell
		},
		"identical snapshot with error": func(s *VerificationSet) {
			r := &s.Records[0]
			r.Decision, r.AdditionalChecks, r.AdditionalPriorMatches = nil, nil, nil
			r.Outcome, r.Error = OutcomeStillPresent, "boom"
		},
		"additional checks without decision": func(s *VerificationSet) {
			r := &s.Records[0]
			r.Decision, r.AdditionalPriorMatches, r.Outcome, r.Error = nil, nil, OutcomeUnverified, "boom"
		},
		"prior matches without decision": func(s *VerificationSet) {
			r := &s.Records[0]
			r.Decision, r.AdditionalChecks, r.Outcome = nil, nil, OutcomeStillPresent
		},
		"invalid decision":         func(s *VerificationSet) { s.Records[0].Decision.Rationale = "" },
		"missing additional check": func(s *VerificationSet) { s.Records[0].AdditionalChecks = nil },
		"extra additional check": func(s *VerificationSet) {
			s.Records[0].AdditionalChecks = append(s.Records[0].AdditionalChecks, Relocation{EvidenceCheck: EvidenceCheck{Status: CheckVerified}})
		},
		"missing prior match":        func(s *VerificationSet) { s.Records[0].AdditionalPriorMatches = nil },
		"extra prior match":          func(s *VerificationSet) { s.Records[0].AdditionalPriorMatches = []int{0, 1} },
		"bad additional status":      func(s *VerificationSet) { s.Records[0].AdditionalChecks[0].Status = "" },
		"prior match below sentinel": func(s *VerificationSet) { s.Records[0].AdditionalPriorMatches = []int{-2} },
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			s := validVerificationSet()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func validManifest() Manifest {
	return Manifest{Header: NewHeader(KindManifest, "r"), Status: RunRunning,
		Repository: Repository{Path: "/r", Commit: "c", SourceRevision: "c", DirtyPolicy: DirtyRefuse},
		Config:     json.RawMessage(`{}`), CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC()}
}

func TestManifestVerifies(t *testing.T) {
	// Audit manifests have no verifies field and must keep decoding.
	audit, err := json.Marshal(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(audit), "verifies") {
		t.Fatalf("audit manifest encodes verifies: %s", audit)
	}
	var m Manifest
	if err := Decode(audit, &m); err != nil || m.Verifies != nil {
		t.Fatalf("audit manifest: %v, verifies = %+v", err, m.Verifies)
	}

	target := func() *VerifyTarget {
		return &VerifyTarget{Run: "20261027T153012Z-a1b2c3", SourceRevision: "c", SnapshotDigest: "d",
			Findings: []string{"ARCH-002", "CORR-001"}, FindingsSHA256: "s"}
	}
	v := validManifest()
	v.Verifies = target()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back Manifest
	if err := Decode(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Verifies == nil || back.Verifies.Run != v.Verifies.Run || !slices.Equal(back.Verifies.Findings, v.Verifies.Findings) {
		t.Fatalf("round trip lost verifies: %+v", back.Verifies)
	}

	invalid := map[string]func(*VerifyTarget){
		"no run":      func(v *VerifyTarget) { v.Run = "" },
		"no revision": func(v *VerifyTarget) { v.SourceRevision = "" },
		"no digest":   func(v *VerifyTarget) { v.SnapshotDigest = "" },
		"no sha256":   func(v *VerifyTarget) { v.FindingsSHA256 = " " },
		"no findings": func(v *VerifyTarget) { v.Findings = nil },
		"unsorted":    func(v *VerifyTarget) { v.Findings = []string{"CORR-001", "ARCH-002"} },
		"duplicate":   func(v *VerifyTarget) { v.Findings = []string{"ARCH-002", "ARCH-002"} },
		"empty id":    func(v *VerifyTarget) { v.Findings = []string{"", "ARCH-002"} },
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			m.Verifies = target()
			mutate(m.Verifies)
			if err := m.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestReportMetaOutcomeCounts(t *testing.T) {
	r := ReportMeta{Header: NewHeader(KindReportMeta, "r"), RunStatus: RunCompleted, Files: []string{"verification.md"},
		VerifiesRun: "orig", OutcomeCounts: map[VerifyOutcome]int{OutcomeFixed: 1, OutcomeStillPresent: 2}}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back ReportMeta
	if err := Decode(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.VerifiesRun != "orig" || back.OutcomeCounts[OutcomeStillPresent] != 2 {
		t.Fatalf("round trip lost data: %+v", back)
	}
	audit, err := json.Marshal(ReportMeta{Header: NewHeader(KindReportMeta, "r"), RunStatus: RunCompleted, Files: []string{"a.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(audit), "verifies_run") || strings.Contains(string(audit), "outcome_counts") {
		t.Fatalf("audit report metadata encodes verify fields: %s", audit)
	}
	r.OutcomeCounts["gone"] = 1
	if r.Validate() == nil {
		t.Error("an unknown outcome must be invalid")
	}
}

func costManifest() Manifest {
	return Manifest{Header: NewHeader(KindManifest, "r"), Status: RunCompleted,
		Repository: Repository{Path: "/r", Commit: "c", SourceRevision: "c", DirtyPolicy: DirtyRefuse},
		Config:     json.RawMessage(`{}`),
		Cost: &RunCost{
			Estimate: CostEstimate{Kind: "audit", Model: "m/v1", Files: 42, Reviewers: 2, PruvCallsLow: 8, PruvCallsHigh: 16,
				Rates:        CostRates{DiscoveryUSD: 0.5, FileReviewUSD: 0.02, PruvCallUSD: 0.3},
				DiscoveryUSD: 0.5, ReviewersUSD: 1.68, PruvLowUSD: 2.4, PruvHighUSD: 4.8, LowUSD: 4.58, HighUSD: 6.98},
			BudgetUSD: 10, Approval: ApprovalWithinBudget,
			Actual: CostActual{USD: 0.07, Executions: 7, ReportedExecutions: 7, PruvExecutions: 4, Candidates: 4},
		}}
}

func TestManifestCostRoundTrip(t *testing.T) {
	m := costManifest()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"cost":{"estimate":{`, `"harness_enforces_budget":false`, `"approval":"within_budget"`, `"reported_executions":7`} {
		if !strings.Contains(string(data), k) {
			t.Errorf("encoding lacks %s: %s", k, data)
		}
	}
	var back Manifest
	if err := Decode(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Cost == nil || *back.Cost != *m.Cost {
		t.Errorf("round trip = %+v, want %+v", back.Cost, m.Cost)
	}

	// Runs recorded before cost estimates existed have no cost.
	m.Cost = nil
	data, _ = json.Marshal(m)
	if strings.Contains(string(data), `"cost"`) {
		t.Fatalf("nil cost must be omitted: %s", data)
	}
	var legacy Manifest
	if err := Decode(data, &legacy); err != nil || legacy.Cost != nil {
		t.Errorf("manifest without cost: %+v, %v", legacy.Cost, err)
	}
}

func TestManifestCostValidation(t *testing.T) {
	for name, mutate := range map[string]func(*RunCost){
		"low above high":        func(c *RunCost) { c.Estimate.LowUSD = 7 },
		"bad approval":          func(c *RunCost) { c.Approval = "maybe" },
		"empty approval":        func(c *RunCost) { c.Approval = "" },
		"no model":              func(c *RunCost) { c.Estimate.Model = "" },
		"negative low":          func(c *RunCost) { c.Estimate.LowUSD = -1 },
		"negative budget":       func(c *RunCost) { c.BudgetUSD = -1 },
		"negative rate":         func(c *RunCost) { c.Estimate.Rates.PruvCallUSD = -0.3 },
		"negative files":        func(c *RunCost) { c.Estimate.Files = -1 },
		"calls low above high":  func(c *RunCost) { c.Estimate.PruvCallsLow = 17 },
		"negative actual usd":   func(c *RunCost) { c.Actual.USD = -0.01 },
		"negative candidates":   func(c *RunCost) { c.Actual.Candidates = -1 },
		"reported above total":  func(c *RunCost) { c.Actual.ReportedExecutions = 8 },
		"pruv above total":      func(c *RunCost) { c.Actual.PruvExecutions = 8 },
		"unknown kind":          func(c *RunCost) { c.Estimate.Kind = "benchmark" },
		"empty kind":            func(c *RunCost) { c.Estimate.Kind = "" },
		"negative high":         func(c *RunCost) { c.Estimate.LowUSD, c.Estimate.HighUSD = -2, -1 },
		"no_budget with budget": func(c *RunCost) { c.Approval = ApprovalNoBudget },
		"within without budget": func(c *RunCost) { c.BudgetUSD = 0 },
		"within but over":       func(c *RunCost) { c.BudgetUSD = 5 },
		"yes_flag within":       func(c *RunCost) { c.Approval = ApprovalYesFlag },
		"prompt within":         func(c *RunCost) { c.Approval = ApprovalPrompt },
		"yes_flag no budget":    func(c *RunCost) { c.Approval, c.BudgetUSD = ApprovalYesFlag, 0 },
	} {
		t.Run(name, func(t *testing.T) {
			m := costManifest()
			mutate(m.Cost)
			if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "cost.") {
				t.Fatalf("expected a cost validation error, got %v", err)
			}
		})
	}
	for name, mutate := range map[string]func(*RunCost){
		"low == high":         func(c *RunCost) { c.Estimate.LowUSD, c.Estimate.HighUSD = 1.5, 1.5 },
		"high == budget":      func(c *RunCost) { c.BudgetUSD = 6.98 },
		"no_budget":           func(c *RunCost) { c.Approval, c.BudgetUSD = ApprovalNoBudget, 0 },
		"yes_flag over":       func(c *RunCost) { c.Approval, c.BudgetUSD = ApprovalYesFlag, 5 },
		"prompt over":         func(c *RunCost) { c.Approval, c.BudgetUSD = ApprovalPrompt, 5 },
		"unchecked over":      func(c *RunCost) { c.Approval, c.BudgetUSD = ApprovalUnchecked, 5 },
		"unchecked within":    func(c *RunCost) { c.Approval = ApprovalUnchecked },
		"unchecked no budget": func(c *RunCost) { c.Approval, c.BudgetUSD = ApprovalUnchecked, 0 },
		"verify kind":         func(c *RunCost) { c.Estimate.Kind = "verify" },
		"total kind":          func(c *RunCost) { c.Estimate.Kind = "total" },
	} {
		t.Run("valid "+name, func(t *testing.T) {
			m := costManifest()
			mutate(m.Cost)
			if err := m.Validate(); err != nil {
				t.Errorf("expected valid: %v", err)
			}
		})
	}
}
