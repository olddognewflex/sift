package kiro

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/harness"
)

// fakeKiro writes an executable script standing in for kiro-cli. Chat runs
// record their cwd, KIRO_HOME contents and stdin under $FAKE_KIRO_REC.
func fakeKiro(t *testing.T, chat string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "kiro-cli")
	script := `#!/bin/sh
case "$1" in
--version) echo "kiro-cli 2.34.2"; exit 0;;
whoami) echo "Logged in"; exit 0;;
settings) echo "chat.disableInheritingDefaultResources"; exit 0;;
chat) if [ "$2" = "--help" ]; then echo "--no-interactive --agent --agent-engine --model --trust-tools --output-format stream-json"; exit 0; fi;;
esac
if [ -n "$FAKE_KIRO_REC" ]; then
  pwd -P > "$FAKE_KIRO_REC/cwd"; ls -A > "$FAKE_KIRO_REC/cwd-contents"
  echo "$KIRO_HOME" > "$FAKE_KIRO_REC/home"
  cat "$KIRO_HOME"/agents/*.json > "$FAKE_KIRO_REC/agent.json"
  cat "$KIRO_HOME/settings/cli.json" > "$FAKE_KIRO_REC/settings.json"
  echo "$@" > "$FAKE_KIRO_REC/args"
  cat > "$FAKE_KIRO_REC/stdin"
fi
` + chat + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// finish emits a stream-json run whose final message is text.
func finish(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "runFinished", "data": map[string]any{
		"sessionId": "s-1", "status": "success", "stopReason": "end_turn", "finalText": text, "finalTextTruncated": false,
	}})
	return `echo '{"type":"runStarted","data":{"engine":"v2"}}'
echo '{"type":"metadata","data":{"sessionId":"s-1","turnDurationMs":10}}'
printf '%s\n' '` + string(b) + `'`
}

const schemaDoc = `{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`

func request(t *testing.T) harness.Request {
	return harness.Request{
		Agent: "pruv", Instructions: "be careful", Prompt: "validate", WorkDir: t.TempDir(),
		OutputSchema: []byte(schemaDoc), Timeout: 10 * time.Second, Model: "claude-haiku-5.5",
		LogDir: filepath.Join(t.TempDir(), "logs"),
	}
}

func record(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("FAKE_KIRO_REC", dir)
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestArgs(t *testing.T) {
	args := strings.Join(Args("sift-x", harness.Request{Model: "m"}), " ")
	for _, want := range []string{"chat --no-interactive", "--agent-engine v2", "--agent sift-x", "--trust-tools= ", "--model m", "--output-format stream-json"} {
		if !strings.Contains(args+" ", want) {
			t.Errorf("args missing %q: %s", want, args)
		}
	}
	for _, bad := range []string{"--trust-all-tools", " -a", "--trust-workspace"} {
		if strings.Contains(args, bad) {
			t.Errorf("args contain %q", bad)
		}
	}
	if !strings.Contains(strings.Join(Args("x", harness.Request{}), " "), "--model auto") {
		t.Error("empty model must select auto")
	}
}

func TestAgentIsReadOnlyAndScoped(t *testing.T) {
	a := Agent("sift-x", "/snap", harness.Request{Instructions: "do it"})
	if !slices.Equal(a.Tools, []string{"read", "glob", "grep"}) || len(a.AllowedTools) != 0 {
		t.Fatalf("tools %v allowed %v", a.Tools, a.AllowedTools)
	}
	if a.IncludeMcpJSON || len(a.MCPServers) != 0 || len(a.Hooks) != 0 || len(a.Resources) != 0 {
		t.Fatalf("agent loads extra config: %+v", a)
	}
	for _, tool := range a.Tools {
		paths := a.ToolsSettings[tool]["allowedPaths"]
		if !slices.Equal(paths.([]string), []string{"/snap", "/snap/**"}) {
			t.Errorf("%s scoped to %v", tool, paths)
		}
	}
	if !strings.HasPrefix(a.Prompt, "do it") || !strings.Contains(a.Prompt, "/snap") {
		t.Errorf("prompt = %q", a.Prompt)
	}
}

func TestProbe(t *testing.T) {
	info, err := New(fakeKiro(t, "")).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "2.34.2" || !slices.Contains(info.Capabilities, "restricted-file-scope") || slices.Contains(info.Capabilities, "budget-limit") {
		t.Fatalf("info = %+v", info)
	}
	if !slices.ContainsFunc(info.Unenforced, func(s string) bool { return strings.Contains(s, "spend limit") }) {
		t.Error("missing spend limit must be disclosed")
	}

	old := filepath.Join(t.TempDir(), "kiro-cli")
	os.WriteFile(old, []byte("#!/bin/sh\ncase \"$1\" in --version) echo kiro-cli 1.0.0;; chat) echo --agent;; esac\n"), 0o755)
	if _, err := New(old).Probe(context.Background()); harness.KindOf(err) != harness.ErrUnsupported || !strings.Contains(err.Error(), "--trust-tools") {
		t.Fatalf("old kiro: %v", err)
	}
	loggedOut := fakeKiro(t, "")
	data, _ := os.ReadFile(loggedOut)
	os.WriteFile(loggedOut, []byte(strings.Replace(string(data), `whoami) echo "Logged in"; exit 0;;`, `whoami) echo "Not logged in"; exit 1;;`, 1)), 0o755)
	if _, err := New(loggedOut).Probe(context.Background()); harness.KindOf(err) != harness.ErrUnsupported || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("logged out: %v", err)
	}
	if _, err := New(filepath.Join(t.TempDir(), "absent")).Probe(context.Background()); harness.KindOf(err) != harness.ErrUnsupported {
		t.Fatalf("missing binary: %v", err)
	}
}

