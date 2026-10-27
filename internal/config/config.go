// Package config loads and validates Sift's YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/olddognewflex/sift/internal/contract"
)

// DefaultFile is the configuration file name looked up in the working
// directory when no --config flag is given.
const DefaultFile = "sift.yaml"

// Known reviewer and harness names. Keeping these fixed (rather than
// discovering them) makes an invalid config fail before any run starts.
// Reviewers come from the registry in contract.Reviewers.
var (
	KnownReviewers = contract.ReviewerNames()
	KnownHarnesses = []string{"claude-code", "kiro"}
)

type Config struct {
	Version int           `yaml:"version" json:"version"`
	Storage StorageConfig `yaml:"storage" json:"storage"`
	Harness HarnessConfig `yaml:"harness" json:"harness"`
	Audit   AuditConfig   `yaml:"audit" json:"audit"`
	Agents  AgentsConfig  `yaml:"agents" json:"agents"`
}

type StorageConfig struct {
	// RunsDir holds audit runs. It must be outside any audited repository.
	RunsDir string `yaml:"runs_dir" json:"runs_dir"`
}

type HarnessConfig struct {
	Name string `yaml:"name" json:"name"`
	// Binary is the harness executable; empty uses the harness's own
	// command (claude, kiro-cli).
	Binary string `yaml:"binary" json:"binary"`
	// Model is passed to the harness; empty means the harness default.
	Model string `yaml:"model" json:"model"`
}

type AuditConfig struct {
	DirtyPolicy            contract.DirtyPolicy `yaml:"dirty_policy" json:"dirty_policy"`
	Reviewers              []string             `yaml:"reviewers" json:"reviewers"`
	MaxFindingsPerReviewer int                  `yaml:"max_findings_per_reviewer" json:"max_findings_per_reviewer"`
	MaxParallelAgents      int                  `yaml:"max_parallel_agents" json:"max_parallel_agents"`
}

type AgentsConfig struct {
	// Dir overrides the built-in agent definitions; empty uses built-ins.
	Dir          string                   `yaml:"dir" json:"dir"`
	Timeout      Duration                 `yaml:"timeout" json:"timeout"`
	MaxBudgetUSD float64                  `yaml:"max_budget_usd" json:"max_budget_usd"`
	Retries      int                      `yaml:"retries" json:"retries"`
	Overrides    map[string]AgentOverride `yaml:"overrides" json:"overrides,omitempty"`
}

type AgentOverride struct {
	Model        string   `yaml:"model" json:"model,omitempty"`
	Timeout      Duration `yaml:"timeout" json:"timeout,omitempty"`
	MaxBudgetUSD float64  `yaml:"max_budget_usd" json:"max_budget_usd,omitempty"`
}

// Duration is a time.Duration written as a Go duration string ("15m").
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// Default returns the documented default configuration.
func Default() Config {
	return Config{
		Version: 1,
		Storage: StorageConfig{RunsDir: "~/.sift/runs"},
		Harness: HarnessConfig{Name: "claude-code"},
		Audit: AuditConfig{
			DirtyPolicy:            contract.DirtyRefuse,
			Reviewers:              contract.ReviewerNames(),
			MaxFindingsPerReviewer: 8,
			MaxParallelAgents:      2,
		},
		Agents: AgentsConfig{
			Timeout:      Duration(20 * time.Minute),
			MaxBudgetUSD: 3,
			Retries:      1,
		},
	}
}

