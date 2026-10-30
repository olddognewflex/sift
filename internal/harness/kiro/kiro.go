// Package kiro runs Sift agents with the Kiro CLI in non-interactive mode.
//
// Kiro has no read-only or no-project-config switch, so this adapter builds
// the isolation itself. Every behavior was verified against kiro-cli 2.34.2
// (see docs/decisions/0005-kiro-harness.md):
//
//   - The agent definition is written to a private KIRO_HOME under a random
//     name, so neither the user's global Kiro config nor a planted
//     .kiro/agents file in the audited repository can replace it.
//   - The process runs in an empty private workspace, not the snapshot.
//     Kiro starts MCP servers from <cwd>/.kiro/settings/mcp.json and loads
//     AGENTS.md and steering files from <cwd> regardless of agent config, so
//     the snapshot must never be the workspace. The agent reaches the
//     snapshot only through read tools scoped to its absolute path.
//   - Kiro has no schema enforcement; the output is validated here.
//   - Kiro has no spend limit; this is reported as unenforced.
package kiro

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/olddognewflex/sift/internal/harness"
	"github.com/olddognewflex/sift/internal/schema"
)

const (
	Name          = "kiro"
	DefaultBinary = "kiro-cli"
	// engine is the agent engine every behavior was verified on.
	engine = "v2"
	// defaultModel is Kiro's own default, which picks a model per task.
	// Set harness.model for runs that should be comparable.
	defaultModel = "auto"
)

// tools are the only tools exposed to agents: file reading and searching.
var tools = []string{"read", "glob", "grep"}

// requiredFlags must all appear in `kiro-cli chat --help`.
var requiredFlags = []string{"--no-interactive", "--agent", "--agent-engine", "--model", "--trust-tools", "--output-format"}

// requiredSetting must appear in `kiro-cli settings list --all`.
const requiredSetting = "chat.disableInheritingDefaultResources"

const killGrace = 10 * time.Second

type Harness struct {
	Binary string
}

func New(binary string) *Harness {
	if binary == "" {
		binary = DefaultBinary
	}
	// A path like ./bin/x is relative to Sift's working directory, but the
	// harness runs elsewhere; resolve it now. Bare names use PATH.
	if strings.ContainsRune(binary, filepath.Separator) && !filepath.IsAbs(binary) {
		if abs, err := filepath.Abs(binary); err == nil {
			binary = abs
		}
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
			"only the read, glob and grep tools are available; no shell, write or web tools",
			"file tools are scoped to the snapshot's absolute path; other paths need approval, which non-interactive mode denies",
			"no tool is pre-trusted (--trust-tools=)",
			"the agent runs in an empty private workspace with a private KIRO_HOME: the audited repository's .kiro config, MCP servers, steering and AGENTS.md, and the user's global Kiro agents, steering, skills and MCP servers are not loaded",
			"the agent definition has a random name, so no planted agent file can replace it",
			"wall-clock timeout",
			"the snapshot is outside the target repository",
		},
		Unenforced: []string{
			"no per-execution spend limit: Kiro meters credits but cannot stop at a budget; spend is bounded only by the timeout",
			"no native structured output: the schema is given in the prompt and Sift validates the result",
			"no operating-system sandbox: the harness process runs with the invoking user's privileges and network access (needed to reach the model API)",
			"path scoping is implemented by the harness, not by Sift; it was verified empirically, not formally",
			"agents read repository content that may contain prompt-injection text; Pruv and Sift's evidence checks mitigate but cannot eliminate this",
		},
	}
	path, err := exec.LookPath(h.Binary)
	if err != nil {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: fmt.Sprintf("kiro binary %q not found", h.Binary), Err: err}
	}
	info.Binary = path
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Probe in an empty directory: Kiro reads workspace config from its cwd,
	// which is usually the repository about to be audited.
	empty, err := os.MkdirTemp("", "sift-kiro-probe-")
	if err != nil {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: "create probe directory", Err: err}
	}
	defer os.RemoveAll(empty)
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(pctx, path, args...)
		cmd.Dir = empty
		return cmd.CombinedOutput()
	}
	out, err := run("--version")
	if err != nil {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: "kiro-cli --version failed", Err: err}
	}
	if !strings.Contains(strings.ToLower(string(out)), "kiro") {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: fmt.Sprintf("%s is not kiro-cli (--version printed %q); set harness.binary to kiro-cli or leave it empty", path, strings.TrimSpace(string(out)))}
	}
	if info.Version = versionPattern.FindString(string(out)); info.Version == "" {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: fmt.Sprintf("cannot parse version from %q", strings.TrimSpace(string(out)))}
	}
	help, err := run("chat", "--help")
	if err != nil {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: "kiro-cli chat --help failed", Err: err}
	}
	var missing []string
	for _, f := range requiredFlags {
		if !regexp.MustCompile(regexp.QuoteMeta(f) + `\b`).Match(help) {
			missing = append(missing, f)
		}
	}
	if !bytes.Contains(help, []byte("stream-json")) {
		missing = append(missing, "--output-format stream-json")
	}
	if settings, err := run("settings", "list", "--all"); err != nil || !bytes.Contains(settings, []byte(requiredSetting)) {
		missing = append(missing, "setting "+requiredSetting)
	}
	if len(missing) > 0 {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: fmt.Sprintf("kiro-cli %s lacks required features %v", info.Version, missing)}
	}
	if out, err := run("whoami"); err != nil {
		return info, &harness.Error{Kind: harness.ErrUnsupported, Msg: "kiro-cli is not logged in (run `kiro-cli login`): " + strings.TrimSpace(string(out)), Err: err}
	}
	info.Capabilities = append([]string{"tool-allowlist", "restricted-file-scope", "private-workspace"}, requiredFlags...)
	return info, nil
}

