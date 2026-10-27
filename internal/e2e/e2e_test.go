// Package e2e holds the optional end-to-end smoke test, which runs a full
// audit with the real Claude Code harness. It makes live model calls and
// costs money, so it only runs when SIFT_E2E=1:
//
//	SIFT_E2E=1 go test ./internal/e2e -v -timeout 30m
//
// SIFT_E2E_MODEL selects the model (default: sonnet).
package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/cli"
	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/report"
	"github.com/olddognewflex/sift/internal/source"
	"github.com/olddognewflex/sift/internal/storage"
	"github.com/olddognewflex/sift/internal/testutil"
)

func TestRealHarnessAudit(t *testing.T) {
	if os.Getenv("SIFT_E2E") != "1" {
		t.Skip("set SIFT_E2E=1 to run the live end-to-end audit")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not installed")
	}
	model := os.Getenv("SIFT_E2E_MODEL")
	if model == "" {
		model = "sonnet"
	}
	repo := testutil.StatsRepo(t)
	runs := filepath.Join(t.TempDir(), "runs")
	cfg := filepath.Join(t.TempDir(), "sift.yaml")
	if err := os.WriteFile(cfg, []byte(`version: 1
storage: {runs_dir: `+runs+`}
harness: {name: claude-code, binary: claude, model: `+model+`}
audit: {max_findings_per_reviewer: 3}
agents: {timeout: 10m, max_budget_usd: 1}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := source.Fingerprint(context.Background(), repo)

	var out, errb bytes.Buffer
	code := cli.Execute(context.Background(), cli.Deps{Stdout: &out, Stderr: &errb, Version: "e2e"}, []string{"--config", cfg, "audit", repo})
	t.Logf("stdout:\n%s\nstderr:\n%s", out.String(), errb.String())
	if code != cli.ExitOK {
		t.Fatalf("audit exit code %d", code)
	}

	run, err := storage.Store{Root: runs}.Latest()
	if err != nil {
		t.Fatal(err)
	}
	data, err := report.Load(run)
	if err != nil {
		t.Fatal(err)
	}
	if data.Manifest.Status != contract.RunCompleted || data.Discovery == nil {
		t.Fatalf("status %s, discovery present %t", data.Manifest.Status, data.Discovery != nil)
	}
	accepted := 0
	for _, it := range data.Items {
		t.Logf("%s %s %s: %s", it.ID, it.State, it.Severity, it.Title)
		if it.State == contract.StateUnvalidated {
			t.Errorf("%s was not validated", it.ID)
		}
		if (it.State == contract.StateConfirmed || it.State == contract.StateSupportedRisk) && strings.Contains(locsOf(it), "stats.go") {
			accepted++
		}
	}
	// The fixture's Max skips its last element; a working pipeline finds it.
	if accepted == 0 {
		t.Error("no accepted finding in stats.go; expected the planted off-by-one defect")
	}
	if paths, err := report.Paths(run); err != nil || len(paths) != 4 {
		t.Errorf("reports: %v %v", paths, err)
	}
	if after, _ := source.Fingerprint(context.Background(), repo); after != before {
		t.Error("the audit modified the repository")
	}
}

func locsOf(it report.Item) string {
	var s []string
	for _, l := range it.Locations {
		s = append(s, l.Path)
	}
	return strings.Join(s, " ")
}