func TestRunIsolatesAndParses(t *testing.T) {
	rec := record(t)
	req := request(t)
	t.Setenv("KIRO_HOME", "/users/real/.kiro")
	res, err := New(fakeKiro(t, "echo '[denied] tool permission approval is not supported' >&2\n"+finish("Here you go:\n```json\n{\"n\": 7}\n```"))).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Output) != `{"n": 7}` || res.Meta.SessionID != "s-1" || res.Meta.NumTurns != 1 || res.Meta.PermissionDenials != 1 || res.Meta.Model != "claude-haiku-5.5" {
		t.Fatalf("result = %s %+v", res.Output, res.Meta)
	}

	snapshot, _ := filepath.EvalSymlinks(req.WorkDir)
	cwd, home := read(t, filepath.Join(rec, "cwd")), read(t, filepath.Join(rec, "home"))
	if cwd == snapshot || strings.HasPrefix(cwd, snapshot) {
		t.Errorf("kiro ran inside the snapshot (%s)", cwd)
	}
	if c := read(t, filepath.Join(rec, "cwd-contents")); c != "" {
		t.Errorf("workspace not empty: %q", c)
	}
	if home == "/users/real/.kiro" || home == "" {
		t.Errorf("KIRO_HOME not private: %q", home)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("private home %s not removed", home)
	}
	var agent AgentConfig
	if err := json.Unmarshal([]byte(read(t, filepath.Join(rec, "agent.json"))), &agent); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(agent.Name, "sift-") || len(agent.Name) < 16 || !strings.Contains(read(t, filepath.Join(rec, "args")), "--agent "+agent.Name) {
		t.Errorf("agent name %q not random or not used", agent.Name)
	}
	if !slices.Contains(agent.ToolsSettings["read"]["allowedPaths"].([]any), any(snapshot)) {
		t.Errorf("read not scoped to the snapshot: %v", agent.ToolsSettings)
	}
	if !strings.Contains(read(t, filepath.Join(rec, "settings.json")), `"chat.disableInheritingDefaultResources": true`) {
		t.Error("default resources not disabled")
	}
	stdin := read(t, filepath.Join(rec, "stdin"))
	if !strings.HasPrefix(stdin, "validate") || !strings.Contains(stdin, schemaDoc) {
		t.Errorf("stdin = %q", stdin)
	}
	for _, f := range []string{"harness-agent.json", "harness-stdout.jsonl", "harness-stderr.log"} {
		if _, err := os.Stat(filepath.Join(req.LogDir, f)); err != nil {
			t.Errorf("log %s: %v", f, err)
		}
	}
}

