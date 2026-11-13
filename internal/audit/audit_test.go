package audit

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/cost"
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

	// Candidates are preserved unmodified; final state lives in findings.json.
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

func TestValidationRespectsParallelism(t *testing.T) {
	e := newEnv(t)
	e.cfg.Audit.MaxParallelAgents = 2
	var running, peak atomic.Int32
	e.h.Before = func(ctx context.Context, req harness.Request) error {
		if req.Agent != "pruv" {
			return nil
		}
		n := running.Add(1)
		defer running.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	if _, err := e.run(t, context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := peak.Load(); p != 2 {
		t.Errorf("peak concurrent validations = %d, want 2", p)
	}
}

func TestValidatorPanicLeavesFindingUnvalidated(t *testing.T) {
	e := newEnv(t)
	e.h.Responders["pruv"] = func(req harness.Request, n int) (json.RawMessage, error) {
		if testutil.FindingID(req) == "CORR-001" {
			panic("boom")
		}
		return testutil.Pruv(req, n)
	}
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v := res.Manifest.Task("validation")
	if v.Status != contract.TaskFailed || !strings.Contains(v.Error, "1 of 4 findings could not be validated") || !strings.Contains(v.Error, "panicked") {
		t.Errorf("validation = %s: %s", v.Status, v.Error)
	}
	got := states(t, res.Run)
	if got["CORR-001"] != contract.StateUnvalidated || got["ARCH-001"] != contract.StateSupportedRisk || got["CORR-003"] != contract.StateRejected {
		t.Errorf("states = %v", got)
	}
}

func TestInterruptedValidationKeepsDecisions(t *testing.T) {
	e := newEnv(t)
	e.cfg.Audit.MaxParallelAgents = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.h.Before = func(ctx context.Context, req harness.Request) error {
		if req.Agent == "pruv" && testutil.FindingID(req) == "CORR-002" {
			cancel()
			return &harness.Error{Kind: harness.ErrCanceled, Msg: "canceled", Err: ctx.Err()}
		}
		return nil
	}
	res, err := e.run(t, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Status != contract.RunInterrupted {
		t.Fatalf("status = %s", res.Manifest.Status)
	}
	if s := res.Manifest.Task("validation").Status; s != contract.TaskInterrupted {
		t.Errorf("validation = %s", s)
	}
	// Validation never finished, so states come from the decisions so far.
	if res.Run.Exists(storage.FindingsFile) {
		t.Fatal("interrupted validation must not write final findings")
	}
	// Findings are validated in order: ARCH-001, CORR-001, CORR-002, ...
	got := states(t, res.Run)
	want := map[string]contract.ValidationState{
		"ARCH-001": contract.StateSupportedRisk,
		"CORR-001": contract.StateConfirmed,
		"CORR-002": contract.StateUnvalidated,
		"CORR-003": contract.StateUnvalidated,
	}
	for id, s := range want {
		if got[id] != s {
			t.Errorf("%s = %s, want %s", id, got[id], s)
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

// cancelOnceAgentsWritten returns a clock that cancels the run once
// initialize has written the agent files, i.e. just before the snapshot
// export starts.
func cancelOnceAgentsWritten(runsDir string, cancel context.CancelFunc) func() time.Time {
	return func() time.Time {
		if m, _ := filepath.Glob(filepath.Join(runsDir, "*", storage.AgentsDir, "*")); len(m) > 0 {
			cancel()
		}
		return time.Now()
	}
}

func TestInterruptDuringExportMarksRunInterrupted(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := Run(ctx, Options{RepoPath: e.repo, Config: e.cfg, Harness: e.h, Store: e.store, SiftVersion: "test",
		Now: cancelOnceAgentsWritten(e.cfg.Storage.RunsDir, cancel)})
	if err == nil || res == nil {
		t.Fatalf("want a run and an export error, got %v, %v", res, err)
	}
	m := res.Manifest
	if m.Status != contract.RunInterrupted {
		t.Fatalf("status = %s, want %s", m.Status, contract.RunInterrupted)
	}
	if m.FinishedAt == nil {
		t.Error("FinishedAt must be set")
	}
	if len(m.Warnings) == 0 || !strings.HasPrefix(m.Warnings[len(m.Warnings)-1], "run interrupted: ") {
		t.Errorf("warnings = %q", m.Warnings)
	}
	var saved contract.Manifest
	if err := res.Run.ReadJSON(storage.ManifestFile, &saved); err != nil || saved.Status != contract.RunInterrupted {
		t.Errorf("saved status = %s, %v", saved.Status, err)
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

func TestReportReadsFinalFindings(t *testing.T) {
	e := newEnv(t)
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var final contract.FindingSet
	if err := res.Run.ReadJSON(storage.FindingsFile, &final); err != nil {
		t.Fatal(err)
	}
	// Change one final state so that it no longer matches what the
	// candidates and decisions would produce. The report must show it.
	var changed string
	for i, f := range final.Findings {
		if f.State == contract.StateConfirmed {
			changed = f.ID
			final.Findings[i].State = contract.StateRejected
			final.Findings[i].StateReason = "edited for test"
			break
		}
	}
	if changed == "" {
		t.Fatal("no confirmed finding to edit")
	}
	if err := res.Run.WriteJSON(storage.FindingsFile, &final); err != nil {
		t.Fatal(err)
	}
	if got := states(t, res.Run)[changed]; got != contract.StateRejected {
		t.Errorf("%s = %s, want the state from findings.json (%s)", changed, got, contract.StateRejected)
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

// warningHarness adds a warning to the Meta of every execution of one agent,
// successful or failed.
type warningHarness struct {
	*fake.Harness
	agent, msg string
}

func (h warningHarness) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	res, err := h.Harness.Run(ctx, req)
	if req.Agent != h.agent {
		return res, err
	}
	var he *harness.Error
	if errors.As(err, &he) {
		he.Meta.Warnings = append(he.Meta.Warnings, h.msg)
	}
	res.Meta.Warnings = append(res.Meta.Warnings, h.msg)
	return res, err
}

func TestHarnessWarningsAreRecorded(t *testing.T) {
	const msg = "a leftover process held its output open"
	for _, tc := range []struct {
		name string
		fail bool
		want int // warnings: one per attempt
	}{
		{"success", false, 1},
		{"failure", true, 2}, // process failures retry once
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if tc.fail {
				e.h.Responders["architecture"] = fake.Fail(harness.ErrProcess, "crashed")
			}
			h := warningHarness{Harness: e.h, agent: "architecture", msg: msg}
			res, err := Run(context.Background(), Options{RepoPath: e.repo, Config: e.cfg, Harness: h, Store: e.store, SiftVersion: "test"})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, w := range res.Manifest.Warnings {
				if strings.Contains(w, msg) {
					got = append(got, w)
				}
			}
			if len(got) != tc.want {
				t.Fatalf("harness warnings = %q, want %d (all warnings %q)", got, tc.want, res.Manifest.Warnings)
			}
			for _, w := range got {
				if !strings.HasPrefix(w, "architecture (") || !strings.Contains(w, "(see executions/") {
					t.Errorf("warning %q should name the agent and its execution directory", w)
				}
			}
		})
	}
}

func TestManifestRecordsCostEstimateAndActual(t *testing.T) {
	e := newEnv(t)
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	c := m.Cost
	if c == nil {
		t.Fatal("manifest has no cost record")
	}
	// The fake harness does not advertise budget-limit, so nothing is capped.
	want := cost.ForAudit(e.cfg, m.Repository.SnapshotFiles, false)
	if !reflect.DeepEqual(c.Estimate, want) {
		t.Errorf("estimate = %+v\nwant %+v", c.Estimate, want)
	}
	if c.Estimate.Files != m.Repository.SnapshotFiles || c.Estimate.Reviewers != 2 || c.Estimate.PruvCallsLow != 8 ||
		c.Estimate.PruvCallsHigh != 16 || c.Estimate.HarnessEnforcesBudget {
		t.Errorf("estimate inputs = %+v", c.Estimate)
	}
	if c.BudgetUSD != e.cfg.Cost.BudgetUSD || c.Approval != contract.ApprovalWithinBudget {
		t.Errorf("budget %v, approval %s", c.BudgetUSD, c.Approval)
	}
	// 1 discovery + 2 reviews + 4 Pruv executions at the fake's $0.01 each.
	a := c.Actual
	if a.Executions != 7 || a.ReportedExecutions != 7 || a.PruvExecutions != 4 || a.Candidates != 4 || math.Abs(a.USD-0.07) > 1e-9 {
		t.Errorf("actual = %+v", a)
	}
	var saved contract.Manifest
	if err := res.Run.ReadJSON(storage.ManifestFile, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Cost == nil || !reflect.DeepEqual(*saved.Cost, *c) {
		t.Errorf("saved cost = %+v, want %+v", saved.Cost, c)
	}
}

// overBudget sets a budget below the fixture's estimate (high ≈ $5.4).
func overBudget(e *env) { e.cfg.Cost.BudgetUSD = 1 }

func TestPreflightApprovalIsRecorded(t *testing.T) {
	for _, approval := range []contract.CostApproval{contract.ApprovalYesFlag, contract.ApprovalPrompt} {
		t.Run(string(approval), func(t *testing.T) {
			e := newEnv(t)
			overBudget(e)
			var seen contract.CostEstimate
			res, err := Run(context.Background(), Options{RepoPath: e.repo, Config: e.cfg, Harness: e.h, Store: e.store, SiftVersion: "test",
				Preflight: func(_ context.Context, est contract.CostEstimate) (contract.CostApproval, error) {
					if len(e.h.Calls()) != 0 {
						t.Error("preflight must run before any agent")
					}
					if ids, _ := e.store.List(); len(ids) != 0 {
						t.Errorf("preflight must run before the run is created: %v", ids)
					}
					seen = est
					return approval, nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			if c := res.Manifest.Cost; c.Approval != approval || c.BudgetUSD != 1 || !reflect.DeepEqual(c.Estimate, seen) {
				t.Errorf("cost = %+v, preflight saw %+v", c, seen)
			}
		})
	}
}

func TestEstimateIsLoggedForEveryRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget float64
	}{{"no budget", 0}, {"within", 100}, {"over", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.cfg.Cost.BudgetUSD = tc.budget
			var log strings.Builder
			var atPreflight string
			res, err := Run(context.Background(), Options{RepoPath: e.repo, Config: e.cfg, Harness: e.h, Store: e.store, SiftVersion: "test", Log: &log,
				Preflight: func(context.Context, contract.CostEstimate) (contract.CostApproval, error) {
					atPreflight = log.String()
					return contract.ApprovalYesFlag, nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			c := res.Manifest.Cost
			var want strings.Builder
			for _, l := range cost.Lines(c.Estimate, c.BudgetUSD) {
				want.WriteString("sift: " + l + "\n")
			}
			if !strings.HasPrefix(log.String(), want.String()) {
				t.Errorf("log should start with the estimate\n%s\ngot\n%s", want.String(), log.String())
			}
			if tc.budget == 1 && atPreflight != want.String() {
				t.Errorf("estimate must be logged before Preflight is asked; it had seen\n%s", atPreflight)
			}
		})
	}
}

func TestPreflightOnlyCalledOverBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget float64
		want   contract.CostApproval
	}{
		{"within budget", 100, contract.ApprovalWithinBudget},
		{"no budget", 0, contract.ApprovalNoBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.cfg.Cost.BudgetUSD = tc.budget
			res, err := Run(context.Background(), Options{RepoPath: e.repo, Config: e.cfg, Harness: e.h, Store: e.store, SiftVersion: "test",
				Preflight: func(context.Context, contract.CostEstimate) (contract.CostApproval, error) {
					t.Error("preflight must not be called when the estimate is within the budget")
					return contract.ApprovalYesFlag, nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Manifest.Cost.Approval; got != tc.want {
				t.Errorf("approval = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestOverBudgetWithoutPreflightIsUnchecked(t *testing.T) {
	e := newEnv(t)
	overBudget(e)
	res, err := e.run(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Manifest.Cost.Approval; got != contract.ApprovalUnchecked {
		t.Errorf("approval = %s, want unchecked (no gate wired)", got)
	}
}

func TestPreflightErrorCreatesNoRun(t *testing.T) {
	e := newEnv(t)
	overBudget(e)
	refused := errors.New("over budget")
	res, err := Run(context.Background(), Options{RepoPath: e.repo, Config: e.cfg, Harness: e.h, Store: e.store, SiftVersion: "test",
		Preflight: func(context.Context, contract.CostEstimate) (contract.CostApproval, error) {
			return "", refused
		}})
	if !errors.Is(err, refused) || res != nil {
		t.Fatalf("got %v, %v; want no run and the preflight error", res, err)
	}
	if ids, _ := e.store.List(); len(ids) != 0 {
		t.Fatalf("runs created: %v", ids)
	}
	if n := len(e.h.Calls()); n != 0 {
		t.Fatalf("harness ran %d agent(s)", n)
	}
}

func TestPreflightInvalidApprovalCreatesNoRun(t *testing.T) {
	// Only yes_flag and prompt can approve an over-budget run.
	for _, approval := range []contract.CostApproval{contract.ApprovalWithinBudget, contract.ApprovalNoBudget, contract.ApprovalUnchecked, "", "sure"} {
		t.Run(string(approval), func(t *testing.T) {
			e := newEnv(t)
			overBudget(e)
			_, err := Run(context.Background(), Options{RepoPath: e.repo, Config: e.cfg, Harness: e.h, Store: e.store, SiftVersion: "test",
				Preflight: func(context.Context, contract.CostEstimate) (contract.CostApproval, error) { return approval, nil }})
			if err == nil || !strings.Contains(err.Error(), "over-budget run") {
				t.Fatalf("err = %v", err)
			}
			if ids, _ := e.store.List(); len(ids) != 0 {
				t.Fatalf("runs created: %v", ids)
			}
			if n := len(e.h.Calls()); n != 0 {
				t.Fatalf("harness ran %d agent(s)", n)
			}
		})
	}
}

func TestEnforcingHarnessCapsEstimate(t *testing.T) {
	e := newEnv(t)
	e.cfg.Agents.MaxBudgetUSD = 0.10
	h := budgetHarness{e.h}
	res, err := Run(context.Background(), Options{RepoPath: e.repo, Config: e.cfg, Harness: h, Store: e.store, SiftVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	est := res.Manifest.Cost.Estimate
	if !est.HarnessEnforcesBudget || !reflect.DeepEqual(est, cost.ForAudit(e.cfg, res.Manifest.Repository.SnapshotFiles, true)) {
		t.Errorf("estimate = %+v", est)
	}
	if uncapped := cost.ForAudit(e.cfg, est.Files, false); est.HighUSD >= uncapped.HighUSD {
		t.Errorf("capped high %v should be below uncapped %v", est.HighUSD, uncapped.HighUSD)
	}
}

// budgetHarness advertises the budget-limit capability.
type budgetHarness struct{ *fake.Harness }

func (h budgetHarness) Probe(ctx context.Context) (harness.Info, error) {
	info, err := h.Harness.Probe(ctx)
	info.Capabilities = append(info.Capabilities, harness.CapBudgetLimit)
	return info, err
}

func TestInterruptedRunKeepsPartialActualCost(t *testing.T) {
	e := newEnv(t)
	e.cfg.Audit.MaxParallelAgents = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.h.Before = func(ctx context.Context, req harness.Request) error {
		if req.Agent == "pruv" && testutil.FindingID(req) == "CORR-002" {
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
	// discovery, 2 reviews, Pruv on ARCH-001 and CORR-001 report $0.01
	// each; the canceled Pruv on CORR-002 is counted but reports nothing.
	var recorded int
	for _, task := range m.Tasks {
		recorded += len(task.Executions)
	}
	a := m.Cost.Actual
	if a.Executions != 6 || a.Executions != recorded || a.ReportedExecutions != 5 || a.PruvExecutions != 3 || a.Candidates != 4 || math.Abs(a.USD-0.05) > 1e-9 {
		t.Errorf("actual = %+v (manifest lists %d executions)", a, recorded)
	}
	var saved contract.Manifest
	if err := res.Run.ReadJSON(storage.ManifestFile, &saved); err != nil || saved.Cost == nil || saved.Cost.Actual != a {
		t.Errorf("saved actual = %+v, %v", saved.Cost, err)
	}
}
