package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/harness"
	"github.com/olddognewflex/sift/internal/harness/fake"
	"github.com/olddognewflex/sift/internal/storage"
	"github.com/olddognewflex/sift/internal/testutil"
)

type cliEnv struct {
	t       *testing.T
	cfgPath string
	runs    string
	repo    string
	h       *fake.Harness
	// stdin and tty stand in for the terminal. The default is an empty,
	// non-terminal stdin, as in a script or CI.
	stdin io.Reader
	tty   bool
}

func newCLI(t *testing.T) *cliEnv {
	dir := t.TempDir()
	e := &cliEnv{t: t, cfgPath: filepath.Join(dir, "sift.yaml"), runs: filepath.Join(dir, "runs"), repo: testutil.StatsRepo(t), h: testutil.Harness()}
	cfg := strings.Replace(config.Template, "runs_dir: ~/.sift/runs", "runs_dir: "+e.runs, 1)
	if err := os.WriteFile(e.cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *cliEnv) sift(args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	return e.siftCtx(context.Background(), args...)
}

func (e *cliEnv) siftCtx(ctx context.Context, args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	var out, errb bytes.Buffer
	stdin := e.stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	d := Deps{Stdout: &out, Stderr: &errb, Version: "test", Stdin: stdin, Interactive: func() bool { return e.tty },
		NewHarness: func(config.Config) (harness.Harness, error) { return e.h, nil }}
	code = Execute(ctx, d, append([]string{"--config", e.cfgPath}, args...))
	return code, out.String(), errb.String()
}

func TestInit(t *testing.T) {
	e := newCLI(t)
	path := filepath.Join(t.TempDir(), "new.yaml")
	if code, out, errs := e.sift("init", "--config", path); code != 0 || !strings.Contains(out, "Wrote") {
		t.Fatalf("init: %d %s %s", code, out, errs)
	}
	if _, _, err := config.Load(path); err != nil {
		t.Fatalf("written config does not load: %v", err)
	}
	if code, _, errs := e.sift("init", "--config", path); code != ExitError || !strings.Contains(errs, "already exists") {
		t.Fatalf("second init: %d %s", code, errs)
	}
	if code, _, _ := e.sift("init", "--config", path, "--force"); code != 0 {
		t.Fatal("--force should overwrite")
	}
}

func TestAuditStatusFindingsReport(t *testing.T) {
	e := newCLI(t)
	code, out, errs := e.sift("audit", e.repo)
	if code != ExitOK {
		t.Fatalf("audit exit %d\n%s\n%s", code, out, errs)
	}
	if !strings.Contains(out, "Status:  completed") || !strings.Contains(errs, "[validation] CORR-001 → confirmed") {
		t.Fatalf("audit output:\n%s\n%s", out, errs)
	}

	_, out, _ = e.sift("status")
	for _, want := range []string{"Status:     completed", "review-correctness", "succeeded", "Findings:", "1 confirmed", "repository unchanged = true", "Reports:"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
	code, out, _ = e.sift("status", "--json")
	var st struct {
		EffectiveStatus contract.RunStatus `json:"effective_status"`
		Manifest        contract.Manifest  `json:"manifest"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || st.EffectiveStatus != contract.RunCompleted || st.Manifest.RunID == "" {
		t.Fatalf("status --json: %d %s", code, out)
	}

	_, out, _ = e.sift("findings")
	for _, want := range []string{"ID", "SEVERITY", "CONFIDENCE", "STATE", "CORR-001", "confirmed", "CORR-002", "needs_investigation", "rejected"} {
		if !strings.Contains(out, want) {
			t.Errorf("findings missing %q:\n%s", want, out)
		}
	}
	_, out, _ = e.sift("findings", "--state", "confirmed")
	if !strings.Contains(out, "CORR-001") || strings.Contains(out, "CORR-002") {
		t.Errorf("--state filter:\n%s", out)
	}
	_, out, _ = e.sift("findings", "--json", "--state", "rejected")
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 1 || items[0]["id"] != "CORR-003" {
		t.Errorf("findings --json: %v %s", err, out)
	}
	if code, _, errs := e.sift("findings", "--state", "great"); code != ExitError || !strings.Contains(errs, "--state") {
		t.Errorf("bad --state: %d %s", code, errs)
	}

	_, out, _ = e.sift("report")
	if lines := strings.Fields(out); len(lines) != 4 || !strings.HasSuffix(lines[0], ".md") {
		t.Errorf("report paths:\n%s", out)
	}
	for _, p := range strings.Fields(out) {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, errs := e.sift("report"); code != ExitError || !strings.Contains(errs, "--regenerate") {
		t.Errorf("missing reports: %d %s", code, errs)
	}
	if code, out, _ := e.sift("report", "--regenerate"); code != 0 || len(strings.Fields(out)) != 4 {
		t.Errorf("regenerate: %d %s", code, out)
	}
}

func TestAuditExitCodes(t *testing.T) {
	e := newCLI(t)
	e.h.Responders["architecture"] = fake.Fail(harness.ErrProcess, "crash")
	if code, out, errs := e.sift("audit", e.repo); code != ExitPartial || !strings.Contains(errs, "incomplete results") {
		t.Fatalf("partial: %d\n%s\n%s", code, out, errs)
	}
	e.h.Responders["discovery"] = fake.Fail(harness.ErrTimeout, "slow")
	if code, _, _ := e.sift("audit", e.repo); code != ExitError {
		t.Fatalf("failed audit exit = %d", code)
	}
	if code, _, errs := e.sift("audit", t.TempDir()); code != ExitError || !strings.Contains(errs, "not inside a Git repository") {
		t.Fatalf("non-git: %d %s", code, errs)
	}
	if code, _, errs := e.sift("audit", e.repo, "--dirty", "maybe"); code != ExitError || !strings.Contains(errs, "dirty_policy") {
		t.Fatalf("bad --dirty: %d %s", code, errs)
	}
	if code, _, _ := e.sift("audit"); code != ExitError {
		t.Fatal("audit without a path must fail")
	}
}

func TestAuditInterruptedDuringExportExits130(t *testing.T) {
	e := newCLI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errb bytes.Buffer
	d := Deps{Stdout: &out, Stderr: &errb, Version: "test",
		NewHarness: func(config.Config) (harness.Harness, error) { return e.h, nil },
		Now: func() time.Time {
			if m, _ := filepath.Glob(filepath.Join(e.runs, "*", storage.AgentsDir, "*")); len(m) > 0 {
				cancel()
			}
			return time.Now()
		}}
	code := Execute(ctx, d, []string{"--config", e.cfgPath, "audit", e.repo})
	if code != ExitInterrupted || !strings.Contains(out.String(), "Status:  interrupted") || !strings.Contains(errb.String(), "audit interrupted: export snapshot") {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
}

func TestAuditInterruptedBeforeRunExits130(t *testing.T) {
	e := newCLI(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errb bytes.Buffer
	d := Deps{Stdout: &out, Stderr: &errb, Version: "test",
		NewHarness: func(config.Config) (harness.Harness, error) { return e.h, nil }}
	if code := Execute(ctx, d, []string{"--config", e.cfgPath, "audit", e.repo}); code != ExitInterrupted || !strings.Contains(errb.String(), "before a run was created") {
		t.Fatalf("exit %d\n%s", code, errb.String())
	}
	if runs, _ := filepath.Glob(filepath.Join(e.runs, "*")); len(runs) != 0 {
		t.Errorf("no run should exist, found %v", runs)
	}
}

func TestDirtyFlag(t *testing.T) {
	e := newCLI(t)
	testutil.WriteFiles(t, e.repo, map[string]string{"scratch.txt": "wip\n"})
	if code, _, errs := e.sift("audit", e.repo); code != ExitError || !strings.Contains(errs, "--dirty=head") {
		t.Fatalf("dirty refused: %d %s", code, errs)
	}
	if code, out, errs := e.sift("audit", e.repo, "--dirty", "head"); code != ExitOK {
		t.Fatalf("--dirty head: %d %s %s", code, out, errs)
	}
	_, out, _ := e.sift("status")
	if !strings.Contains(out, "Dirty tree: 1 uncommitted path(s), policy head") || !strings.Contains(out, "were excluded") {
		t.Errorf("status:\n%s", out)
	}
}

func TestStatusDetectsDeadProcess(t *testing.T) {
	e := newCLI(t)
	if code, _, _ := e.sift("audit", e.repo); code != 0 {
		t.Fatal("audit failed")
	}
	store := storage.Store{Root: e.runs}
	run, _ := store.Latest()
	m, _ := run.Manifest()
	m.Status, m.PID, m.FinishedAt = contract.RunRunning, 999999, nil
	if err := run.WriteJSON(storage.ManifestFile, m); err != nil {
		t.Fatal(err)
	}
	_, out, _ := e.sift("status", "--run", run.ID)
	if !strings.Contains(out, "Status:     interrupted (the audit process (pid 999999) exited without finishing") {
		t.Errorf("status:\n%s", out)
	}
}

func TestNoRuns(t *testing.T) {
	e := newCLI(t)
	for _, cmd := range []string{"status", "findings", "report"} {
		if code, _, errs := e.sift(cmd); code != ExitError || !strings.Contains(errs, "no audit runs found") {
			t.Errorf("%s: %d %s", cmd, code, errs)
		}
	}
	if code, _, errs := e.sift("status", "--run", "20200101T000000Z-abcdef"); code != ExitError || !strings.Contains(errs, "not found") {
		t.Errorf("unknown run: %d %s", code, errs)
	}
}

// overBudget sets a $1 run budget, which the fixture audit exceeds: 2
// reviewers × 8 findings × $0.30 alone is $4.80.
func (e *cliEnv) overBudget() {
	e.t.Helper()
	data, err := os.ReadFile(e.cfgPath)
	if err != nil {
		e.t.Fatal(err)
	}
	cfg := strings.Replace(string(data), "budget_usd: 15", "budget_usd: 1", 1)
	if cfg == string(data) {
		e.t.Fatal("template has no budget_usd: 15 line")
	}
	if err := os.WriteFile(e.cfgPath, []byte(cfg), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *cliEnv) noRuns() {
	e.t.Helper()
	if runs, _ := filepath.Glob(filepath.Join(e.runs, "*")); len(runs) != 0 {
		e.t.Errorf("no run should exist, found %v", runs)
	}
	if n := len(e.h.Calls()); n != 0 {
		e.t.Errorf("harness ran %d agent(s); want none", n)
	}
}

func (e *cliEnv) latestCost() *contract.RunCost {
	e.t.Helper()
	run, err := storage.Store{Root: e.runs}.Latest()
	if err != nil {
		e.t.Fatal(err)
	}
	m, err := run.Manifest()
	if err != nil {
		e.t.Fatal(err)
	}
	if m.Cost == nil {
		e.t.Fatal("manifest has no cost")
	}
	return m.Cost
}

const estimateLine = "sift: cost estimate (audit, 2 files, 2 reviewers): $"

func TestAuditOverBudgetRefusesNonInteractive(t *testing.T) {
	e := newCLI(t)
	e.overBudget()
	code, out, errs := e.sift("audit", e.repo)
	if code != ExitError {
		t.Fatalf("exit %d, want %d\n%s\n%s", code, ExitError, out, errs)
	}
	for _, want := range []string{estimateLine, "sift: estimated cost up to $5.38 exceeds the run budget $1.00 (cost.budget_usd); rerun with --yes or raise cost.budget_usd"} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr missing %q:\n%s", want, errs)
		}
	}
	if strings.Contains(errs, "Continue?") {
		t.Errorf("prompted without a terminal:\n%s", errs)
	}
	if out != "" {
		t.Errorf("stdout should be empty, got %q", out)
	}
	e.noRuns()
}

func TestAuditOverBudgetYes(t *testing.T) {
	e := newCLI(t)
	e.overBudget()
	code, out, errs := e.sift("audit", e.repo, "--yes")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	if !strings.Contains(errs, estimateLine) || !strings.Contains(errs, "sift: over budget; proceeding (--yes)") {
		t.Errorf("stderr:\n%s", errs)
	}
	if strings.Index(errs, estimateLine) > strings.Index(errs, "sift: run ") {
		t.Errorf("estimate must be printed before the run starts:\n%s", errs)
	}
	if c := e.latestCost(); c.Approval != contract.ApprovalYesFlag || c.BudgetUSD != 1 {
		t.Errorf("cost approval %s, budget %v; want yes_flag, 1", c.Approval, c.BudgetUSD)
	}
}

func TestAuditOverBudgetPrompt(t *testing.T) {
	const prompt = "Estimated cost up to $5.38 exceeds budget $1.00. Continue? [y/N] "
	for _, tc := range []struct {
		name, input string
		want        int
	}{
		{"no", "n\n", ExitError},
		{"empty", "\n", ExitError},
		{"eof", "", ExitError},
		{"other", "sure\n", ExitError},
		{"y without Enter", "y", ExitError}, // input ended; not a deliberate answer
		{"yes", "y\n", ExitOK},
		{"YES padded", "  YES \n", ExitOK},
		{"y CRLF", "y\r\n", ExitOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCLI(t)
			e.overBudget()
			e.tty, e.stdin = true, strings.NewReader(tc.input)
			code, out, errs := e.sift("audit", e.repo)
			if code != tc.want {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.want, out, errs)
			}
			if !strings.Contains(errs, prompt) || strings.Index(errs, estimateLine) > strings.Index(errs, prompt) {
				t.Errorf("want the estimate, then the prompt, on stderr:\n%s", errs)
			}
			if tc.want == ExitOK {
				if c := e.latestCost(); c.Approval != contract.ApprovalPrompt {
					t.Errorf("approval %s, want prompt", c.Approval)
				}
				return
			}
			if !strings.Contains(errs, "exceeds the run budget") {
				t.Errorf("stderr:\n%s", errs)
			}
			if out != "" {
				t.Errorf("stdout should be empty, got %q", out)
			}
			e.noRuns()
		})
	}
}

// blockingReader cancels the run when the gate starts reading, then blocks
// like a person who never answers.
type blockingReader struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (r blockingReader) Read([]byte) (int, error) {
	r.cancel()
	<-r.done
	return 0, io.EOF
}

func TestAuditOverBudgetPromptInterrupted(t *testing.T) {
	e := newCLI(t)
	e.overBudget()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	e.tty, e.stdin = true, blockingReader{cancel, done}
	code, _, errs := e.siftCtx(ctx, "audit", e.repo)
	if code != ExitInterrupted || !strings.Contains(errs, "Continue? [y/N]") || !strings.Contains(errs, "interrupted before a run was created") {
		t.Fatalf("exit %d\n%s", code, errs)
	}
	e.noRuns()
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestAuditWithinBudgetDoesNotPrompt(t *testing.T) {
	e := newCLI(t)
	e.tty = true
	e.stdin = readerFunc(func([]byte) (int, error) {
		t.Error("stdin read within budget")
		return 0, io.EOF
	})
	code, out, errs := e.sift("audit", e.repo)
	if code != ExitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	if !strings.Contains(errs, estimateLine) || !strings.Contains(errs, "sift:   budget $15.00 (cost.budget_usd): within budget") || strings.Contains(errs, "Continue?") {
		t.Errorf("stderr:\n%s", errs)
	}
	if c := e.latestCost(); c.Approval != contract.ApprovalWithinBudget {
		t.Errorf("approval %s, want within_budget", c.Approval)
	}
}

func TestStatusShowsCost(t *testing.T) {
	e := newCLI(t)
	if code, out, errs := e.sift("audit", e.repo); code != ExitOK {
		t.Fatalf("audit: %d\n%s\n%s", code, out, errs)
	}
	run, _ := storage.Store{Root: e.runs}.Latest()
	m, _ := run.Manifest()
	execs := loadExecutions(run, m)
	var summed float64
	for _, x := range execs {
		summed += x.CostUSD
	}

	// The manifest's actual cost wins over the per-execution sum.
	m.Cost.Actual.USD = 1.23
	if err := run.WriteJSON(storage.ManifestFile, m); err != nil {
		t.Fatal(err)
	}
	_, out, _ := e.sift("status")
	est := m.Cost.Estimate
	for _, want := range []string{
		fmt.Sprintf("Estimate:   $%.2f – $%.2f (budget $15.00, within budget)", est.LowUSD, est.HighUSD),
		fmt.Sprintf("Agent executions: %d, reported cost $1.23,", len(execs)),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}

	// A manifest from before cost recording falls back to the sum.
	m.Cost = nil
	if err := run.WriteJSON(storage.ManifestFile, m); err != nil {
		t.Fatal(err)
	}
	_, out, _ = e.sift("status")
	if strings.Contains(out, "Estimate:") || !strings.Contains(out, fmt.Sprintf("reported cost $%.2f,", summed)) || summed == 0 {
		t.Errorf("legacy status (sum $%.2f):\n%s", summed, out)
	}
}

func TestStatusShowsNoBudget(t *testing.T) {
	e := newCLI(t)
	data, _ := os.ReadFile(e.cfgPath)
	if err := os.WriteFile(e.cfgPath, []byte(strings.Replace(string(data), "budget_usd: 15", "budget_usd: 0", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := e.sift("audit", e.repo); code != ExitOK {
		t.Fatalf("audit: %d\n%s\n%s", code, out, errs)
	}
	if _, out, _ := e.sift("status"); !strings.Contains(out, " (no budget set)\n") || strings.Contains(out, "no_budget") {
		t.Errorf("status:\n%s", out)
	}
}

func TestStatusLabelsApproval(t *testing.T) {
	for approval, want := range map[contract.CostApproval]string{
		contract.ApprovalYesFlag:      "approved with --yes",
		contract.ApprovalPrompt:       "approved at prompt",
		contract.ApprovalWithinBudget: "within budget",
		contract.ApprovalNoBudget:     "no budget set",
		contract.ApprovalUnchecked:    "not checked",
	} {
		if got := approvalLabel(approval); got != want {
			t.Errorf("approvalLabel(%s) = %q, want %q", approval, got, want)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	file, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var nilFile *os.File
	for name, v := range map[string]any{
		"null device": null, "pipe read end": r, "pipe write end": w, "regular file": file,
		"strings.Reader": strings.NewReader("y\n"), "bytes.Buffer": &bytes.Buffer{}, "nil *os.File": nilFile, "nil": nil,
	} {
		if isTerminal(v) {
			t.Errorf("%s: reported as a terminal", name)
		}
	}
	// A real terminal, when the test has one (not in CI).
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		defer tty.Close()
		if !isTerminal(tty) {
			t.Error("/dev/tty: not reported as a terminal")
		}
	}
}

func TestInteractiveNeedsStdinAndStderrTerminals(t *testing.T) {
	stdin, stderr := strings.NewReader(""), &bytes.Buffer{}
	for _, tc := range []struct {
		name          string
		inTTY, errTTY bool
		want          bool
	}{
		{"both terminals", true, true, true},
		{"stderr redirected", true, false, false},
		{"stdin redirected", false, true, false},
		{"neither", false, false, false},
	} {
		isTerm := func(v any) bool {
			switch v {
			case stdin:
				return tc.inTTY
			case stderr:
				return tc.errTTY
			}
			t.Fatalf("unexpected stream %T", v)
			return false
		}
		if got := interactive(stdin, stderr, isTerm); got != tc.want {
			t.Errorf("%s: interactive = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// With Interactive left to its default, a pipe for stdin is not a
// terminal, so an over-budget audit refuses without prompting.
func TestAuditDefaultInteractiveRefusesPipe(t *testing.T) {
	e := newCLI(t)
	e.overBudget()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	var out, errb bytes.Buffer
	d := Deps{Stdout: &out, Stderr: &errb, Stdin: r, Version: "test",
		NewHarness: func(config.Config) (harness.Harness, error) { return e.h, nil }}
	code := Execute(context.Background(), d, []string{"--config", e.cfgPath, "audit", e.repo})
	if code != ExitError || strings.Contains(errb.String(), "Continue?") || !strings.Contains(errb.String(), "exceeds the run budget") {
		t.Fatalf("exit %d\n%s", code, errb.String())
	}
	e.noRuns()
}

func TestSpendGateNeverApprovesCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	est := contract.CostEstimate{HighUSD: 5}
	for _, tc := range []struct {
		name  string
		yes   bool
		stdin io.Reader
	}{
		{"--yes", true, strings.NewReader("")},
		{"prompt answered y", false, strings.NewReader("y\n")},
	} {
		var errb bytes.Buffer
		d := Deps{Stderr: &errb, Stdin: tc.stdin, Interactive: func() bool { return true }}
		approval, err := spendGate(d, 1, tc.yes)(ctx, est)
		if approval != "" || !errors.Is(err, context.Canceled) {
			t.Errorf("%s: approval %q, err %v; want none and context.Canceled", tc.name, approval, err)
		}
	}
}

// cancelOnRead answers y but cancels the run while doing so, the race
// where the reply and the interrupt arrive together.
type cancelOnRead struct{ cancel context.CancelFunc }

func (c cancelOnRead) Read(p []byte) (int, error) {
	c.cancel()
	return copy(p, "y\n"), nil
}

func TestSpendGateRechecksContextAfterReply(t *testing.T) {
	for range 20 { // either select branch may win; both must refuse
		ctx, cancel := context.WithCancel(context.Background())
		d := Deps{Stderr: io.Discard, Stdin: cancelOnRead{cancel}, Interactive: func() bool { return true }}
		approval, err := spendGate(d, 1, false)(ctx, contract.CostEstimate{HighUSD: 5})
		cancel()
		if approval != "" || !errors.Is(err, context.Canceled) {
			t.Fatalf("approval %q, err %v; want none and context.Canceled", approval, err)
		}
	}
}

func TestSpendGateBoundsAnswer(t *testing.T) {
	d := Deps{Stderr: io.Discard, Stdin: strings.NewReader(strings.Repeat("y", 4*maxAnswer) + "\n"), Interactive: func() bool { return true }}
	if approval, err := spendGate(d, 1, false)(context.Background(), contract.CostEstimate{HighUSD: 5}); approval != "" || err == nil {
		t.Errorf("over-long answer: approval %q, err %v", approval, err)
	}
}