// AgentConfig is the Kiro agent definition written for each execution.
type AgentConfig struct {
	Name           string                    `json:"name"`
	Description    string                    `json:"description"`
	Prompt         string                    `json:"prompt"`
	Tools          []string                  `json:"tools"`
	AllowedTools   []string                  `json:"allowedTools"`
	Resources      []string                  `json:"resources"`
	IncludeMcpJSON bool                      `json:"includeMcpJson"`
	MCPServers     map[string]any            `json:"mcpServers"`
	Hooks          map[string]any            `json:"hooks"`
	ToolsSettings  map[string]map[string]any `json:"toolsSettings"`
}

// Agent returns the agent definition for req, whose snapshot is at root.
// Exposed for tests.
func Agent(name, root string, req harness.Request) AgentConfig {
	scope := map[string]any{"allowedPaths": []string{root, root + "/**"}}
	settings := map[string]map[string]any{}
	for _, t := range tools {
		settings[t] = scope
	}
	return AgentConfig{
		Name:          name,
		Description:   "Sift " + req.Agent + " agent",
		Prompt:        req.Instructions + pathNote(root),
		Tools:         tools,
		AllowedTools:  []string{},
		Resources:     []string{},
		MCPServers:    map[string]any{},
		Hooks:         map[string]any{},
		ToolsSettings: settings,
	}
}

// pathNote adapts instructions that speak of "the working directory": in
// Kiro the snapshot is reached by absolute path, never as the cwd.
func pathNote(root string) string {
	return fmt.Sprintf(`

## Where the repository is

The repository snapshot (called your working directory above) is at:

    %s

Use absolute paths under that directory for every tool call. Report every
file path relative to it, never as an absolute path.`, root)
}

// input is the user message: the task followed by the output contract.
func input(req harness.Request) string {
	return req.Prompt + `

## Output format

Your final message must be exactly one JSON object that conforms to the JSON
Schema below: no prose before or after it, and no code fences. Every
required field must be present and no other fields are allowed.

` + string(req.OutputSchema)
}

func model(req harness.Request) string {
	if req.Model == "" {
		return defaultModel
	}
	return req.Model
}

// Args returns the command-line arguments for an execution. Exposed for tests.
func Args(agent string, req harness.Request) []string {
	return []string{
		"chat", "--no-interactive",
		"--agent-engine", engine,
		"--agent", agent,
		"--trust-tools=",
		"--model", model(req),
		"--output-format", "stream-json",
	}
}

