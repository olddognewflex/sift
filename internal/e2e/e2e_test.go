// Package e2e holds the optional end-to-end smoke test, which runs a full
// audit of a fixture repository through a real harness. It makes live model
// calls and costs money (or Kiro credits), so it only runs when SIFT_E2E=1:
//
//	SIFT_E2E=1 go test ./internal/e2e -v -timeout 30m
//	make e2e
//
// SIFT_E2E_HARNESS lists the harnesses to run, comma-separated (default:
// claude-code). For example, SIFT_E2E_HARNESS=claude-code,kiro make e2e runs
// the same audit through both. A harness whose CLI is not installed is
// skipped.
//
// SIFT_E2E_MODEL overrides the model for every selected harness. Model names
// are harness-specific, so set it only when running one harness. Defaults:
// sonnet (claude-code), claude-sonnet-5.5 (kiro).
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
	"github.com/olddognewflex/sift/internal/harness/claudecode"
	"github.com/olddognewflex/sift/internal/harness/kiro"
	"github.com/olddognewflex/sift/internal/report"
	"github.com/olddognewflex/sift/internal/source"
	"github.com/olddognewflex/sift/internal/storage"
	"github.com/olddognewflex/sift/internal/testutil"
)

// harnessDefaults gives each supported harness its CLI and a default model
// of comparable capability.
var harnessDefaults = map[string]struct{ binary, model string }{
	claudecode.Name: {claudecode.DefaultBinary, "sonnet"},
	kiro.Name:       {kiro.DefaultBinary, "claude-sonnet-5.5"},
}

func TestRealHarnessAudit(t *testing.T) {
	if os.Getenv("SIFT_E2E") != "1" {
		t.Skip("set SIFT_E2E=1 to run the live end-to-end audit")
	}
	names := os.Getenv("SIFT_E2E_HARNESS")
	if names == "" {
		names = claudecode.Name
	}
	for name := range strings.SplitSeq(names, ",") {
		name = strings.TrimSpace(name)
		t.Run(name, func(t *testing.T) {
			d, ok := harnessDefaults[name]
			if !ok {
				t.Fatalf("unknown harness %q in SIFT_E2E_HARNESS", name)
			}
			if _, err := exec.LookPath(d.binary); err != nil {
				t.Skipf("%s not installed", d.binary)
			}
			model := d.model
			if m := os.Getenv("SIFT_E2E_MODEL"); m != "" {
				model = m
			}
			audit(t, name, model)
		})
	}
}

func audit(t *testing.T, harnessName, model string) {
	repo := testutil.StatsRepo(t)
	runs := filepath.Join(t.TempDir(), "runs")
	cfg := filepath.Join(t.TempDir(), "sift.yaml")
	if err := os.WriteFile(cfg, []byte(`version: 1
storage: {runs_dir: `+runs+`}
harness: {name: `+harnessName+`, model: `+model+`}
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
	m := data.Manifest
	if m.Status != contract.RunCompleted || data.Discovery == nil {
		t.Fatalf("status %s, discovery present %t", m.Status, data.Discovery != nil)
	}
	if m.Harness.Name != harnessName {
		t.Errorf("run recorded harness %q, want %q", m.Harness.Name, harnessName)
	}
	// Every harness has limits; the run must disclose them.
	if len(m.Policy.Unenforced) == 0 {
		t.Error("manifest lists no unenforced guarantees")
	}

	accepted := 0
	for _, it := range data.Items {
		t.Logf("%s %s %s: %s", it.ID, it.State, it.Severity, it.Title)
		if it.State == contract.StateUnvalidated {
			t.Errorf("%s was not validated", it.ID)
		}
		// Harnesses that reach the snapshot by absolute path (kiro) must
		// still produce repository-relative locations.
		for _, l := range it.Locations {
			if filepath.IsAbs(l.Path) || strings.HasPrefix(l.Path, "..") {
				t.Errorf("%s cites a path outside the repository: %q", it.ID, l.Path)
			}
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
