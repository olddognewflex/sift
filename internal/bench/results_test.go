package bench

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/testutil"
)

func sampleResults(t *testing.T) *Results {
	t.Helper()
	fixtures := []Fixture{{Name: "f1", Key: compareKey("f1"), SourceDigest: "s1", KeyDigest: "k1"}}
	r := results("claude-code", map[string][]Observation{"f1": {
		cobs("CORR-001", 10, confirmed, accept), cobs("CORR-002", 70, rejected, reject), cobs("ARCH-001", 90, rejected, reject),
	}}, map[string]string{"f1": "s1"})
	r.CreatedAt = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	r.Harness.Version = "2.1.0"
	r.Cost = CostInfo{BudgetUSD: 20, ActualUSD: 3.21, ChargedUSD: 3.5, Executions: 9}
	r.Cost.Estimate.LowUSD, r.Cost.Estimate.HighUSD = 4.5, 6.75
	if _, err := r.Rescore(fixtures); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResultsReadIsStrict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.json")
	if err := WriteResults(path, sampleResults(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResults(path); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(path)
	for name, data := range map[string]string{
		"unknown field":           strings.Replace(string(good), `"bench_version": 1,`, `"bench_version": 1, "extra": true,`, 1),
		"nested unknown":          strings.Replace(string(good), `"commit": ""`, `"commit": "", "branch": "main"`, 1),
		"trailing data":           string(good) + "{}",
		"unknown version":         strings.Replace(string(good), `"bench_version": 1,`, `"bench_version": 2,`, 1),
		"unknown in observations": strings.Replace(string(good), `"reported_by": "correctness",`, `"reported_by": "correctness", "severity": "high",`, 1),
	} {
		bad := filepath.Join(dir, "bad.json")
		if data == string(good) {
			t.Fatalf("%s: replacement did not apply", name)
		}
		if err := os.WriteFile(bad, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadResults(bad); err == nil {
			t.Errorf("%s: ReadResults accepted it", name)
		}
	}
}

// Scores and the comparison are derived, so fields an older (or newer)
// score layout has are ignored rather than making the file unreadable.
func TestResultsReadScoresLeniently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	if err := WriteResults(path, sampleResults(t)); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(path)
	data := strings.Replace(string(good), `"rules_version": "match/v1",`, `"rules_version": "match/v1", "retired_metric": 0.5,`, 1)
	data = strings.Replace(data, `"bench_version": 1,`, `"bench_version": 1, "comparison": {"verdict": "improved", "old_field": 1},`, 1)
	if data == string(good) || !strings.Contains(data, "retired_metric") {
		t.Fatal("replacement did not apply")
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := ReadResults(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Scores.RulesVersion != "match/v1" || r.Comparison == nil || r.Comparison.Verdict != VerdictImproved || len(r.Fixtures[0].Runs[0].Observations) != 3 {
		t.Errorf("scores %+v comparison %+v", r.Scores.RulesVersion, r.Comparison)
	}
}

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "bench", "baseline.json")
	testutil.WriteFiles(t, dir, map[string]string{"bench/baseline.json": "{}"})
	if err := os.Symlink(filepath.Join(dir, "bench"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, filepath.Join(dir, "bench", ".", "x", "..", "baseline.json"), filepath.Join(dir, "link", "baseline.json")} {
		if !samePath(p, a) {
			t.Errorf("samePath(%q, baseline) = false", p)
		}
	}
	if samePath(filepath.Join(dir, "bench", "other.json"), a) {
		t.Error("a different file is the same path")
	}
	if !samePath(filepath.Join(dir, "new", "..", "missing.json"), filepath.Join(dir, "missing.json")) {
		t.Error("paths to a file that does not exist yet compare by their cleaned form")
	}
}

// Before the first baseline exists, another spelling of its path (a
// symlinked directory, or another case on a case-insensitive filesystem)
// is still the baseline.
func TestSamePathBeforeTheFileExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bench"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "bench"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(dir, "bench", "baseline.json")
	same := []string{filepath.Join(dir, "link", "baseline.json"), filepath.Join(dir, "bench", "Baseline.JSON")}
	if _, err := os.Stat(filepath.Join(dir, "BENCH")); err == nil { // case-insensitive filesystem
		same = append(same, filepath.Join(dir, "Bench", "Baseline.json"))
	}
	for _, p := range same {
		if !samePath(p, baseline) {
			t.Errorf("samePath(%q, missing baseline) = false", p)
		}
	}
	if samePath(filepath.Join(dir, "link", "other.json"), baseline) || samePath(filepath.Join(dir, "baseline.json"), baseline) {
		t.Error("a different file is the same path")
	}
}

func TestWriteResultsIsAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bench", "results")
	path := filepath.Join(dir, "r.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteResults(path, sampleResults(t)); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
	data, _ := os.ReadFile(path)
	if !bytes.HasPrefix(data, []byte("{\n")) || !bytes.HasSuffix(data, []byte("}\n")) {
		t.Errorf("content %q", data)
	}
}

func TestSummary(t *testing.T) {
	r := sampleResults(t)
	r.Warnings = []string{"spend guard: stopped"}
	r.Comparison = &Comparison{BaselineCreatedAt: "2026-11-01T00:00:00Z", BaselineCommit: "abc123", Fixtures: []string{"f1"}, Verdict: VerdictRegressed,
		Metrics: []Delta{judge("candidate.correctness.recall", frac{6, 10}, frac{4, 10}), judge("pruv.recall", frac{1, 2}, frac{1, 2})}}
	var b bytes.Buffer
	r.Summary(&b)
	out := b.String()
	for _, want := range []string{
		"harness claude-code 2.1.0 (model sonnet), 1 fixture(s) × 1 trial(s), rules match/v1",
		"estimate $4.50 – $6.75, budget $20.00; reported $3.21, charged $3.50 over 9 executions",
		"f1       1      completed  3",
		"candidate  correctness   2         0.50 (1/2)",
		"0.33 (1/3)", // candidate correctness recall
		"candidate  architecture  1         0.00 (0/1)  n/a",
		"pruv: retention 1.00 (1/1), decoy rejection 1.00 (1/1)",
		"warning: spend guard: stopped",
		"comparison with baseline 2026-11-01T00:00:00Z (abc123) over [f1]: regressed",
		"candidate.correctness.recall: 0.60 → 0.40 (Δ -0.20, tolerance 0.10) regressed",
		"rerun",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pruv.recall") {
		t.Errorf("summary lists an unchanged metric:\n%s", out)
	}
}

func TestRescoreLeavesOutChangedFixtures(t *testing.T) {
	r := sampleResults(t)
	r.Comparison = &Comparison{Verdict: VerdictImproved}
	notes, err := r.Rescore([]Fixture{{Name: "f1", Key: compareKey("f1"), SourceDigest: "changed"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "source changed") || len(r.Scores.Fixtures) != 0 || r.Comparison != nil {
		t.Errorf("notes %q scores %+v comparison %v", notes, r.Scores.Fixtures, r.Comparison)
	}
	if notes, _ := r.Rescore(nil); len(notes) != 1 || !strings.Contains(notes[0], "no longer exists") {
		t.Errorf("notes %q", notes)
	}
}

func TestSiftTreeIgnoresBenchResults(t *testing.T) {
	ctx := context.Background()
	repo := testutil.GitRepo(t, map[string]string{"go.mod": "module x\n"})
	testutil.WriteFiles(t, repo, map[string]string{"bench/results/20261115T000000Z.json": "{}"})
	root, info, err := SiftTree(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if info.Dirty || info.Commit == "" || root == "" {
		t.Errorf("root %q info %+v", root, info)
	}
	testutil.WriteFiles(t, repo, map[string]string{"notes.txt": "y"})
	if _, info, _ := SiftTree(ctx, repo); !info.Dirty {
		t.Error("an untracked file outside bench/results/ should make the tree dirty")
	}
}

// A rename out of bench/results/ changes a tracked path elsewhere, so it is
// dirty; gitignored paths such as .claude/ never are.
func TestSiftTreeRenamesAndIgnoredPaths(t *testing.T) {
	ctx := context.Background()
	repo := testutil.GitRepo(t, map[string]string{".gitignore": ".claude/\n", "bench/results/a.json": "{}", "bench/results/b.json": "{}"})
	testutil.WriteFiles(t, repo, map[string]string{".claude/notes.md": "local", "bench/results/b.json": "{\"changed\": true}"})
	if _, info, err := SiftTree(ctx, repo); err != nil || info.Dirty {
		t.Errorf("only .claude/ and bench/results/ changed: dirty %v, %v", info.Dirty, err)
	}
	testutil.Git(t, repo, "mv", "bench/results/a.json", "moved.json")
	if _, info, _ := SiftTree(ctx, repo); !info.Dirty {
		t.Error("a rename out of bench/results/ should make the tree dirty")
	}
}