func (h *Harness) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	if req.WorkDir == "" || len(req.OutputSchema) == 0 || req.Timeout <= 0 {
		return harness.Result{}, &harness.Error{Kind: harness.ErrUnsupported, Msg: "request needs WorkDir, OutputSchema and Timeout"}
	}
	root, err := filepath.Abs(req.WorkDir)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return harness.Result{}, &harness.Error{Kind: harness.ErrUnsupported, Msg: "resolve snapshot", Err: err}
	}
	private, err := os.MkdirTemp("", "sift-kiro-")
	if err != nil {
		return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: "create private kiro home", Err: err}
	}
	defer os.RemoveAll(private)
	if dir := kiroAncestor(private); dir != "" {
		return harness.Result{}, &harness.Error{Kind: harness.ErrUnsupported, Msg: fmt.Sprintf("the temp directory %s is inside %s, which has a .kiro folder Kiro could load as workspace config; set TMPDIR elsewhere", private, dir)}
	}
	name := "sift-" + randomHex(8)
	agent := Agent(name, root, req)
	home, workspace, err := setupPrivate(private, agent)
	if err != nil {
		return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: "prepare private kiro home", Err: err}
	}

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, h.Binary, Args(name, req)...)
	cmd.Dir = workspace
	cmd.Env = append(withoutEnv(os.Environ(), "KIRO_HOME"), "KIRO_HOME="+home)
	cmd.Stdin = strings.NewReader(input(req))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return terminate(cmd) }
	cmd.WaitDelay = killGrace

	start := time.Now()
	runErr := cmd.Run()
	if cmd.Process != nil {
		killGroup(cmd)
	}
	meta := harness.Meta{Duration: time.Since(start), ExitCode: cmd.ProcessState.ExitCode(), Model: model(req)}
	if req.LogDir != "" {
		if err := writeLogs(req.LogDir, agent, stdout.Bytes(), stderr.Bytes()); err != nil {
			return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: "persist harness output", Meta: meta, Err: err}
		}
	}
	switch {
	case ctx.Err() != nil:
		return harness.Result{}, &harness.Error{Kind: harness.ErrCanceled, Msg: "execution canceled", Meta: meta, Err: ctx.Err()}
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return harness.Result{}, &harness.Error{Kind: harness.ErrTimeout, Msg: fmt.Sprintf("no result within %s", req.Timeout), Meta: meta}
	case cmd.ProcessState == nil:
		return harness.Result{}, &harness.Error{Kind: harness.ErrUnsupported, Msg: "start kiro-cli", Meta: meta, Err: runErr}
	}
	return parse(stdout.Bytes(), stderr.Bytes(), runErr, req.OutputSchema, meta)
}

// setupPrivate writes the private KIRO_HOME (agent definition and settings)
// and creates the empty workspace.
func setupPrivate(dir string, agent AgentConfig) (home, workspace string, err error) {
	home, workspace = filepath.Join(dir, "home"), filepath.Join(dir, "workspace")
	settings := map[string]any{
		// Custom agents otherwise inherit global and workspace steering,
		// skills, and AGENTS.md.
		requiredSetting: true,
	}
	files := map[string]any{
		filepath.Join(home, "agents", agent.Name+".json"): agent,
		filepath.Join(home, "settings", "cli.json"):       settings,
	}
	for path, v := range files {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return "", "", err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", "", err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return "", "", err
		}
	}
	return home, workspace, os.MkdirAll(workspace, 0o700)
}

// event is one line of `--output-format stream-json`.
type event struct {
	Type string `json:"type"`
	Data struct {
		SessionID          string `json:"sessionId"`
		Status             string `json:"status"`
		StopReason         string `json:"stopReason"`
		FinalText          string `json:"finalText"`
		FinalTextTruncated bool   `json:"finalTextTruncated"`
		Stage              string `json:"stage"`
		Message            string `json:"message"`
		TurnDurationMs     *int64 `json:"turnDurationMs"`
	} `json:"data"`
}

// deniedPrefix starts each stderr line reporting a refused tool call.
const deniedPrefix = "[denied]"