// Load reads path, applying values over the defaults. An empty path loads
// ./sift.yaml when present and the defaults otherwise.
func Load(path string) (Config, string, error) {
	explicit := path != ""
	if !explicit {
		path = DefaultFile
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		cfg := Default()
		return cfg, "", cfg.Validate()
	}
	if err != nil {
		return Config{}, "", fmt.Errorf("read config: %w", err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return Config{}, "", fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, path, nil
}

// Parse decodes YAML over the defaults, rejecting unknown keys, and validates.
func Parse(data []byte) (Config, error) {
	cfg := Default()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, err
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if c.Version != 1 {
		bad("version: unsupported %d (supported: 1)", c.Version)
	}
	if strings.TrimSpace(c.Storage.RunsDir) == "" {
		bad("storage.runs_dir: required")
	}
	if !slices.Contains(KnownHarnesses, c.Harness.Name) {
		bad("harness.name: %q is not one of %v", c.Harness.Name, KnownHarnesses)
	}
	if !contract.Valid(c.Audit.DirtyPolicy) {
		bad("audit.dirty_policy: %q is not one of %v", c.Audit.DirtyPolicy, c.Audit.DirtyPolicy.Enum())
	}
	if len(c.Audit.Reviewers) == 0 {
		bad("audit.reviewers: at least one reviewer is required")
	}
	seen := map[string]bool{}
	for _, r := range c.Audit.Reviewers {
		if !slices.Contains(KnownReviewers, r) {
			bad("audit.reviewers: %q is not one of %v", r, KnownReviewers)
		}
		if seen[r] {
			bad("audit.reviewers: %q is listed twice", r)
		}
		seen[r] = true
	}
	if c.Audit.MaxFindingsPerReviewer < 1 || c.Audit.MaxFindingsPerReviewer > 50 {
		bad("audit.max_findings_per_reviewer: must be between 1 and 50")
	}
	if c.Audit.MaxParallelAgents < 1 || c.Audit.MaxParallelAgents > 8 {
		bad("audit.max_parallel_agents: must be between 1 and 8")
	}
	if c.Agents.Timeout <= 0 {
		bad("agents.timeout: must be positive")
	}
	if c.Agents.MaxBudgetUSD <= 0 {
		bad("agents.max_budget_usd: must be positive")
	}
	if c.Agents.Retries < 0 || c.Agents.Retries > 3 {
		bad("agents.retries: must be between 0 and 3")
	}
	for name, o := range c.Agents.Overrides {
		if o.Timeout < 0 || o.MaxBudgetUSD < 0 {
			bad("agents.overrides.%s: timeout and max_budget_usd must not be negative", name)
		}
	}
	return errors.Join(errs...)
}

// Limits returns the effective model, timeout, and budget for an agent.
func (c Config) Limits(agent string) (model string, timeout time.Duration, budget float64) {
	model, timeout, budget = c.Harness.Model, time.Duration(c.Agents.Timeout), c.Agents.MaxBudgetUSD
	if o, ok := c.Agents.Overrides[agent]; ok {
		if o.Model != "" {
			model = o.Model
		}
		if o.Timeout > 0 {
			timeout = time.Duration(o.Timeout)
		}
		if o.MaxBudgetUSD > 0 {
			budget = o.MaxBudgetUSD
		}
	}
	return model, timeout, budget
}

// RunsDir returns the absolute runs directory, expanding a leading "~".
func (c Config) RunsDir() (string, error) {
	return ExpandPath(c.Storage.RunsDir)
}

// ExpandPath expands a leading "~/" and makes p absolute.
func ExpandPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", p, err)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}

// Template is the file written by `sift init`. Every key is documented and
// set to its default; tests assert that it parses to Default().
const Template = `# Sift configuration. All values shown are the defaults.
version: 1

storage:
  # Where audit runs (manifests, artifacts, reports, source snapshots) are
  # stored. Must be outside any repository you audit.
  runs_dir: ~/.sift/runs

harness:
  # Agent execution harness. Supported:
  #   claude-code - Claude Code (enforces max_budget_usd)
  #   kiro        - Kiro CLI (cannot enforce max_budget_usd; see ADR 0005)
  name: claude-code
  # Harness executable, resolved via PATH when not absolute. Empty uses the
  # harness's own command: claude or kiro-cli.
  binary: ""
  # Model passed to the harness, in the harness's own naming (e.g. sonnet
  # for claude-code, claude-sonnet-5.5 for kiro). Empty uses the harness
  # default.
  model: ""

audit:
  # What to do when the repository has uncommitted changes:
  #   refuse  - stop with an error (default)
  #   head    - audit the last commit only; uncommitted changes are ignored
  #   include - audit the working tree; the diff is recorded in the run
  dirty_policy: refuse
  # Specialist reviewers to run. Supported: architecture, correctness.
  reviewers: [architecture, correctness]
  # Upper bound on candidate findings each reviewer may report.
  max_findings_per_reviewer: 8
  # Agent executions allowed to run at the same time.
  max_parallel_agents: 2

agents:
  # Directory of agent definition overrides (<name>.md). Empty uses the
  # definitions built into this sift binary.
  dir: ""
  # Wall-clock limit for one agent execution.
  timeout: 20m
  # Spending limit for one agent execution, enforced by the harness.
  # The kiro harness cannot enforce it; there, only the timeout bounds spend.
  max_budget_usd: 3
  # Extra attempts after a process failure or invalid structured output.
  # Timeouts, cancellations, and budget exhaustion are not retried.
  retries: 1
  # Per-agent overrides of model, timeout, and max_budget_usd, e.g.:
  # overrides:
  #   pruv: {model: opus, timeout: 10m}
  overrides: {}
`
