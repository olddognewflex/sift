package claudecode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/harness"
)

// fakeClaude writes an executable script standing in for the claude CLI.
func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodHelp = `case "$1" in
--version) echo "2.1.311 (Claude Code)"; exit 0;;
--help) echo "--print --output-format --json-schema --tools --restricted --safe-mode --strict-mcp-config --permission-mode --permission-prompts --no-session-persistence --max-budget-usd --system-prompt --model"; exit 0;;
esac`

func request(t *testing.T) harness.Request {
	return harness.Request{
		Agent: "pruv", Instructions: "be careful", Prompt: "validate", WorkDir: t.TempDir(),
		OutputSchema: []byte(`{"type":"object"}`), Timeout: 5 * time.Second, MaxBudgetUSD: 1.5, Model: "sonnet",
		LogDir: filepath.Join(t.TempDir(), "logs"),
	}
}

func TestArgsEnforceReadOnlyPolicy(t *testing.T) {
	args := Args(harness.Request{OutputSchema: []byte("{}"), Instructions: "sys", MaxBudgetUSD: 2, Model: "opus"})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--print", "--restricted", "--safe-mode", "--strict-mcp-config",
		"--permission-mode dontAsk", "--permission-prompts none", "--tools Read,Grep,Glob",
		"--no-session-persistence", "--max-budget-usd 2", "--model opus", "--output-format json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	for _, forbidden := range []string{"--dangerously-skip-permissions", "bypassPermissions", "Bash", "Edit", "Write", "WebFetch"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("args contain %q", forbidden)
		}
	}
	if slices.Contains(Args(harness.Request{}), "--model") {
		t.Error("empty model must use the harness default")
	}
}

func TestProbe(t *testing.T) {
	info, err := New(fakeClaude(t, goodHelp)).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "2.1.311" || len(info.Enforcement) == 0 || len(info.Unenforced) == 0 {
		t.Fatalf("info = %+v", info)
	}

	old := fakeClaude(t, `case "$1" in --version) echo "1.0.0";; --help) echo "--print --output-format";; esac`)
	_, err = New(old).Probe(context.Background())
	if harness.KindOf(err) != harness.ErrUnsupported || !strings.Contains(err.Error(), "--json-schema") {
		t.Fatalf("old harness: %v", err)
	}
	_, err = New(filepath.Join(t.TempDir(), "absent")).Probe(context.Background())
	if harness.KindOf(err) != harness.ErrUnsupported {
		t.Fatalf("missing binary: %v", err)
	}
}

func TestRunSuccess(t *testing.T) {
	bin := fakeClaude(t, `cat > "$PWD/stdin.txt"
echo '{"type":"result","subtype":"success","is_error":false,"result":"{}","structured_output":{"verdict":"reject"},"session_id":"s1","total_cost_usd":0.25,"num_turns":4,"permission_denials":[{"tool_name":"Read"}],"modelUsage":{"claude-x":{}}}'`)
	req := request(t)
	res, err := New(bin).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Output) != `{"verdict":"reject"}` {
		t.Errorf("output = %s", res.Output)
	}
	m := res.Meta
	if m.SessionID != "s1" || m.CostUSD != 0.25 || m.NumTurns != 4 || m.PermissionDenials != 1 || m.Model != "claude-x" {
		t.Errorf("meta = %+v", m)
	}
	if b, _ := os.ReadFile(filepath.Join(req.WorkDir, "stdin.txt")); string(b) != "validate" {
		t.Errorf("prompt not passed on stdin: %q", b)
	}
	if _, err := os.Stat(filepath.Join(req.LogDir, "harness-stdout.json")); err != nil {
		t.Error("raw output not persisted")
	}
}

func TestRunFailures(t *testing.T) {
	cases := []struct {
		name   string
		script string
		kind   harness.ErrorKind
	}{
		{"budget", `echo '{"type":"result","subtype":"error_max_budget_usd","is_error":true,"errors":["Reached maximum budget"]}'; exit 1`, harness.ErrBudget},
		{"harness error", `echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"overloaded"}'; exit 1`, harness.ErrProcess},
		{"no structured output", `echo '{"type":"result","subtype":"success","is_error":false,"result":"I refuse"}'`, harness.ErrInvalidOutput},
		{"null structured output", `echo '{"type":"result","subtype":"success","is_error":false,"structured_output":null}'`, harness.ErrInvalidOutput},
		{"crash", `echo "Error: --json-schema is not a valid JSON Schema" >&2; exit 1`, harness.ErrProcess},
		{"garbage", `echo 'not json'`, harness.ErrProcess},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(fakeClaude(t, c.script)).Run(context.Background(), request(t))
			if harness.KindOf(err) != c.kind {
				t.Fatalf("kind = %s (%v), want %s", harness.KindOf(err), err, c.kind)
			}
		})
	}
	_, err := New(fakeClaude(t, `echo "boom" >&2; exit 3`)).Run(context.Background(), request(t))
	if err == nil || !strings.Contains(err.Error(), "boom") || harness.MetaOf(err).ExitCode != 3 {
		t.Fatalf("stderr and exit code should be reported: %v", err)
	}
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-survived")
	// The child ignores nothing special; it must die with the group.
	bin := fakeClaude(t, `(sleep 3; touch `+marker+`) &
sleep 30`)
	req := request(t)
	req.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := New(bin).Run(context.Background(), req)
	if harness.KindOf(err) != harness.ErrTimeout {
		t.Fatalf("kind = %s (%v)", harness.KindOf(err), err)
	}
	if time.Since(start) > killGrace {
		t.Fatalf("timeout took %s", time.Since(start))
	}
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child process outlived the harness timeout")
	}
}

func TestRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := New(fakeClaude(t, "sleep 30")).Run(ctx, request(t))
	if harness.KindOf(err) != harness.ErrCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("kind = %s (%v)", harness.KindOf(err), err)
	}
}

func TestRunRejectsIncompleteRequest(t *testing.T) {
	_, err := New("claude").Run(context.Background(), harness.Request{})
	if harness.KindOf(err) != harness.ErrUnsupported {
		t.Fatalf("err = %v", err)
	}
}
