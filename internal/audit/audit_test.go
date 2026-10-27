package audit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/harness"
	"github.com/olddognewflex/sift/internal/harness/fake"
	"github.com/olddognewflex/sift/internal/report"
	"github.com/olddognewflex/sift/internal/source"
	"github.com/olddognewflex/sift/internal/storage"
	"github.com/olddognewflex/sift/internal/testutil"
)

type env struct {
	repo  string
	store storage.Store
	h     *fake.Harness
	cfg   config.Config
}

func newEnv(t *testing.T) *env {
	cfg := config.Default()
	cfg.Storage.RunsDir = filepath.Join(t.TempDir(), "runs")
	return &env{repo: testutil.StatsRepo(t), store: storage.Store{Root: cfg.Storage.RunsDir}, h: testutil.Harness(), cfg: cfg}
}

func (e *env) run(t *testing.T, ctx context.Context) (*Result, error) {
	t.Helper()
	return Run(ctx, Options{RepoPath: e.repo, Config: e.cfg, Harness: e.h, Store: e.store, SiftVersion: "test"})
}

func states(t *testing.T, run *storage.Run) map[string]contract.ValidationState {
	t.Helper()
	data, err := report.Load(run)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]contract.ValidationState{}
	for _, it := range data.Items {
		out[it.ID] = it.State
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAuditCompletes(t *testing.T) {
	e := newEnv(t)
	before, _ := source.Fingerprint(context.Background(), e.repo)
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Status != contract.RunCompleted {
		t.Fatalf("status = %s, warnings %v", m.Status, m.Warnings)
	}
	for _, task := range m.Tasks {
		if task.Status != contract.TaskSucceeded {
			t.Errorf("task %s = %s (%s)", task.ID, task.Status, task.Error)
		}
	}

	want := map[string]contract.ValidationState{
		"ARCH-001": contract.StateSupportedRisk,
		"CORR-001": contract.StateConfirmed,
		"CORR-002": contract.StateNeedsInvestigation, // accepted by Pruv, but evidence fabricated
		"CORR-003": contract.StateRejected,
	}
	got := states(t, res.Run)
	for id, s := range want {
		if got[id] != s {
			t.Errorf("%s = %s, want %s", id, got[id], s)
		}
	}

	// Candidates are preserved unmodified; final state lives in decisions.
	var cands contract.FindingSet
	if err := res.Run.ReadJSON(storage.CandidatesFile, &cands); err != nil {
		t.Fatal(err)
	}
	for _, f := range cands.Findings {
		if f.State != contract.StateCandidate || f.SourceRevision != m.Repository.Commit {
			t.Errorf("candidate %s: state %s rev %s", f.ID, f.State, f.SourceRevision)
		}
	}
	var final contract.FindingSet
	if err := res.Run.ReadJSON(storage.FindingsFile, &final); err != nil {
		t.Fatal(err)
	}
	if len(final.Findings) != 4 {
		t.Errorf("final findings = %d", len(final.Findings))
	}

	// Reproducibility metadata.
	if m.Repository.Commit == "" || m.Repository.SnapshotDigest == "" || m.Harness.Version != "1.0.0" || len(m.Agents) != 4 || len(m.Config) == 0 {
		t.Errorf("manifest missing metadata: %+v", m)
	}
	if !m.Integrity.RepositoryUnchanged {
		t.Errorf("integrity: %+v", m.Integrity)
	}
	for _, a := range m.Agents {
		if !res.Run.Exists("agents/" + a.Name + ".md") {
			t.Errorf("agent definition %s not archived", a.Name)
		}
	}
	if n := len(m.Task("validation").Executions); n != 4 {
		t.Errorf("validation executions = %d, want 4", n)
	}
	var exec contract.ExecutionResult
	if err := res.Run.ReadJSON(m.Task("discovery").Executions[0], &exec); err != nil || !exec.Succeeded || exec.SessionID != "fake-session" {
		t.Errorf("execution record = %+v, %v", exec, err)
	}

	// Agents ran against the snapshot, never the repository.
	snapshot := res.Run.Path(storage.SourceDir)
	for _, c := range e.h.Calls() {
		if c.WorkDir != snapshot {
			t.Errorf("%s ran in %s", c.Agent, c.WorkDir)
		}
		if c.Agent == "correctness" && !strings.Contains(c.Prompt, "A tiny statistics library.") {
			t.Error("reviewer prompt lacks discovery context")
		}
		if c.Agent == "pruv" && !strings.Contains(c.Prompt, "mechanical evidence checks") {
			t.Error("pruv prompt lacks evidence checks")
		}
	}
	after, _ := source.Fingerprint(context.Background(), e.repo)
	if before != after {
		t.Error("the audit modified the repository")
	}

	// Reports.
	for _, f := range []string{report.ExecutiveSummary, report.ArchitectureAudit, report.FindingsInventory, report.RemediationPlan} {
		if !res.Run.Exists("report/" + f) {
			t.Errorf("report %s missing", f)
		}
	}
	plan := readFile(t, res.Run.Path("report/"+report.RemediationPlan))
	if !strings.Contains(plan, "CORR-001") || strings.Contains(plan, "## 3.") {
		t.Errorf("remediation plan should list exactly the 2 accepted findings:\n%s", plan)
	}
	if !strings.Contains(plan, "Open investigations") || !strings.Contains(plan, "CORR-002") {
		t.Error("remediation plan should list CORR-002 as an open investigation")
	}
	audit := readFile(t, res.Run.Path("report/"+report.ArchitectureAudit))
	for _, s := range []string{"## Rejected findings", "Max is slow", "## Architectural strengths", "Small API", "## Review coverage", "Unreviewed scope"} {
		if !strings.Contains(audit, s) {
			t.Errorf("architecture audit missing %q", s)
		}
	}
	summary := readFile(t, res.Run.Path("report/"+report.ExecutiveSummary))
	if strings.Contains(summary, "PARTIAL") || !strings.Contains(summary, "No scorecard is produced") {
		t.Error("executive summary content unexpected")
	}
}

func TestReviewerFailureMakesRunPartial(t *testing.T) {
	e := newEnv(t)
	e.h.Responders["architecture"] = fake.Fail(harness.ErrProcess, "crashed")
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Status != contract.RunPartial {
		t.Fatalf("status = %s", m.Status)
	}
	if task := m.Task("review-architecture"); task.Status != contract.TaskFailed || len(task.Executions) != 2 {
		t.Errorf("architecture task = %s with %d attempts (process failures retry once)", task.Status, len(task.Executions))
	}
	got := states(t, res.Run)
	if _, ok := got["ARCH-001"]; ok || got["CORR-001"] != contract.StateConfirmed {
		t.Errorf("states = %v", got)
	}
	audit := readFile(t, res.Run.Path("report/"+report.ArchitectureAudit))
	if !strings.Contains(audit, "PARTIAL audit") || !strings.Contains(audit, "**Not reviewed.** Task review-architecture failed") {
		t.Errorf("report must mark the gap:\n%s", audit[:min(len(audit), 2000)])
	}
}

func TestDiscoveryFailureFailsRun(t *testing.T) {
	e := newEnv(t)
	e.h.Responders["discovery"] = fake.Fail(harness.ErrTimeout, "too slow")
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Status != contract.RunFailed {
		t.Fatalf("status = %s", m.Status)
	}
	if n := len(m.Task("discovery").Executions); n != 1 {
		t.Errorf("timeouts must not be retried; %d attempts", n)
	}
	for _, id := range []string{"plan", "review-architecture", "review-correctness", "validation"} {
		if s := m.Task(id).Status; s != contract.TaskSkipped {
			t.Errorf("%s = %s, want skipped", id, s)
		}
	}
	if m.Task("report").Status != contract.TaskSucceeded {
		t.Error("a failed run still gets a report")
	}
	summary := readFile(t, res.Run.Path("report/"+report.ExecutiveSummary))
	if !strings.Contains(summary, "FAILED audit") || !strings.Contains(summary, "Task discovery failed") {
		t.Errorf("summary:\n%s", summary)
	}
}

func TestInvalidOutputIsRetriedWithFeedback(t *testing.T) {
	e := newEnv(t)
	good, _ := json.Marshal(testutil.CorrectnessReview())
	e.h.Responders["correctness"] = func(_ harness.Request, call int) (json.RawMessage, error) {
		if call == 1 {
			return json.RawMessage(`{"findings":[{"title":"no evidence"}],"strengths":[],"reviewed_scope":[],"unreviewed_scope":[],"limitations":[]}`), nil
		}
		return good, nil
	}
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	task := res.Manifest.Task("review-correctness")
	if task.Status != contract.TaskSucceeded || len(task.Executions) != 2 {
		t.Fatalf("task = %s, %d attempts", task.Status, len(task.Executions))
	}
	var first contract.ExecutionResult
	if err := res.Run.ReadJSON(task.Executions[0], &first); err != nil || first.ErrorKind != string(harness.ErrInvalidOutput) {
		t.Fatalf("first attempt = %+v %v", first, err)
	}
	var retry string
	for _, c := range e.h.Calls() {
		if c.Agent == "correctness" {
			retry = c.Prompt
		}
	}
	if !strings.Contains(retry, "Previous attempt") || !strings.Contains(retry, "evidence") {
		t.Errorf("retry prompt lacks feedback:\n%s", retry)
	}
}

func TestInvalidOutputExhaustsRetries(t *testing.T) {
	e := newEnv(t)
	e.h.Responders["correctness"] = fake.JSON(map[string]any{"unexpected": true})
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Status != contract.RunPartial || res.Manifest.Task("review-correctness").Status != contract.TaskFailed {
		t.Fatalf("status = %s", res.Manifest.Status)
	}
}

func TestValidatorFailureLeavesFindingUnvalidated(t *testing.T) {
	e := newEnv(t)
	e.h.Responders["pruv"] = func(req harness.Request, n int) (json.RawMessage, error) {
		if testutil.FindingID(req) == "CORR-001" {
			return nil, &harness.Error{Kind: harness.ErrBudget, Msg: "out of money"}
		}
		return testutil.Pruv(req, n)
	}
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Status != contract.RunPartial {
		t.Fatalf("status = %s", res.Manifest.Status)
	}
	v := res.Manifest.Task("validation")
	if v.Status != contract.TaskFailed || !strings.Contains(v.Error, "1 of 4 findings could not be validated") {
		t.Errorf("validation = %s: %s", v.Status, v.Error)
	}
	got := states(t, res.Run)
	if got["CORR-001"] != contract.StateUnvalidated || got["ARCH-001"] != contract.StateSupportedRisk {
		t.Errorf("states = %v", got)
	}
	var decs contract.DecisionSet
	if err := res.Run.ReadJSON(storage.DecisionsFile, &decs); err != nil {
		t.Fatal(err)
	}
	for _, d := range decs.Decisions {
		if d.FindingID == "CORR-001" && (d.Error == "" || d.Decision != nil) {
			t.Errorf("failed validation record = %+v", d)
		}
	}
}

func TestInterruptedRunKeepsPartialResults(t *testing.T) {
	e := newEnv(t)
	e.cfg.Audit.MaxParallelAgents = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.h.Before = func(ctx context.Context, req harness.Request) error {
		if req.Agent == "correctness" {
			cancel()
			return &harness.Error{Kind: harness.ErrCanceled, Msg: "canceled", Err: ctx.Err()}
		}
		return nil
	}
	res, err := e.run(t, ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Status != contract.RunInterrupted {
		t.Fatalf("status = %s", m.Status)
	}
	if m.Task("discovery").Status != contract.TaskSucceeded || !res.Run.Exists(storage.DiscoveryFile) {
		t.Error("completed work must be preserved")
	}
	if s := m.Task("review-correctness").Status; s != contract.TaskInterrupted {
		t.Errorf("correctness = %s", s)
	}
	if n := len(m.Task("review-correctness").Executions); n != 1 {
		t.Errorf("canceled executions must not be retried; %d attempts", n)
	}
	if !strings.Contains(readFile(t, res.Run.Path("report/"+report.ExecutiveSummary)), "INTERRUPTED audit") {
		t.Error("report must be marked interrupted")
	}
}

func TestDirtyTreeRefusedBeforeRunCreated(t *testing.T) {
	e := newEnv(t)
	testutil.WriteFiles(t, e.repo, map[string]string{"stats.go": "package stats\n"})
	_, err := e.run(t, context.Background())
	if !errors.Is(err, source.ErrDirty) {
		t.Fatalf("err = %v", err)
	}
	if ids, _ := e.store.List(); len(ids) != 0 {
		t.Fatalf("runs created: %v", ids)
	}
}

func TestDirtyIncludeRecordsDiff(t *testing.T) {
	e := newEnv(t)
	testutil.WriteFiles(t, e.repo, map[string]string{"extra.go": "package stats\n"})
	e.cfg.Audit.DirtyPolicy = contract.DirtyInclude
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := res.Manifest.Repository
	if !r.Dirty || !strings.Contains(r.SourceRevision, "+worktree.") || !res.Run.Exists(storage.SourceDiffFile) || !res.Run.Exists("source/extra.go") {
		t.Fatalf("repository = %+v", r)
	}
	var cands contract.FindingSet
	_ = res.Run.ReadJSON(storage.CandidatesFile, &cands)
	if cands.Findings[0].SourceRevision != r.SourceRevision {
		t.Error("findings must carry the dirty source revision")
	}
}

func TestRunsDirInsideRepoRefused(t *testing.T) {
	e := newEnv(t)
	e.store.Root = filepath.Join(e.repo, ".sift-runs")
	if _, err := e.run(t, context.Background()); err == nil || !strings.Contains(err.Error(), "inside the audited repository") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnusableHarnessRefused(t *testing.T) {
	e := newEnv(t)
	e.h.ProbeErr = &harness.Error{Kind: harness.ErrUnsupported, Msg: "too old"}
	if _, err := e.run(t, context.Background()); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("err = %v", err)
	}
	if ids, _ := e.store.List(); len(ids) != 0 {
		t.Fatalf("runs created: %v", ids)
	}
}

func TestExcessFindingsAreTruncated(t *testing.T) {
	e := newEnv(t)
	e.cfg.Audit.MaxFindingsPerReviewer = 1
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := states(t, res.Run)
	if len(got) != 2 {
		t.Fatalf("findings = %v", got)
	}
	if !strings.Contains(strings.Join(res.Manifest.Warnings, "\n"), "only the first 1 were kept") {
		t.Errorf("warnings = %v", res.Manifest.Warnings)
	}
}

func TestReportRegeneratesFromArtifacts(t *testing.T) {
	e := newEnv(t)
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := readFile(t, res.Run.Path("report/"+report.FindingsInventory))
	if err := os.RemoveAll(res.Run.Path(storage.ReportDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := report.Paths(res.Run); err == nil {
		t.Fatal("Paths must fail when reports are missing")
	}
	meta, err := report.Generate(res.Run, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, res.Run.Path("report/"+report.FindingsInventory)); got != original {
		t.Error("regenerated inventory differs from the original")
	}
	if meta.StateCounts[contract.StateConfirmed] != 1 || meta.Partial {
		t.Errorf("meta = %+v", meta)
	}
}
