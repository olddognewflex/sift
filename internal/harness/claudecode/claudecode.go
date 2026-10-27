// Package claudecode runs Sift agents with the Claude Code CLI in
// non-interactive print mode.
//
// Every flag used here was verified against Claude Code 2.1.311 (see
// docs/decisions/0002-claude-code-harness.md). Probe checks that the
// installed CLI still advertises each one before any run starts.
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/olddognewflex/sift/internal/harness"
)

const (
	Name          = "claude-code"
	DefaultBinary = "claude"
)

// tools are the only built-in tools exposed to agents: file reading and
// searching. No shell, no file writes, no web access.
var tools = []string{"Read", "Grep", "Glob"}

// requiredFlags must all appear in `claude --help`.
var requiredFlags = []string{
	"--print", "--output-format", "--json-schema", "--tools", "--restricted",
	"--safe-mode", "--strict-mcp-config", "--permission-mode",
	"--permission-prompts", "--no-session-persistence", "--max-budget-usd",
	"--system-prompt", "--model",
}

// killGrace is how long the harness gets to exit after SIGTERM.
const killGrace = 10 * time.Second

type Harness struct {
	Binary string
}

// New returns a harness running binary; empty selects DefaultBinary.
func New(binary string) *Harness {
	if binary == "" {
		binary = DefaultBinary
	}
	return &Harness{Binary: binary}
}

var versionPattern = regexp.MustCompile(`(\d+\.\d+\.\d+)`)

func (h *Harness) Probe(ctx context.Context) (harness.Info, error) {
	info := harness.Info{
		Name:   Name,
		Binary: h.Binary,
		Tools:  tools,
		Enforcement: []string{
			"only the Read, Grep and Glob tools are available (--tools); no shell, write or web tools",
			"file tools are confined to the snapshot directory (--restricted); out-of-scope reads are denied and counted",
			"anything that would prompt for permission is denied (--permission-mode dontAsk --permission-prompts none)",
			"user/project settings, hooks, plugins, CLAUDE.md files and MCP servers are not loaded (--restricted --safe-mode --strict-mcp-config)",
			"per-execution spend limit (--max-budget-usd) and wall-clock timeout",
			"the agent's working directory is a snapshot outside the target repository",
		},
		Unenforced: []string{
			"no operating-system sandbox: the harness process runs with the invoking user's privileges and network access (needed to reach the model API)",
			"path confinement is implemented by the harness, not by Sift; it was verified empirically, not formally",
			"agents read repository content that may contain prompt-injection text; Pruv and Sift's evidence checks mitigate but cannot eliminate this",
		},
	}
	path, err := exec.LookPath(h.Binary)
	if err != nil {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: fmt.Sprintf("claude binary %q not found", h.Binary), Err: err}
	}
	info.Binary = path
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(pctx, path, "--version").Output()
	if err != nil {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: "claude --version failed", Err: err}
	}
	if !strings.Contains(string(out), "Claude Code") {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: fmt.Sprintf("%s is not Claude Code (--version printed %q); set harness.binary to claude or leave it empty", path, strings.TrimSpace(string(out)))}
	}
	info.Version = versionPattern.FindString(string(out))
	if info.Version == "" {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: fmt.Sprintf("cannot parse version from %q", strings.TrimSpace(string(out)))}
	}
	help, err := exec.CommandContext(pctx, path, "--help").Output()
	if err != nil {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: "claude --help failed", Err: err}
	}
	var missing []string
	for _, f := range requiredFlags {
		if !regexp.MustCompile(regexp.QuoteMeta(f) + `\b`).Match(help) {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: fmt.Sprintf("claude %s lacks required flags %v", info.Version, missing)}
	}
	info.Capabilities = append([]string{"structured-output", "tool-allowlist", "restricted-file-scope", "safe-mode", "budget-limit"}, requiredFlags...)
	return info, nil
}

// Args returns the command-line arguments for req. Exposed for tests.
func Args(req harness.Request) []string {
	args := []string{
		"--print",
		"--output-format", "json",
		"--json-schema", string(req.OutputSchema),
		"--tools", strings.Join(tools, ","),
		"--restricted",
		"--safe-mode",
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--no-session-persistence",
		"--system-prompt", req.Instructions,
	}
	if req.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(req.MaxBudgetUSD, 'f', -1, 64))
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	return args
}