func parse(stdout, stderr []byte, runErr error, schemaDoc []byte, meta harness.Meta) (harness.Result, error) {
	var finished, failed *event
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var ev event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Data.SessionID != "" {
			meta.SessionID = ev.Data.SessionID
		}
		switch ev.Type {
		case "metadata":
			if ev.Data.TurnDurationMs != nil {
				meta.NumTurns++
			}
		case "runFinished":
			finished = &ev
		case "runError":
			failed = &ev
		}
	}
	if err := sc.Err(); err != nil {
		return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: "read harness output", Meta: meta, Err: err}
	}
	for line := range strings.SplitSeq(string(stderr), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), deniedPrefix) {
			meta.PermissionDenials++
		}
	}
	switch {
	case failed != nil && failed.Data.Stage == "init":
		// Setup failures (unknown model, agent not loadable) repeat on retry.
		return harness.Result{}, &harness.Error{Kind: harness.ErrUnsupported, Msg: fmt.Sprintf("harness could not start the session: %s", failed.Data.Message), Meta: meta, Err: runErr}
	case failed != nil:
		return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: fmt.Sprintf("harness reported an error at %s: %s", failed.Data.Stage, failed.Data.Message), Meta: meta, Err: runErr}
	case finished == nil:
		msg := "harness did not report a result"
		if runErr != nil {
			msg = fmt.Sprintf("harness exited with code %d", meta.ExitCode)
		}
		return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: msg + tail(stderr), Meta: meta, Err: runErr}
	case finished.Data.Status != "success":
		return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: fmt.Sprintf("harness reported %s (%s)", finished.Data.Status, finished.Data.StopReason), Meta: meta}
	case finished.Data.FinalTextTruncated:
		return harness.Result{}, &harness.Error{Kind: harness.ErrInvalidOutput, Msg: "final message was truncated", Meta: meta}
	}
	out, err := extractJSON(finished.Data.FinalText, schemaDoc)
	if err != nil {
		return harness.Result{}, &harness.Error{Kind: harness.ErrInvalidOutput, Msg: "final message is not valid structured output: " + truncate(finished.Data.FinalText, 300), Meta: meta, Err: err}
	}
	return harness.Result{Output: out, Meta: meta}, nil
}

// extractJSON returns the JSON object in text that matches schemaDoc. Models
// asked for bare JSON sometimes wrap it in a code fence or prose, and the
// prose may itself contain braces (code, examples), so every top-level object
// in the text is a candidate; the last one that validates wins. Objects
// nested inside another are never candidates: they are parts of an answer,
// not the answer, even when they happen to satisfy the schema.
//
// If none validates, the error describes the last top-level object, since
// that is the model's answer. When that object does not parse, the error says
// so rather than quoting a schema error from a fragment inside it.
func extractJSON(text string, schemaDoc []byte) (json.RawMessage, error) {
	s := strings.TrimSpace(text)
	type object struct {
		// end is the end of a well-formed object, or for a broken one the
		// index of the byte where parsing broke (len(s) if the text ran out).
		start, end int
		raw        json.RawMessage
		err        error // decode error, if the object does not parse
		eof        bool  // broken because the text ended
		// close is, for an object broken by a syntax error, the end of its
		// closing brace found by looseClose, or -1 if it never closes.
		close int
	}
	// Collect the top-level objects in order. last is the latest one; a
	// brace inside it may be nested in it.
	var top []object
	var last *object
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		if last != nil && i < last.end && (last.err == nil || last.eof) {
			continue // inside a well-formed object, or one cut off by the end
		}
		o := object{start: i}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		var se *json.SyntaxError
		switch err := dec.Decode(&o.raw); {
		case err == nil:
			o.end = i + int(dec.InputOffset())
		case errors.As(err, &se):
			o.end, o.err, o.close = i+int(se.Offset)-1, err, -1 // Offset counts the bad byte
		default: // unexpected EOF: everything after the brace parsed
			o.end, o.err, o.eof = len(s), err, true
		}
		// A brace in prose or code ("{the}", "func main() {") is not an
		// attempt at JSON; a broken object that opens with a key is.
		if o.err != nil && !strings.HasPrefix(strings.TrimLeft(s[i+1:], " \t\r\n"), `"`) {
			continue
		}
		if o.err != nil && !o.eof {
			o.close = looseClose(s, i)
		}
		// Inside an object broken by a syntax error, an object that closes
		// before the broken one's own closing brace is part of it. One whose
		// closing brace is that brace is a restart that the loose scan
		// misread as the broken object's end. If the broken object never
		// closes, an object that closes at or before the break is part of
		// it, even if it was meant as a restarted answer: the two look the
		// same. One that runs past the break is a restart.
		if last != nil {
			if last.close > 0 && i < last.close && o.end < last.close {
				continue
			}
			if last.close < 0 && i < last.end && o.end <= last.end {
				continue
			}
		}
		top = append(top, o)
		last = &top[len(top)-1]
	}

	var firstErr error
	for k := len(top) - 1; k >= 0; k-- {
		o := top[k]
		err := o.err
		if err != nil {
			err = fmt.Errorf("malformed JSON object: %w", err)
		} else if err = schema.Validate(schemaDoc, o.raw); err == nil {
			return o.raw, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		return nil, errors.New("no JSON object in final message")
	}
	return nil, firstErr
}

