package bench

// The live benchmark makes real model calls and costs money (or Kiro
// credits), so it runs only with SIFT_BENCH=1, and spends nothing without
// SIFT_BENCH_BUDGET_USD. Settings and rules: docs/benchmark.md.
//
//	make bench                        # estimate only
//	make bench BENCH_BUDGET=20        # run within $20
//	make bench-rescore RESULTS=<file> # rescore a results file offline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/cli"
	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/harness/claudecode"
	"github.com/olddognewflex/sift/internal/harness/kiro"
	"github.com/olddognewflex/sift/internal/testutil"
)

// defaultModels mirrors the end-to-end suite's per-harness defaults.
var defaultModels = map[string]string{claudecode.Name: "sonnet", kiro.Name: "claude-sonnet-5.5"}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func baselinePath(root string) string { return filepath.Join(root, "bench", "baseline.json") }

// outPath resolves SIFT_BENCH_OUT against the repository root. It may not
// be the baseline, which only SIFT_BENCH_UPDATE_BASELINE writes.
func outPath(root, out string) (string, error) {
	if !filepath.IsAbs(out) {
		out = filepath.Join(root, out)
	}
	if samePath(out, baselinePath(root)) {
		return "", fmt.Errorf("SIFT_BENCH_OUT %s is the baseline; use SIFT_BENCH_UPDATE_BASELINE=1 to update it", out)
	}
	return filepath.Clean(out), nil
}

// baselineUpdateErr says why the baseline may not be updated. Before the
// run, pass incomplete 0.
func baselineUpdateErr(filter []string, dirty bool, incomplete int) error {
	switch {
	case len(filter) > 0:
		return errors.New("refusing SIFT_BENCH_UPDATE_BASELINE with SIFT_BENCH_FIXTURES set: the baseline covers every fixture")
	case dirty:
		return errors.New("refusing SIFT_BENCH_UPDATE_BASELINE: the Sift tree has uncommitted changes outside bench/results/; commit them first")
	case incomplete > 0:
		return fmt.Errorf("refusing SIFT_BENCH_UPDATE_BASELINE: %d run(s) incomplete", incomplete)
	}
	return nil
}

// fixtureRepo materializes a fixture as a committed repository. Its path
// is recorded in each run's manifest, so it must not name the benchmark.
func fixtureRepo(t *testing.T) func(map[string]string) (string, error) {
	return func(files map[string]string) (string, error) {
		dir, err := os.MkdirTemp("", "sift-fixture-")
		if err != nil {
			return "", err
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		testutil.Git(t, dir, "init", "-q")
		testutil.WriteFiles(t, dir, files)
		testutil.Git(t, dir, "add", "-A")
		testutil.Git(t, dir, "commit", "-q", "-m", "initial")
		return dir, nil
	}
}

func TestBenchLive(t *testing.T) {
	if os.Getenv("SIFT_BENCH") != "1" {
		t.Skip("set SIFT_BENCH=1 to run the live benchmark (costs money; see docs/benchmark.md)")
	}
	budget, err := strconv.ParseFloat(envOr("SIFT_BENCH_BUDGET_USD", "0"), 64)
	if err == nil {
		err = CheckBudget(budget)
	}
	if err != nil {
		t.Fatalf("SIFT_BENCH_BUDGET_USD %q: %v", os.Getenv("SIFT_BENCH_BUDGET_USD"), err)
	}
	trials, err := strconv.Atoi(envOr("SIFT_BENCH_TRIALS", "1"))
	if err != nil || trials < 1 || trials > MaxTrials {
		t.Fatalf("SIFT_BENCH_TRIALS: want an integer from 1 to %d, got %q", MaxTrials, os.Getenv("SIFT_BENCH_TRIALS"))
	}
	var filter []string
	for f := range strings.SplitSeq(os.Getenv("SIFT_BENCH_FIXTURES"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			filter = append(filter, f)
		}
	}
	update := os.Getenv("SIFT_BENCH_UPDATE_BASELINE") == "1"

	// Ctrl-C, SIGTERM, or the test deadline cancels the audit in flight
	// (its harness processes are killed) and still writes the results.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop) // after the first signal, a second one quits at once
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl.Add(-min(10*time.Minute, time.Until(dl)/10)))
		defer cancel()
	}

	root, sift, err := SiftTree(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	out, err := outPath(root, envOr("SIFT_BENCH_OUT", filepath.Join("bench", "results", stamp+".json")))
	if err != nil {
		t.Fatal(err)
	}
	if update {
		if err := baselineUpdateErr(filter, sift.Dirty, 0); err != nil {
			t.Fatal(err)
		}
	}
	fixtures, err := LoadFixtures("testdata/fixtures", filter)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Harness.Name = envOr("SIFT_BENCH_HARNESS", claudecode.Name)
	cfg.Harness.Model = envOr("SIFT_BENCH_MODEL", defaultModels[cfg.Harness.Name])
	h, err := cli.DefaultHarness(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d fixture(s) × %d trial(s), harness %s, model %s", len(fixtures), trials, cfg.Harness.Name, cfg.Harness.Model)
	res, err := Run(ctx, Options{
		Fixtures: fixtures, Config: cfg, Harness: h, Budget: budget, BudgetKey: "SIFT_BENCH_BUDGET_USD", Trials: trials,
		// Agents see their working directory, so it must not name the benchmark.
		RunsDir: envOr("SIFT_BENCH_RUNS_DIR", filepath.Join(os.TempDir(), "sift-runs-"+stamp)),
		Log:     testWriter{t},
		Repo:    fixtureRepo(t),
	})
	if errors.Is(err, ErrNoBudget) {
		high := res.Cost.Estimate.HighUSD
		t.Skipf("estimate only; nothing spent; rerun with BENCH_BUDGET=<≥ %.2f>; a run that must complete, such as a baseline, needs BENCH_BUDGET=%.0f (docs/benchmark.md, Budget)", high, RecommendedBudget(high, cfg))
	}
	if err != nil && (res == nil || len(res.Fixtures) == 0 || len(res.Fixtures[0].Runs) == 0) {
		t.Fatal(err) // refused before any audit ran
	}
	res.Sift = sift
	compareWithBaseline(t, root, "", res, fixtures)
	if werr := WriteResults(out, res); werr != nil {
		t.Fatal(werr)
	}
	summarize(t, res)
	t.Logf("results written to %s", out)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("interrupted (%v); the results cover the runs that finished", context.Cause(ctx))
	}
	if update {
		if err := baselineUpdateErr(filter, sift.Dirty, res.Incomplete()); err != nil {
			t.Fatal(err)
		}
		if err := WriteResults(baselinePath(root), res); err != nil {
			t.Fatal(err)
		}
		t.Log("baseline updated: bench/baseline.json")
	}
}

