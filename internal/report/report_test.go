package report

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/storage"
	"github.com/olddognewflex/sift/internal/testutil"
)

const (
	auditID  = "20261027T100000Z-aaaaaa"
	verifyID = "20261028T100000Z-bbbbbb"
)

// finding returns an established finding as copied from the audited run.
func finding(id string, d contract.FindingDraft, state contract.ValidationState) contract.Finding {
	return contract.Finding{
		FindingDraft: d, ID: id, Category: contract.CategoryCorrectness, ReportedBy: "correctness",
		SourceRevision: "rev1", State: state, StateReason: "passed the gate",
		EvidenceChecks: []contract.EvidenceCheck{{Index: 0, Status: contract.CheckVerified, Detail: "matches"}},
	}
}

func decision(v contract.Verdict) *contract.ValidationDecision {
	d := &contract.ValidationDecision{
		Verdict: v, EvidenceAssessment: "re-read the cited lines", ContradictoryEvidence: "searched callers",
		TriggerAssessment: "reachable", ImpactAssessment: "real", RemediationAssessment: "done",
		Rationale: "the loop bound now covers the last element",
	}
	if v == contract.VerdictAccept {
		d.Classification = contract.ClassDefect
	}
	return d
}

// verifyRun builds a verify run by hand. It re-checks ARCH-001 (fixed),
// CORR-001 (still present, code moved to another file) and CORR-002 (no
// record yet). writeRecords=false leaves verification.json out.
func verifyRun(t *testing.T, writeRecords bool) *storage.Run {
	t.Helper()
	store := storage.Store{Root: t.TempDir()}
	run, err := store.Create(verifyID)
	if err != nil {
		t.Fatal(err)
	}
	arch := testutil.ArchitectureReview().Findings[0]
	corr := testutil.CorrectnessReview().Findings
	m := &contract.Manifest{
		Header: contract.NewHeader(contract.KindManifest, verifyID), Status: contract.RunPartial,
		CreatedAt:  time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC),
		Repository: contract.Repository{Path: "/repo", Commit: "rev2", SourceRevision: "rev2", DirtyPolicy: contract.DirtyRefuse},
		Harness:    contract.HarnessInfo{Name: "fake", Version: "1"},
		Tasks: []contract.TaskState{
			{ID: "verification", Status: contract.TaskFailed, Error: "1 finding not verified"},
			{ID: "report", Status: contract.TaskSucceeded, DependsOn: []string{"verification"}},
		},
		Verifies: &contract.VerifyTarget{
			Run: auditID, SourceRevision: "rev1", SnapshotDigest: "digest1",
			Findings: []string{"ARCH-001", "CORR-001", "CORR-002"}, FindingsSHA256: "sha",
		},
	}
	must(t, run.WriteJSON(storage.ManifestFile, m))
	must(t, run.WriteJSON(storage.FindingsFile, &contract.FindingSet{
		Header: contract.NewHeader(contract.KindFindings, verifyID),
		Findings: []contract.Finding{
			finding("ARCH-001", arch, contract.StateConfirmed),
			finding("CORR-001", corr[0], contract.StateSupportedRisk),
			finding("CORR-002", corr[1], contract.StateConfirmed),
		},
	}))
	if !writeRecords {
		return run
	}
	fixed := decision(contract.VerdictReject)
	fixed.AdditionalEvidence = []contract.Evidence{{
		Kind: contract.EvidenceSource, Description: "the new guard", Path: "stats.go", StartLine: 5, EndLine: 5,
		Excerpt: "if len(xs) == 0 { return 0, ErrEmpty }",
	}}
	must(t, run.WriteJSON(storage.VerificationFile, &contract.VerificationSet{
		Header: contract.NewHeader(contract.KindVerification, verifyID),
		Records: []contract.VerificationRecord{
			{
				FindingID: "ARCH-001", OriginalState: contract.StateConfirmed,
				// The validator raised the severity at the audit.
				Severity: contract.SeverityCritical, Confidence: contract.ConfidenceHigh,
				Relocations: []contract.Relocation{{EvidenceCheck: contract.EvidenceCheck{
					Index: 0, Status: contract.CheckMismatch, Detail: "cited lines changed"}}},
				Validator: "pruv", ValidatorVersion: "3", Decision: fixed,
				AdditionalChecks: []contract.Relocation{{EvidenceCheck: contract.EvidenceCheck{
					Index: 0, Status: contract.CheckVerified, Detail: "matches"}}},
				AdditionalPriorMatches: []int{0},
				Outcome:                contract.OutcomeFixed, OutcomeReason: "cited code changed and new code was added",
			},
			{
				FindingID: "CORR-001", OriginalState: contract.StateSupportedRisk,
				Severity: contract.SeverityHigh, Confidence: contract.ConfidenceMedium,
				Relocations: []contract.Relocation{{
					EvidenceCheck: contract.EvidenceCheck{Index: 0, Status: contract.CheckRelocated,
						Detail: "found once in moved.go", ActualStartLine: 10, ActualEndLine: 10},
					ActualPath: "moved.go",
				}},
				Validator: "pruv", ValidatorVersion: "3", Decision: decision(contract.VerdictAccept),
				Outcome: contract.OutcomeStillPresent, OutcomeReason: "validator says still present; evidence relocated",
			},
		},
	}))
	return run
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoadVerifyRun(t *testing.T) {
	d, err := Load(verifyRun(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Missing) != 0 || d.Discovery != nil || d.Plan != nil {
		t.Errorf("verify run must not look for audit artifacts: missing %v", d.Missing)
	}
	var ids []string
	for _, it := range d.Items {
		ids = append(ids, it.ID)
		if it.Verification == nil || it.Record != nil {
			t.Errorf("%s: verification %v, validation %v", it.ID, it.Verification, it.Record)
		}
	}
	if want := []string{"ARCH-001", "CORR-001", "CORR-002"}; !slices.Equal(ids, want) {
		t.Fatalf("items = %v, want %v", ids, want)
	}
	arch, corr, missing := d.Items[0], d.Items[1], d.Items[2]
	if arch.Verification.Outcome != contract.OutcomeFixed || arch.State != contract.StateConfirmed {
		t.Errorf("ARCH-001 = %s, state %s", arch.Verification.Outcome, arch.State)
	}
	if arch.Severity != contract.SeverityCritical {
		t.Errorf("ARCH-001 severity = %s, want the record's critical", arch.Severity)
	}
	if corr.Confidence != contract.ConfidenceMedium {
		t.Errorf("CORR-001 confidence = %s, want the record's medium", corr.Confidence)
	}
	v := missing.Verification
	if v.Outcome != contract.OutcomeUnverified || v.OutcomeReason != "not checked" || v.Error == "" {
		t.Errorf("CORR-002 synthesised record = %+v", v)
	}
	if v.OriginalState != contract.StateConfirmed || missing.Severity != contract.SeverityHigh {
		t.Errorf("CORR-002 synthesised state %s, severity %s", v.OriginalState, missing.Severity)
	}
}

func TestLoadVerifyRunWithoutRecords(t *testing.T) {
	d, err := Load(verifyRun(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.Missing, []string{storage.VerificationFile}) {
		t.Errorf("missing = %v", d.Missing)
	}
	for _, it := range d.Items {
		if it.Verification.Outcome != contract.OutcomeUnverified {
			t.Errorf("%s = %s, want unverified", it.ID, it.Verification.Outcome)
		}
	}
	if len(d.Items) != 3 {
		t.Errorf("items = %d", len(d.Items))
	}
}

func TestLoadVerifyRunRejectsUnknownSelection(t *testing.T) {
	run := verifyRun(t, true)
	m, err := run.Manifest()
	must(t, err)
	m.Verifies.Findings = append(m.Verifies.Findings, "CORR-009")
	must(t, run.WriteJSON(storage.ManifestFile, m))
	if _, err := Load(run); err == nil || !strings.Contains(err.Error(), "CORR-009") {
		t.Fatalf("Load = %v, want an error naming CORR-009", err)
	}
}

func TestGenerateVerifyRun(t *testing.T) {
	run := verifyRun(t, true)
	now := time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
	meta, err := Generate(run, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.ToSlash(filepath.Join(storage.ReportDir, VerificationReport))
	if !slices.Equal(meta.Files, []string{rel}) {
		t.Errorf("files = %v, want only %s", meta.Files, rel)
	}
	entries, err := os.ReadDir(run.Path(storage.ReportDir))
	must(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"report.json", VerificationReport}; !slices.Equal(names, want) {
		t.Errorf("report dir = %v, want %v", names, want)
	}

	var stored contract.ReportMeta
	must(t, run.ReadJSON(storage.ReportMetaFile, &stored))
	if stored.VerifiesRun != auditID || !stored.Partial || stored.SourceRevision != "rev2" {
		t.Errorf("meta = verifies %q, partial %v, revision %q", stored.VerifiesRun, stored.Partial, stored.SourceRevision)
	}
	wantOutcomes := map[contract.VerifyOutcome]int{
		contract.OutcomeFixed: 1, contract.OutcomeStillPresent: 1, contract.OutcomeCannotTell: 0, contract.OutcomeUnverified: 1,
	}
	if !maps.Equal(stored.OutcomeCounts, wantOutcomes) {
		t.Errorf("outcome counts = %v, want %v", stored.OutcomeCounts, wantOutcomes)
	}
	wantStates := map[contract.ValidationState]int{contract.StateConfirmed: 2, contract.StateSupportedRisk: 1}
	if !maps.Equal(stored.StateCounts, wantStates) {
		t.Errorf("state counts = %v, want %v", stored.StateCounts, wantStates)
	}

	data, err := os.ReadFile(run.Path(rel))
	must(t, err)
	md := string(data)
	for _, want := range []string{
		"> **PARTIAL verify run.** This run did not complete.",
		"| Audited revision | `rev1` (run `" + auditID + "`) |",
		"| Verified revision | `rev2` |",
		"| Fixed | 1 |", "| Not verified | 1 |", "| **Findings selected** | 3 |",
		"Fixed · was a confirmed defect at the audit · critical severity",
		"- **Outcome reason:** cited code changed and new code was added",
		"`stats.go:4` → [mismatch]: the code",
		"(cited lines changed)",
		"`stats.go:5` [verified]: the new guard; absent from the audited snapshot",
		"**Pruv (pruv v3): reject**",
		"- **Rationale:** the loop bound now covers the last element",
		"`stats.go:6` → `moved.go:10` [relocated]",
		"Not verified · was a confirmed defect at the audit",
		"**Not verified:** no record in " + storage.VerificationFile,
		"The verify run is partial; results are incomplete.",
		"Task verification failed: 1 finding not verified.",
		"1 finding(s) were not verified",
		"Locations are shown as cited at the audit.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("verification.md lacks %q", want)
		}
	}
	for _, unwanted := range []string{"Execution evidence", "were not validated", "Scorecard"} {
		if strings.Contains(md, unwanted) {
			t.Errorf("verification.md has audit-only text %q", unwanted)
		}
	}
	// Fixed first, then still present, then not verified.
	a, c, u := strings.Index(md, "### ARCH-001"), strings.Index(md, "### CORR-001"), strings.Index(md, "### CORR-002")
	if a < 0 || a >= c || c >= u {
		t.Errorf("finding order: ARCH-001@%d CORR-001@%d CORR-002@%d", a, c, u)
	}
	if paths, err := Paths(run); err != nil || len(paths) != 1 {
		t.Errorf("Paths = %v, %v", paths, err)
	}
}

func TestLoadVerifyRunWithoutFindings(t *testing.T) {
	run := verifyRun(t, true)
	must(t, os.Remove(run.Path(storage.FindingsFile)))
	if _, err := Load(run); err == nil || !strings.Contains(err.Error(), storage.FindingsFile) {
		t.Fatalf("Load = %v, want an error naming %s", err, storage.FindingsFile)
	}
}

// TestGenerateVerifyRunEdgeCases covers execution evidence, a cannot_tell
// outcome, the identical-snapshot still_present (no decision), a failed
// validator, every prior-match branch and the dirty-include limitation.
func TestGenerateVerifyRunEdgeCases(t *testing.T) {
	run := verifyRun(t, false)
	m, err := run.Manifest()
	must(t, err)
	m.Status = contract.RunCompleted
	m.Tasks[0].Status, m.Tasks[0].Error = contract.TaskSucceeded, ""
	m.Repository.Dirty, m.Repository.DirtyPolicy = true, contract.DirtyInclude
	must(t, run.WriteJSON(storage.ManifestFile, m))

	var fs contract.FindingSet
	must(t, run.ReadJSON(storage.FindingsFile, &fs))
	c2 := &fs.Findings[2]
	c2.Evidence = append(c2.Evidence, contract.Evidence{
		Kind: contract.EvidenceExecution, Description: "the test failed", Command: "go test ./...", Result: "FAIL",
	})
	c2.EvidenceChecks = append(c2.EvidenceChecks, contract.EvidenceCheck{Index: 1, Status: contract.CheckNotVerifiable, Detail: "execution"})
	must(t, run.WriteJSON(storage.FindingsFile, &fs))

	unsure := decision(contract.VerdictReject)
	unsure.AdditionalEvidence = []contract.Evidence{
		{Kind: contract.EvidenceSource, Description: "first", Path: "a.go", StartLine: 1, EndLine: 1, Excerpt: "some excerpt long enough to search"},
		{Kind: contract.EvidenceSource, Description: "second", Path: "b.go", StartLine: 2, EndLine: 2, Excerpt: "another excerpt long enough to search"},
		{Kind: contract.EvidenceSource, Description: "third", Path: "c.go", StartLine: 3, EndLine: 3, Excerpt: "a third excerpt long enough to search"},
	}
	verified := []contract.Relocation{{EvidenceCheck: contract.EvidenceCheck{Index: 0, Status: contract.CheckVerified, Detail: "matches"}}}
	must(t, run.WriteJSON(storage.VerificationFile, &contract.VerificationSet{
		Header: contract.NewHeader(contract.KindVerification, verifyID),
		Records: []contract.VerificationRecord{
			{
				FindingID: "ARCH-001", OriginalState: contract.StateConfirmed,
				Severity: contract.SeverityHigh, Confidence: contract.ConfidenceHigh, Relocations: verified,
				Validator: "pruv", ValidatorVersion: "3", Decision: unsure,
				AdditionalChecks: []contract.Relocation{
					{EvidenceCheck: contract.EvidenceCheck{Index: 0, Status: contract.CheckVerified, Detail: "matches"}},
					{EvidenceCheck: contract.EvidenceCheck{Index: 1, Status: contract.CheckVerified, Detail: "matches"}},
					{EvidenceCheck: contract.EvidenceCheck{Index: 2, Status: contract.CheckMissingFile, Detail: "no c.go"}},
				},
				AdditionalPriorMatches: []int{-1, 2, 0},
				Outcome:                contract.OutcomeCannotTell, OutcomeReason: "rejected, but no cited code changed",
			},
			{
				FindingID: "CORR-001", OriginalState: contract.StateSupportedRisk,
				Severity: contract.SeverityHigh, Confidence: contract.ConfidenceHigh, Relocations: verified,
				Validator: "pruv", ValidatorVersion: "3",
				Outcome: contract.OutcomeStillPresent, OutcomeReason: "snapshot identical to the audited one",
			},
			{
				FindingID: "CORR-002", OriginalState: contract.StateConfirmed,
				Severity: contract.SeverityHigh, Confidence: contract.ConfidenceHigh, Relocations: verified,
				Validator: "pruv", ValidatorVersion: "3",
				Outcome: contract.OutcomeUnverified, OutcomeReason: "validator failed", Error: "pruv timed out",
			},
		},
	}))

	meta, err := Generate(run, time.Now)
	must(t, err)
	if meta.Partial {
		t.Error("completed verify run reported as partial")
	}
	data, err := os.ReadFile(run.Path(meta.Files[0]))
	must(t, err)
	md := string(data)
	for _, want := range []string{
		"| Cannot tell | 1 |", "| Still present | 1 |", "| Not verified | 1 |",
		"2. Execution: the test failed (`go test ./...`); not re-checked",
		"could not be searched in the audited snapshot",
		"found 2 time(s) in the audited snapshot",
		"3. `c.go:3` [missing_file]: third; absent from the audited snapshot",
		"**Not verified:** pruv timed out",
		"1 finding(s) were checked but the result is not actionable",
		"Execution evidence from the audit cannot be re-checked against the new snapshot.",
		"Uncommitted changes were included in the verified snapshot.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("verification.md lacks %q", want)
		}
	}
	if strings.Contains(md, "verify run.**") {
		t.Error("completed run has an incomplete-run banner")
	}
	start := strings.Index(md, "### CORR-001")
	section := md[start:]
	section = section[:strings.Index(section[4:], "###")+4]
	if strings.Contains(section, "**Pruv") || !strings.Contains(section, "snapshot identical to the audited one") {
		t.Errorf("identical-snapshot section:\n%s", section)
	}
}