func (h *Harness) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	if req.WorkDir == "" || len(req.OutputSchema) == 0 || req.Timeout <= 0 {
		return harness.Result{}, &harness.Error{Kind: harness.ErrUnsupported, Msg: "request needs WorkDir, OutputSchema and Timeout"}
	}
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, h.Binary, Args(req)...)
	cmd.Dir = req.WorkDir
	cmd.Stdin = strings.NewReader(req.Prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return terminate(cmd) }
	cmd.WaitDelay = killGrace

	start := time.Now()
	runErr := cmd.Run()
	if cmd.Process != nil {
		killGroup(cmd) // reap anything the harness left behind
	}
	meta := harness.Meta{Duration: time.Since(start), ExitCode: cmd.ProcessState.ExitCode()}
	if req.LogDir != "" {
		if err := writeLogs(req.LogDir, stdout.Bytes(), stderr.Bytes()); err != nil {
			return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: "persist harness output", Meta: meta, Err: err}
		}
	}

	switch {
	case ctx.Err() != nil:
		return harness.Result{}, &harness.Error{Kind: harness.ErrCanceled, Msg: "execution canceled", Meta: meta, Err: ctx.Err()}
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return harness.Result{}, &harness.Error{Kind: harness.ErrTimeout, Msg: fmt.Sprintf("no result within %s", req.Timeout), Meta: meta}
	}
	return parse(stdout.Bytes(), stderr.Bytes(), runErr, meta)
}

type envelope struct {
	Type              string                     `json:"type"`
	Subtype           string                     `json:"subtype"`
	IsError           bool                       `json:"is_error"`
	Result            string                     `json:"result"`
	StructuredOutput  json.RawMessage            `json:"structured_output"`
	SessionID         string                     `json:"session_id"`
	TotalCostUSD      float64                    `json:"total_cost_usd"`
	NumTurns          int                        `json:"num_turns"`
	PermissionDenials []json.RawMessage          `json:"permission_denials"`
	ModelUsage        map[string]json.RawMessage `json:"modelUsage"`
	Errors            []string                   `json:"errors"`
	TerminalReason    string                     `json:"terminal_reason"`
}

// parse interprets the harness's JSON result envelope. Exposed via tests.
func parse(stdout, stderr []byte, runErr error, meta harness.Meta) (harness.Result, error) {
	var env envelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil || env.Type != "result" {
		msg := "harness did not produce a JSON result"
		if runErr != nil {
			msg = fmt.Sprintf("harness exited with code %d", meta.ExitCode)
		}
		return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: msg + tail(stderr), Meta: meta, Err: runErr}
	}
	meta.SessionID = env.SessionID
	meta.CostUSD = env.TotalCostUSD
	meta.NumTurns = env.NumTurns
	meta.PermissionDenials = len(env.PermissionDenials)
	models := make([]string, 0, len(env.ModelUsage))
	for m := range env.ModelUsage {
		models = append(models, m)
	}
	slices.Sort(models)
	meta.Model = strings.Join(models, ",")

	if env.IsError || env.Subtype != "success" {
		kind := harness.ErrProcess
		if env.Subtype == "error_max_budget_usd" {
			kind = harness.ErrBudget
		}
		detail := strings.Join(env.Errors, "; ")
		if detail == "" {
			detail = truncate(env.Result, 500)
		}
		return harness.Result{}, &harness.Error{Kind: kind, Msg: fmt.Sprintf("harness reported %s (%s): %s", env.Subtype, env.TerminalReason, detail), Meta: meta}
	}
	out := bytes.TrimSpace(env.StructuredOutput)
	if len(out) == 0 || string(out) == "null" {
		return harness.Result{}, &harness.Error{Kind: harness.ErrInvalidOutput, Msg: "harness returned no structured output; final text: " + truncate(env.Result, 300), Meta: meta}
	}
	return harness.Result{Output: json.RawMessage(out), Meta: meta}, nil
}

func writeLogs(dir string, stdout, stderr []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "harness-stdout.json"), stdout, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "harness-stderr.log"), stderr, 0o644)
}

func tail(stderr []byte) string {
	s := strings.TrimSpace(string(stderr))
	if s == "" {
		return ""
	}
	if len(s) > 500 {
		s = "…" + s[len(s)-500:]
	}
	return ": " + s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
