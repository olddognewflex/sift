package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTemplateMatchesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(Template))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Agents.Overrides = map[string]AgentOverride{} // template writes `overrides: {}`
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("template drifted from defaults:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestParseOverridesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
version: 1
audit:
  reviewers: [correctness]
  dirty_policy: include
agents:
  timeout: 90s
  overrides:
    pruv: {model: opus, max_budget_usd: 5}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Audit.Reviewers; len(got) != 1 || got[0] != "correctness" {
		t.Errorf("reviewers = %v", got)
	}
	if cfg.Audit.MaxFindingsPerReviewer != 8 {
		t.Errorf("unset value should keep default, got %d", cfg.Audit.MaxFindingsPerReviewer)
	}
	model, timeout, budget := cfg.Limits("pruv")
	if model != "opus" || timeout != 90*time.Second || budget != 5 {
		t.Errorf("pruv limits = %q %v %v", model, timeout, budget)
	}
	model, _, budget = cfg.Limits("discovery")
	if model != "" || budget != 3 {
		t.Errorf("discovery limits = %q %v", model, budget)
	}
}

func TestEmptyFileIsDefaults(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil || !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":      "version: 1\nbogus: true\n",
		"version":          "version: 2\n",
		"dirty policy":     "audit: {dirty_policy: sometimes}\n",
		"reviewer":         "audit: {reviewers: [security]}\n",
		"no reviewers":     "audit: {reviewers: []}\n",
		"duplicate":        "audit: {reviewers: [correctness, correctness]}\n",
		"max findings":     "audit: {max_findings_per_reviewer: 0}\n",
		"parallel":         "audit: {max_parallel_agents: 100}\n",
		"duration":         "agents: {timeout: soon}\n",
		"negative timeout": "agents: {timeout: -1m}\n",
		"budget":           "agents: {max_budget_usd: 0}\n",
		"harness":          "harness: {name: other}\n",
		"retries":          "agents: {retries: 9}\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatalf("expected error for %q", doc)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cfg, path, err := Load("")
	if err != nil || path != "" || !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("no file: got %v %q %v", cfg, path, err)
	}
	if _, _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("explicit missing file should fail")
	}
	if err := os.WriteFile(DefaultFile, []byte("audit: {max_findings_per_reviewer: 3}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, path, err = Load("")
	if err != nil || path != DefaultFile || cfg.Audit.MaxFindingsPerReviewer != 3 {
		t.Fatalf("./sift.yaml: got %v %q %v", cfg.Audit, path, err)
	}
	if err := os.WriteFile(DefaultFile, []byte("audit: {max_findings_per_reviewer: x}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(""); err == nil || !strings.Contains(err.Error(), DefaultFile) {
		t.Fatalf("invalid file error should name the file, got %v", err)
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := ExpandPath("~/.sift/runs")
	if err != nil || got != filepath.Join(home, ".sift/runs") {
		t.Fatalf("got %q %v", got, err)
	}
	got, _ = ExpandPath("rel")
	if !filepath.IsAbs(got) {
		t.Fatalf("not absolute: %q", got)
	}
}
