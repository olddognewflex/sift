package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/harness"
	"github.com/olddognewflex/sift/internal/report"
	"github.com/olddognewflex/sift/internal/storage"
	"github.com/olddognewflex/sift/internal/testutil"
)

var (
	accept      = testutil.Decision(contract.VerdictAccept, contract.ClassDefect)
	reject      = testutil.Decision(contract.VerdictReject, "")
	investigate = testutil.Decision(contract.VerdictInvestigate, "")
)

// monotonic returns a clock that advances one second per call, so run IDs
// (second resolution) sort in creation order.
func monotonic() func() time.Time {
	var mu sync.Mutex
	t := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(time.Second)
		return t
	}
}

// venv is an audited fixture repository ready to verify. The audit leaves
// ARCH-001 supported_risk and CORR-001 confirmed (TestAuditCompletes).
type venv struct {
	*env
	now   func() time.Time
	audit *Result
}

func newVerifyEnv(t *testing.T) *venv {
	t.Helper()
	v := &venv{env: newEnv(t), now: monotonic()}
	res, err := Run(context.Background(), Options{RepoPath: v.repo, Config: v.cfg, Harness: v.h, Store: v.store, SiftVersion: "test", Now: v.now})
	if err != nil || res.Manifest.Status != contract.RunCompleted {
		t.Fatalf("audit: %v, %v", err, res)
	}
	v.audit = res
	return v
}

