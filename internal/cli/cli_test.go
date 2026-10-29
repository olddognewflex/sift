package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	var out, errb bytes.Buffer
	d := Deps{Stdout: &out, Stderr: &errb, Version: "test",
		NewHarness: func(config.Config) (harness.Harness, error) { return e.h, nil }}
	code = Execute(context.Background(), d, append([]string{"--config", e.cfgPath}, args...))
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