// TestBenchRescore rescores a results file with the current keys and
// matching rules and rewrites its scores (and its comparison with the
// baseline, when there is one). It is offline and free.
func TestBenchRescore(t *testing.T) {
	path := os.Getenv("SIFT_BENCH_RESCORE")
	if path == "" {
		t.Skip("set SIFT_BENCH_RESCORE=<results file> to rescore it offline (make bench-rescore)")
	}
	root, _, err := SiftTree(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	res, err := ReadResults(path)
	if err != nil {
		t.Fatal(err)
	}
	fixtures, err := LoadFixtures("testdata/fixtures", nil)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := res.Rescore(fixtures)
	for _, n := range notes {
		t.Log("note: " + n)
	}
	if err != nil {
		t.Fatal(err)
	}
	compareWithBaseline(t, root, path, res, fixtures)
	if err := WriteResults(path, res); err != nil {
		t.Fatal(err)
	}
	summarize(t, res)
}

// compareWithBaseline sets res.Comparison from bench/baseline.json, when
// it exists and is not self (the file being rescored). A comparison that
// cannot be made is a note, not a failure.
func compareWithBaseline(t *testing.T, root, self string, res *Results, fixtures []Fixture) {
	t.Helper()
	path := baselinePath(root)
	if self != "" && samePath(self, path) {
		return
	}
	base, err := ReadResults(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Log("no bench/baseline.json; nothing to compare with")
		return
	}
	if err == nil {
		res.Comparison, err = Compare(base, res, fixtures)
	}
	if err != nil {
		t.Logf("note: no comparison with the baseline: %v", err)
	}
}

func summarize(t *testing.T, res *Results) {
	var b bytes.Buffer
	res.Summary(&b)
	t.Log("\n" + b.String())
}

// testWriter sends progress lines to the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func TestOutPathRefusesBaseline(t *testing.T) {
	root := t.TempDir()
	testutil.WriteFiles(t, root, map[string]string{"bench/baseline.json": "{}"})
	if err := os.Symlink(filepath.Join(root, "bench"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{"bench/baseline.json", "bench/results/../baseline.json", filepath.Join(root, "alias", "baseline.json")} {
		if _, err := outPath(root, out); err == nil {
			t.Errorf("outPath(%q) accepted the baseline", out)
		}
	}
	if got, err := outPath(root, "bench/results/x.json"); err != nil || got != filepath.Join(root, "bench", "results", "x.json") {
		t.Errorf("outPath = %q, %v", got, err)
	}
}

func TestBaselineUpdateRefusals(t *testing.T) {
	for _, tc := range []struct {
		filter     []string
		dirty      bool
		incomplete int
		want       string
	}{
		{nil, false, 0, ""},
		{[]string{"ledger"}, false, 0, "SIFT_BENCH_FIXTURES"},
		{nil, true, 0, "uncommitted"},
		{nil, false, 2, "2 run(s) incomplete"},
	} {
		err := baselineUpdateErr(tc.filter, tc.dirty, tc.incomplete)
		if (err == nil) != (tc.want == "") || err != nil && !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}