// commit applies changes to the repository: content to write, or "" to
// delete the file.
func (v *venv) commit(t *testing.T, changes map[string]string) {
	t.Helper()
	for name, content := range changes {
		if content == "" {
			if err := os.Remove(filepath.Join(v.repo, name)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		testutil.WriteFiles(t, v.repo, map[string]string{name: content})
	}
	testutil.Git(t, v.repo, "add", "-A")
	testutil.Git(t, v.repo, "commit", "-q", "-m", "change")
}

func (v *venv) verify(t *testing.T, ctx context.Context, decisions map[string]contract.ValidationDecision, mods ...func(*VerifyOptions)) (*Result, error) {
	t.Helper()
	v.h.Responders["pruv"] = testutil.VerifyPruv(decisions)
	opts := VerifyOptions{Options: Options{Config: v.cfg, Harness: v.h, Store: v.store, SiftVersion: "test", Now: v.now}}
	for _, m := range mods {
		m(&opts)
	}
	return Verify(ctx, opts)
}

func (v *venv) mustVerify(t *testing.T, decisions map[string]contract.ValidationDecision, mods ...func(*VerifyOptions)) *Result {
	t.Helper()
	res, err := v.verify(t, context.Background(), decisions, mods...)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return res
}

func only(ids ...string) func(*VerifyOptions) {
	return func(o *VerifyOptions) { o.Findings = ids }
}

func records(t *testing.T, run *storage.Run) map[string]contract.VerificationRecord {
	t.Helper()
	var set contract.VerificationSet
	if err := run.ReadJSON(storage.VerificationFile, &set); err != nil {
		t.Fatal(err)
	}
	out := map[string]contract.VerificationRecord{}
	for _, r := range set.Records {
		out[r.FindingID] = r
	}
	return out
}

func wantOutcomes(t *testing.T, run *storage.Run, want map[string]contract.VerifyOutcome) map[string]contract.VerificationRecord {
	t.Helper()
	recs := records(t, run)
	for id, o := range want {
		if r := recs[id]; r.Outcome != o {
			t.Errorf("%s = %s (%s), want %s", id, r.Outcome, r.OutcomeReason, o)
		}
	}
	if len(recs) != len(want) {
		t.Errorf("records for %d findings, want %d", len(recs), len(want))
	}
	return recs
}

// The fix corrects the loop bound CORR-001 cites.
var fixedStats = strings.Replace(testutil.StatsSource, "i < len(xs)-1;", "i < len(xs);", 1)

func TestVerifyFixedAndStillPresent(t *testing.T) {
	v := newVerifyEnv(t)
	v.commit(t, map[string]string{"stats.go": fixedStats})
	calls := len(v.h.Calls())
	res := v.mustVerify(t, map[string]contract.ValidationDecision{"ARCH-001": accept, "CORR-001": reject})
	m := res.Manifest
	if m.Status != contract.RunCompleted {
		t.Fatalf("status = %s, warnings %v", m.Status, m.Warnings)
	}
	wantOutcomes(t, res.Run, map[string]contract.VerifyOutcome{"ARCH-001": contract.OutcomeStillPresent, "CORR-001": contract.OutcomeFixed})

	am := v.audit.Manifest
	raw, err := os.ReadFile(v.audit.Run.Path(storage.FindingsFile))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	want := contract.VerifyTarget{
		Run: v.audit.Run.ID, SourceRevision: am.Repository.SourceRevision, SnapshotDigest: am.Repository.SnapshotDigest,
		Findings: []string{"ARCH-001", "CORR-001"}, FindingsSHA256: hex.EncodeToString(sum[:]),
	}
	if m.Verifies == nil || !reflect.DeepEqual(*m.Verifies, want) {
		t.Errorf("verifies = %+v, want %+v", m.Verifies, want)
	}
	if m.Repository.SourceRevision == am.Repository.SourceRevision {
		t.Error("verify run must snapshot the new revision")
	}

	// The copied findings equal the originals.
	var orig, copied contract.FindingSet
	if err := v.audit.Run.ReadJSON(storage.FindingsFile, &orig); err != nil {
		t.Fatal(err)
	}
	if err := res.Run.ReadJSON(storage.FindingsFile, &copied); err != nil {
		t.Fatal(err)
	}
	var selected []contract.Finding
	for _, f := range orig.Findings {
		if f.ID == "ARCH-001" || f.ID == "CORR-001" {
			selected = append(selected, f)
		}
	}
	if !reflect.DeepEqual(copied.Findings, selected) {
		t.Error("copied findings differ from the originals")
	}

	// Only Pruv runs, once per finding, in the verify run's snapshot.
	newCalls := v.h.Calls()[calls:]
	if len(newCalls) != 2 {
		t.Fatalf("%d agent calls, want 2", len(newCalls))
	}
	for _, c := range newCalls {
		if c.Agent != "pruv" || c.WorkDir != res.Run.Path(storage.SourceDir) || !strings.Contains(c.Prompt, "# Task: verify finding ") {
			t.Errorf("call %s in %s", c.Agent, c.WorkDir)
		}
	}
	for _, id := range []string{"ARCH-001", "CORR-001"} {
		if !res.Run.Exists("executions/verification/" + id + "/attempt-1/result.json") {
			t.Errorf("no execution recorded for %s", id)
		}
	}
	// The task graph is verification → report; Pruv is archived at v3.
	if len(m.Tasks) != 2 || m.Tasks[0].ID != taskVerification || m.Tasks[1].ID != taskReport || !slices.Equal(m.Tasks[1].DependsOn, []string{taskVerification}) {
		t.Errorf("tasks = %+v", m.Tasks)
	}
	if len(m.Agents) != 1 || m.Agents[0].Name != "pruv" || m.Agents[0].Version != "3" || !res.Run.Exists("agents/pruv.md") {
		t.Errorf("agents = %+v", m.Agents)
	}
	if m.Cost == nil || m.Cost.Estimate.Kind != "verify" || m.Cost.Actual.PruvExecutions != 2 {
		t.Errorf("cost = %+v", m.Cost)
	}
	if !res.Run.Exists("report/" + report.VerificationReport) {
		t.Error("verification report missing")
	}
}

// hashTree maps every file under dir to its SHA-256.
func hashTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[p] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestVerifyLeavesOriginalUnchanged(t *testing.T) {
	v := newVerifyEnv(t)
	before := hashTree(t, v.audit.Run.Dir)
	v.mustVerify(t, nil)
	v.commit(t, map[string]string{"stats.go": fixedStats})
	res := v.mustVerify(t, map[string]contract.ValidationDecision{"ARCH-001": accept, "CORR-001": reject})
	if _, err := report.Generate(res.Run, v.now); err != nil {
		t.Fatal(err)
	}
	if after := hashTree(t, v.audit.Run.Dir); !reflect.DeepEqual(before, after) {
		t.Error("the audited run changed")
	}
}

func TestVerifyIdenticalSnapshotRunsNoAgent(t *testing.T) {
	v := newVerifyEnv(t)
	calls := len(v.h.Calls())
	res := v.mustVerify(t, nil)
	if n := len(v.h.Calls()) - calls; n != 0 {
		t.Errorf("%d agent calls, want 0", n)
	}
	if res.Manifest.Status != contract.RunCompleted {
		t.Errorf("status = %s", res.Manifest.Status)
	}
	recs := wantOutcomes(t, res.Run, map[string]contract.VerifyOutcome{"ARCH-001": contract.OutcomeStillPresent, "CORR-001": contract.OutcomeStillPresent})
	if r := recs["CORR-001"]; r.Decision != nil || r.OutcomeReason != identicalReason {
		t.Errorf("CORR-001 = %+v", r)
	}
}

func TestVerifyMovedFileIsStillPresent(t *testing.T) {
	v := newVerifyEnv(t)
	v.commit(t, map[string]string{"stats.go": "", "max.go": testutil.StatsSource})
	res := v.mustVerify(t, map[string]contract.ValidationDecision{"ARCH-001": accept, "CORR-001": accept})
	recs := wantOutcomes(t, res.Run, map[string]contract.VerifyOutcome{"ARCH-001": contract.OutcomeStillPresent, "CORR-001": contract.OutcomeStillPresent})
	rl := recs["CORR-001"].Relocations[0]
	if rl.Status != contract.CheckRelocated || rl.ActualPath != "max.go" || rl.ActualStartLine != 6 {
		t.Errorf("relocation = %+v", rl)
	}
}

func TestVerifyDeletedFile(t *testing.T) {
	v := newVerifyEnv(t)
	v.commit(t, map[string]string{"stats.go": ""})
	res := v.mustVerify(t, map[string]contract.ValidationDecision{"ARCH-001": investigate, "CORR-001": accept})
	recs := wantOutcomes(t, res.Run, map[string]contract.VerifyOutcome{"ARCH-001": contract.OutcomeCannotTell, "CORR-001": contract.OutcomeCannotTell})
	if s := recs["CORR-001"].Relocations[0].Status; s != contract.CheckMissingFile {
		t.Errorf("relocation = %s", s)
	}
	res = v.mustVerify(t, map[string]contract.ValidationDecision{"ARCH-001": reject}, only("ARCH-001"))
	wantOutcomes(t, res.Run, map[string]contract.VerifyOutcome{"ARCH-001": contract.OutcomeFixed})
}

func TestVerifyRejectWithoutChangedCode(t *testing.T) {
	v := newVerifyEnv(t)
	v.commit(t, map[string]string{"README.md": "stats\n"})
	cites := reject
	cites.AdditionalEvidence = []contract.Evidence{{Kind: contract.EvidenceSource, Description: "documented",
		Path: "stats.go", StartLine: 3, EndLine: 3, Excerpt: "// Max returns the largest value."}}
	res := v.mustVerify(t, map[string]contract.ValidationDecision{"ARCH-001": reject, "CORR-001": cites})
	recs := wantOutcomes(t, res.Run, map[string]contract.VerifyOutcome{"ARCH-001": contract.OutcomeCannotTell, "CORR-001": contract.OutcomeCannotTell})
	r := recs["CORR-001"]
	if len(r.AdditionalPriorMatches) != 1 || r.AdditionalPriorMatches[0] < 1 || !strings.Contains(r.OutcomeReason, "already existed") {
		t.Errorf("CORR-001 prior matches %v, reason %q", r.AdditionalPriorMatches, r.OutcomeReason)
	}
	if !strings.Contains(recs["ARCH-001"].OutcomeReason, "disagrees") {
		t.Errorf("ARCH-001 reason %q", recs["ARCH-001"].OutcomeReason)
	}
}

func TestVerifyPruvFailureIsUnverified(t *testing.T) {
	v := newVerifyEnv(t)
	v.commit(t, map[string]string{"stats.go": fixedStats})
	res, err := v.verify(t, context.Background(), map[string]contract.ValidationDecision{"ARCH-001": accept})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Status != contract.RunPartial || res.Manifest.Task(taskVerification).Status != contract.TaskFailed {
		t.Errorf("status = %s, task %s", res.Manifest.Status, res.Manifest.Task(taskVerification).Status)
	}
	recs := wantOutcomes(t, res.Run, map[string]contract.VerifyOutcome{"ARCH-001": contract.OutcomeStillPresent, "CORR-001": contract.OutcomeUnverified})
	if recs["CORR-001"].Error == "" {
		t.Error("unverified record must say why")
	}

	res, err = v.verify(t, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Status != contract.RunFailed {
		t.Errorf("all unverified: status = %s, want failed", res.Manifest.Status)
	}
}

func TestVerifyInterruptedKeepsRecords(t *testing.T) {
	v := newVerifyEnv(t)
	v.cfg.Audit.MaxParallelAgents = 1
	v.commit(t, map[string]string{"stats.go": fixedStats})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Findings are verified in ID order. ARCH-001's decision arrives and
	// the run is interrupted before CORR-001 starts.
	answer := testutil.VerifyPruv(map[string]contract.ValidationDecision{"ARCH-001": accept, "CORR-001": reject})
	v.h.Responders["pruv"] = func(req harness.Request, n int) (json.RawMessage, error) {
		out, err := answer(req, n)
		if testutil.FindingID(req) == "ARCH-001" {
			cancel()
		}
		return out, err
	}
	opts := VerifyOptions{Options: Options{Config: v.cfg, Harness: v.h, Store: v.store, SiftVersion: "test", Now: v.now}}
	res, err := Verify(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Status != contract.RunInterrupted {
		t.Fatalf("status = %s", res.Manifest.Status)
	}
	recs := records(t, res.Run)
	if len(recs) != 1 || recs["ARCH-001"].Outcome != contract.OutcomeStillPresent {
		t.Fatalf("records = %+v, want only ARCH-001", recs)
	}
	data, err := report.Load(res.Run)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range data.Items {
		if it.ID == "CORR-001" && (it.Verification.Outcome != contract.OutcomeUnverified || it.Verification.OutcomeReason != "not checked") {
			t.Errorf("CORR-001 = %+v, want unverified (not checked)", it.Verification)
		}
	}
	if arts := res.Manifest.Task(taskVerification).Artifacts; !slices.Contains(arts, storage.VerificationFile) {
		t.Errorf("artifacts = %v", arts)
	}
	if !res.Run.Exists("report/" + report.VerificationReport) {
		t.Error("an interrupted verify run still gets a report")
	}
}

func TestVerifySelectionIsSortedAndUnique(t *testing.T) {
	v := newVerifyEnv(t)
	res := v.mustVerify(t, nil, only("CORR-001", "ARCH-001", "CORR-001"))
	want := []string{"ARCH-001", "CORR-001"}
	if got := res.Manifest.Verifies.Findings; !slices.Equal(got, want) {
		t.Errorf("verifies.findings = %v, want %v", got, want)
	}
	var copied contract.FindingSet
	if err := res.Run.ReadJSON(storage.FindingsFile, &copied); err != nil {
		t.Fatal(err)
	}
	if len(copied.Findings) != 2 || copied.Findings[0].ID != "ARCH-001" {
		t.Errorf("copied %d findings", len(copied.Findings))
	}
}

func TestVerifyDefaultsToLatestAudit(t *testing.T) {
	v := newVerifyEnv(t)
	first := v.mustVerify(t, nil)
	// The newest run is now a verify run; the default still targets the audit.
	second := v.mustVerify(t, nil)
	for _, r := range []*Result{first, second} {
		if r.Manifest.Verifies.Run != v.audit.Run.ID {
			t.Errorf("run %s verifies %s, want %s", r.Run.ID, r.Manifest.Verifies.Run, v.audit.Run.ID)
		}
	}
}

func TestVerifyRefusals(t *testing.T) {
	setManifest := func(t *testing.T, v *venv, fn func(*contract.Manifest)) {
		m, err := v.audit.Run.Manifest()
		if err != nil {
			t.Fatal(err)
		}
		fn(m)
		if err := v.audit.Run.WriteJSON(storage.ManifestFile, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, v *venv) []func(*VerifyOptions)
		want  string
	}{
		{"missing run", func(*testing.T, *venv) []func(*VerifyOptions) {
			return []func(*VerifyOptions){func(o *VerifyOptions) { o.RunID = "20261120T000000Z-ffffff" }}
		}, "not found"},
		{"verify run", func(t *testing.T, v *venv) []func(*VerifyOptions) {
			id := v.mustVerify(t, nil).Run.ID
			return []func(*VerifyOptions){func(o *VerifyOptions) { o.RunID = id }}
		}, "is a verify run"},
		{"still running", func(t *testing.T, v *venv) []func(*VerifyOptions) {
			host, _ := os.Hostname()
			setManifest(t, v, func(m *contract.Manifest) { m.Status, m.Host, m.PID = contract.RunRunning, host, os.Getpid() })
			return nil
		}, "still running"},
		{"no findings", func(t *testing.T, v *venv) []func(*VerifyOptions) {
			if err := os.Remove(v.audit.Run.Path(storage.FindingsFile)); err != nil {
				t.Fatal(err)
			}
			return nil
		}, "validation did not finish"},
		{"no snapshot", func(t *testing.T, v *venv) []func(*VerifyOptions) {
			if err := os.RemoveAll(v.audit.Run.Path(storage.SourceDir)); err != nil {
				t.Fatal(err)
			}
			return nil
		}, "snapshot"},
		{"unknown finding", func(*testing.T, *venv) []func(*VerifyOptions) {
			return []func(*VerifyOptions){only("CORR-009")}
		}, "has no finding CORR-009"},
		{"not established", func(*testing.T, *venv) []func(*VerifyOptions) {
			return []func(*VerifyOptions){only("CORR-001", "CORR-003")}
		}, "CORR-003 is rejected; only confirmed or supported_risk findings can be verified"},
		{"nothing selected", func(t *testing.T, v *venv) []func(*VerifyOptions) {
			var set contract.FindingSet
			if err := v.audit.Run.ReadJSON(storage.FindingsFile, &set); err != nil {
				t.Fatal(err)
			}
			for i := range set.Findings {
				set.Findings[i].State = contract.StateRejected
			}
			if err := v.audit.Run.WriteJSON(storage.FindingsFile, &set); err != nil {
				t.Fatal(err)
			}
			return nil
		}, "no confirmed or supported_risk findings"},
		{"missing commit", func(t *testing.T, v *venv) []func(*VerifyOptions) {
			setManifest(t, v, func(m *contract.Manifest) { m.Repository.Commit = strings.Repeat("0", 40) })
			return nil
		}, "does not contain commit"},
		{"dirty tree", func(t *testing.T, v *venv) []func(*VerifyOptions) {
			testutil.WriteFiles(t, v.repo, map[string]string{"stats.go": fixedStats})
			return nil
		}, "uncommitted"},
		{"pre-v3 pruv", func(t *testing.T, v *venv) []func(*VerifyOptions) {
			dir := t.TempDir()
			testutil.WriteFiles(t, dir, map[string]string{"pruv.md": "---\nname: pruv\nversion: 2\n---\nBe skeptical.\n"})
			v.cfg.Agents.Dir = dir
			return nil
		}, "verify needs Pruv version 3 or later"},
		{"preflight declined", func(t *testing.T, v *venv) []func(*VerifyOptions) {
			v.cfg.Cost.BudgetUSD = 0.001
			return []func(*VerifyOptions){func(o *VerifyOptions) {
				o.Preflight = func(context.Context, contract.CostEstimate) (contract.CostApproval, error) {
					return "", errors.New("declined by test")
				}
			}}
		}, "declined by test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := newVerifyEnv(t)
			mods := tc.setup(t, v)
			before, err := v.store.List()
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.verify(t, context.Background(), map[string]contract.ValidationDecision{"ARCH-001": accept, "CORR-001": accept}, mods...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if after, _ := v.store.List(); !slices.Equal(before, after) {
				t.Errorf("runs %v → %v: a refusal must not create a run", before, after)
			}
			entries, _ := os.ReadDir(v.store.Root)
			if len(entries) != len(before) {
				t.Errorf("runs directory has %d entries, want %d", len(entries), len(before))
			}
		})
	}
}