// looseClose returns the index just past the brace that closes the object
// opening at s[i], or -1 if it never closes. Unlike a JSON parser it reads
// past some syntax errors, so it can find the end of a broken answer:
//   - Inside a string, a quote ends the string only if the next non-space
//     byte could follow a string in JSON (, : } ] or the end of the text),
//     which always holds in valid JSON. Unescaped quotes in a string are
//     read as part of it.
//   - Outside strings, a bare word other than true, false or null is
//     allowed only where a value or key could stand (followed by , : } ]).
//
// It gives up where the text cannot be JSON past this point: prose
// punctuation, or an object or array opening where no value can stand
// (anywhere but after a colon, or first or after a comma in an array). A
// restarted answer after a broken one, or a stray brace in prose after the
// answer, then cannot "close" the broken object.
func looseClose(s string, i int) int {
	nextIs := func(j int, set string) bool {
		rest := strings.TrimLeft(s[j:], " \t\r\n")
		return rest != "" && strings.ContainsRune(set, rune(rest[0]))
	}
	var stack []byte // open containers, '{' or '['
	prev := byte(0)  // previous token: ':' ',' '{' '[' or 'v' for a value
	inString := false
	for j := i; j < len(s); j++ {
		c := s[j]
		if inString {
			switch {
			case c == '\\':
				j++
			case c == '"':
				if inString = strings.TrimLeft(s[j+1:], " \t\r\n") != "" && !nextIs(j+1, ",:}]"); !inString {
					prev = 'v'
				}
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
		case c == '{' || c == '[':
			inArray := len(stack) > 0 && stack[len(stack)-1] == '['
			if !(prev == 0 || prev == ':' || inArray && (prev == '[' || prev == ',')) {
				return -1
			}
			stack, prev = append(stack, c), c
		case c == '}' || c == ']':
			stack, prev = stack[:len(stack)-1], 'v'
			if len(stack) == 0 {
				if c == '}' {
					return j + 1
				}
				return -1
			}
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
		case c == ',' || c == ':':
			prev = c
		case c == '-' || c >= '0' && c <= '9':
			for j+1 < len(s) && strings.IndexByte("0123456789eE.+-", s[j+1]) >= 0 {
				j++
			}
			prev = 'v'
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_':
			k := j
			for k < len(s) && (s[k] >= 'a' && s[k] <= 'z' || s[k] >= 'A' && s[k] <= 'Z' || s[k] >= '0' && s[k] <= '9' || s[k] == '_') {
				k++
			}
			if w := s[j:k]; w != "true" && w != "false" && w != "null" && !nextIs(k, ",:}]") {
				return -1
			}
			j, prev = k-1, 'v'
		default:
			return -1
		}
	}
	return -1
}

// kiroAncestor returns the nearest ancestor of dir that contains a .kiro
// folder, or "". Kiro may treat such a folder as workspace config.
func kiroAncestor(dir string) string {
	for d := filepath.Dir(dir); ; d = filepath.Dir(d) {
		if fi, err := os.Stat(filepath.Join(d, ".kiro")); err == nil && fi.IsDir() {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

func withoutEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

func writeLogs(dir string, agent AgentConfig, stdout, stderr []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	agentJSON, err := json.MarshalIndent(agent, "", "  ")
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{
		"harness-agent.json":   agentJSON,
		"harness-stdout.jsonl": stdout,
		"harness-stderr.log":   stderr,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
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