func TestRunFailures(t *testing.T) {
	cases := map[string]struct {
		chat string
		kind harness.ErrorKind
		want string
	}{
		"prose":            {finish("I could not finish."), harness.ErrInvalidOutput, "no JSON object"},
		"schema mismatch":  {finish(`{"n":"seven"}`), harness.ErrInvalidOutput, "want integer"},
		"extra field":      {finish(`{"n":1,"x":2}`), harness.ErrInvalidOutput, "unexpected field"},
		"malformed":        {finish(`{"n":1`), harness.ErrInvalidOutput, "no JSON object"},
		"init error":       {`echo '{"type":"runError","data":{"stage":"init","message":"Mode not found"}}'; exit 4`, harness.ErrUnsupported, "Mode not found"},
		"run error":        {`echo '{"type":"runError","data":{"stage":"turn","message":"stream reset"}}'; exit 4`, harness.ErrProcess, "stream reset"},
		"wrong shape":      {finish(`Example: {"x":1}. Answer: {"n":"7"}`), harness.ErrInvalidOutput, "want integer"},
		"no result":        {`echo "boom" >&2; exit 2`, harness.ErrProcess, "exited with code 2: boom"},
		"failed status":    {`echo '{"type":"runFinished","data":{"status":"error","stopReason":"refusal"}}'`, harness.ErrProcess, "error (refusal)"},
		"truncated output": {`echo '{"type":"runFinished","data":{"status":"success","finalText":"{","finalTextTruncated":true}}'`, harness.ErrInvalidOutput, "truncated"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(fakeKiro(t, tc.chat)).Run(context.Background(), request(t))
			if harness.KindOf(err) != tc.kind || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v (kind %s), want %s containing %q", err, harness.KindOf(err), tc.kind, tc.want)
			}
		})
	}
}

func TestExtractJSON(t *testing.T) {
	for text, want := range map[string]string{
		`{"n":1}`:                         `{"n":1}`,
		"Done.\n```json\n{\"n\": 2}\n```": `{"n": 2}`,
		"I checked `func main() {` in x.go. {\"n\":3}": `{"n":3}`,
		`Example: {"x":1}. Final: {"n":4} Thanks {`:    `{"n":4}`,
		`{"n":5} then a stray example {"y":2}`:         `{"n":5}`,
	} {
		got, err := extractJSON(text, []byte(schemaDoc))
		if err != nil || string(got) != want {
			t.Errorf("%q: got %s, %v; want %s", text, got, err, want)
		}
	}
}

func TestProbeRejectsOtherBinary(t *testing.T) {
	claude := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(claude, []byte("#!/bin/sh\necho '2.1.311 (Claude Code)'\n"), 0o755)
	if _, err := New(claude).Probe(context.Background()); harness.KindOf(err) != harness.ErrUnsupported || !strings.Contains(err.Error(), "is not kiro-cli") {
		t.Fatalf("got %v", err)
	}
}

func TestKiroAncestor(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	deep := filepath.Join(root, "a", "b")
	os.MkdirAll(deep, 0o755)
	if got := kiroAncestor(deep); got != "" {
		t.Fatalf("unexpected ancestor %q", got)
	}
	os.MkdirAll(filepath.Join(root, "a", ".kiro"), 0o755)
	if got := kiroAncestor(deep); got != filepath.Join(root, "a") {
		t.Fatalf("got %q", got)
	}
}

func TestRunTimeoutAndCancel(t *testing.T) {
	slow := fakeKiro(t, "sleep 30")
	req := request(t)
	req.Timeout = 300 * time.Millisecond
	start := time.Now()
	if _, err := New(slow).Run(context.Background(), req); harness.KindOf(err) != harness.ErrTimeout {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout took %s", time.Since(start))
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	if _, err := New(slow).Run(ctx, request(t)); harness.KindOf(err) != harness.ErrCanceled {
		t.Fatalf("cancel: %v", err)
	}
}

func TestRelativeBinaryResolved(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.MkdirAll("bin", 0o755)
	data, _ := os.ReadFile(fakeKiro(t, finish(`{"n":1}`)))
	os.WriteFile(filepath.Join("bin", "kiro-cli"), data, 0o755)
	h := New("./bin/kiro-cli")
	if !filepath.IsAbs(h.Binary) {
		t.Fatalf("binary %q not resolved", h.Binary)
	}
	if _, err := h.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if _, err := h.Run(context.Background(), request(t)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if New("kiro-cli").Binary != "kiro-cli" {
		t.Error("bare names must stay PATH lookups")
	}
}

func TestCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(fakeKiro(t, "sleep 30")).Run(ctx, request(t)); harness.KindOf(err) != harness.ErrCanceled {
		t.Fatalf("got %v (kind %s), want canceled", err, harness.KindOf(err))
	}
}

func TestRunRejectsIncompleteRequest(t *testing.T) {
	if _, err := New("kiro-cli").Run(context.Background(), harness.Request{}); harness.KindOf(err) != harness.ErrUnsupported {
		t.Fatalf("got %v", err)
	}
}
