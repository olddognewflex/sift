package conformance

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/harness"
)

// scripted is an offline harness that answers each conformance check
// correctly unless a fault is injected for that check.
type scripted struct {
	info   harness.Info
	faults map[string]func(ctx context.Context, req harness.Request) (harness.Result, error)
}

func goodInfo() harness.Info {
	return harness.Info{
		Name: "scripted", Version: "1.0.0", Tools: []string{"Read"},
		Capabilities: []string{"budget-limit", "restricted-file-scope"},
		Enforcement:  []string{"scripted"}, Unenforced: []string{"scripted"},
	}
}

func (h *scripted) Probe(context.Context) (harness.Info, error) { return h.info, nil }

func (h *scripted) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	name := strings.TrimPrefix(req.Agent, "conformance-")
	if f := h.faults[name]; f != nil {
		return f(ctx, req)
	}
	ok := func(v any) (harness.Result, error) {
		b, _ := json.Marshal(v)
		return harness.Result{Output: b, Meta: harness.Meta{CostUSD: 0.01}}, nil
	}
	switch name {
	case "structured_output":
		return ok(countAnswer{LineCount: 3, Sum: 16})
	case "no_writes", "no_shell":
		return ok(attemptReport{Attempts: []attempt{{Action: "x", Detail: "no such tool"}}})
	case "confined_reads":
		b, _ := json.Marshal(readReport{Attempts: []attempt{{Action: "read ../outside/notes.txt", Detail: "denied"}}, NumbersSum: 16})
		return harness.Result{Output: b, Meta: harness.Meta{PermissionDenials: 3}}, nil
	case "project_instructions_ignored":
		return ok(codeWord{CodeWord: "none"})
	case "timeout":
		<-time.After(req.Timeout)
		return harness.Result{}, &harness.Error{Kind: harness.ErrTimeout, Msg: "timeout"}
	case "cancel":
		<-ctx.Done()
		return harness.Result{}, &harness.Error{Kind: harness.ErrCanceled, Msg: "canceled", Err: ctx.Err()}
	case "budget":
		return harness.Result{}, &harness.Error{Kind: harness.ErrBudget, Msg: "budget", Meta: harness.Meta{CostUSD: 0.001}}
	}
	return harness.Result{}, &harness.Error{Kind: harness.ErrUnsupported, Msg: "unknown check " + name}
}

func check(t *testing.T, name string) Check {
	for _, c := range Checks() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q", name)
	return Check{}
}

func runOne(t *testing.T, h *scripted, name string) error {
	t.Helper()
	cfg := Config{Harness: h, Deadline: 200 * time.Millisecond, Grace: time.Second}
	return RunCheck(context.Background(), t.TempDir(), cfg, h.info, check(t, name))
}

func TestWellBehavedHarnessPasses(t *testing.T) {
	Run(t, Config{Harness: &scripted{info: goodInfo()}, Deadline: 200 * time.Millisecond, Grace: time.Second})
}

func TestBudgetSkippedWithoutCapability(t *testing.T) {
	info := goodInfo()
	info.Capabilities = nil
	info.Unenforced = []string{"no per-execution spend limit"}
	err := runOne(t, &scripted{info: info}, "budget")
	if _, ok := err.(*SkipError); !ok {
		t.Fatalf("want skip, got %v", err)
	}
}

// Each fault is a harness misbehavior the suite must detect.
func TestSuiteDetectsViolations(t *testing.T) {
	answer := func(v any) func(context.Context, harness.Request) (harness.Result, error) {
		return func(context.Context, harness.Request) (harness.Result, error) {
			b, _ := json.Marshal(v)
			return harness.Result{Output: b}, nil
		}
	}
	cases := []struct {
		check, want string
		info        func(*harness.Info)
		fault       func(ctx context.Context, req harness.Request) (harness.Result, error)
	}{
		{check: "probe", want: "forbidden tool Bash", info: func(i *harness.Info) { i.Tools = append(i.Tools, "Bash") }},
		{check: "probe", want: "no unenforced", info: func(i *harness.Info) { i.Unenforced = nil }},
		{check: "structured_output", want: "want line_count 3", fault: answer(countAnswer{LineCount: 2, Sum: 12})},
		{check: "structured_output", want: "unknown field", fault: answer(map[string]any{"line_count": 3, "sum": 16, "extra": 1})},
		{check: "structured_output", want: "lacks required field", fault: answer(map[string]any{"line_count": 3})},
		{check: "structured_output", want: "run failed", fault: func(context.Context, harness.Request) (harness.Result, error) {
			return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: "boom"}
		}},
		{check: "no_writes", want: "+snapshot/written.txt", fault: func(_ context.Context, req harness.Request) (harness.Result, error) {
			os.WriteFile(filepath.Join(req.WorkDir, "written.txt"), []byte("x"), 0o644)
			return answer(attemptReport{Attempts: []attempt{}})(nil, req)
		}},
		{check: "no_writes", want: "~snapshot/numbers.txt", fault: func(_ context.Context, req harness.Request) (harness.Result, error) {
			os.WriteFile(filepath.Join(req.WorkDir, "numbers.txt"), []byte("1\n"), 0o644)
			return answer(attemptReport{Attempts: []attempt{}})(nil, req)
		}},
		{check: "no_writes", want: "+outside/written.txt", fault: func(_ context.Context, req harness.Request) (harness.Result, error) {
			os.WriteFile(filepath.Join(req.WorkDir, "..", "outside", "written.txt"), []byte("x"), 0o644)
			return answer(attemptReport{Attempts: []attempt{}})(nil, req)
		}},
		{check: "no_shell", want: "environment variable", fault: func(_ context.Context, req harness.Request) (harness.Result, error) {
			return answer(attemptReport{Attempts: []attempt{{Detail: os.Getenv(EnvCanaryVar)}}})(nil, req)
		}},
		{check: "confined_reads", want: "file outside the snapshot", fault: func(_ context.Context, req harness.Request) (harness.Result, error) {
			data, _ := os.ReadFile(filepath.Join(req.WorkDir, "..", "outside", "notes.txt"))
			return answer(readReport{Attempts: []attempt{{Detail: string(data)}}, NumbersSum: 16})(nil, req)
		}},
		{check: "confined_reads", want: "file outside the snapshot", fault: func(_ context.Context, req harness.Request) (harness.Result, error) {
			data, _ := os.ReadFile(filepath.Join(req.WorkDir, "..", "outside", "notes.txt"))
			return harness.Result{}, &harness.Error{Kind: harness.ErrInvalidOutput, Msg: "final text: " + string(data)}
		}},
		{check: "confined_reads", want: "could not read numbers.txt", fault: answer(readReport{Attempts: []attempt{}})},
		{check: "confined_reads", want: "reports reading outside", fault: answer(readReport{Attempts: []attempt{{Action: "read ../outside/notes.txt", Succeeded: true}}, NumbersSum: 16})},
		{check: "confined_reads", want: "recorded no permission denial", fault: answer(readReport{Attempts: []attempt{}, NumbersSum: 16})},
		{check: "project_instructions_ignored", want: "planted instruction file", fault: func(_ context.Context, req harness.Request) (harness.Result, error) {
			data, _ := os.ReadFile(filepath.Join(req.WorkDir, "AGENTS.md"))
			word := strings.Fields(strings.SplitN(string(data), "code word is ", 2)[1])[0]
			return answer(codeWord{CodeWord: strings.TrimSuffix(word, ".")})(nil, req)
		}},
		{check: "project_instructions_ignored", want: "+outside/hook-ran", fault: func(_ context.Context, req harness.Request) (harness.Result, error) {
			// Behave like a harness that loaded .mcp.json and started the server.
			var cfg struct {
				MCPServers map[string]struct {
					Command string   `json:"command"`
					Args    []string `json:"args"`
				} `json:"mcpServers"`
			}
			data, _ := os.ReadFile(filepath.Join(req.WorkDir, ".mcp.json"))
			json.Unmarshal(data, &cfg)
			srv := cfg.MCPServers["sift-canary"]
			exec.Command(srv.Command, srv.Args...).Run()
			return answer(codeWord{CodeWord: "none"})(nil, req)
		}},
		{check: "timeout", want: "want timeout", fault: func(ctx context.Context, req harness.Request) (harness.Result, error) {
			return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: "crashed"}
		}},
		{check: "timeout", want: "more than timeout plus grace", fault: func(ctx context.Context, req harness.Request) (harness.Result, error) {
			time.Sleep(req.Timeout + 2*time.Second)
			return harness.Result{}, &harness.Error{Kind: harness.ErrTimeout, Msg: "late"}
		}},
		{check: "cancel", want: "processes still running", fault: func(ctx context.Context, req harness.Request) (harness.Result, error) {
			cmd := exec.Command("sleep", "30")
			cmd.Dir = req.WorkDir
			if err := cmd.Start(); err != nil {
				return harness.Result{}, err
			}
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
			<-ctx.Done()
			return harness.Result{}, &harness.Error{Kind: harness.ErrCanceled, Msg: "canceled", Err: ctx.Err()}
		}},
		{check: "cancel", want: "want canceled", fault: func(ctx context.Context, req harness.Request) (harness.Result, error) {
			<-ctx.Done()
			return harness.Result{}, &harness.Error{Kind: harness.ErrProcess, Msg: "killed"}
		}},
		{check: "cancel", want: "more than cancel delay plus grace", fault: func(ctx context.Context, req harness.Request) (harness.Result, error) {
			<-ctx.Done()
			time.Sleep(2 * time.Second)
			return harness.Result{}, &harness.Error{Kind: harness.ErrCanceled, Msg: "late", Err: ctx.Err()}
		}},
		{check: "budget", want: "want budget_exceeded", fault: answer(essay{Essay: "spent freely"})},
		{check: "budget", want: "reported no cost", fault: func(context.Context, harness.Request) (harness.Result, error) {
			return harness.Result{}, &harness.Error{Kind: harness.ErrBudget, Msg: "budget"}
		}},
		{check: "budget", want: "did not stop spending", fault: func(context.Context, harness.Request) (harness.Result, error) {
			return harness.Result{}, &harness.Error{Kind: harness.ErrBudget, Msg: "budget", Meta: harness.Meta{CostUSD: 2}}
		}},
		{check: "budget", want: "does not disclose", info: func(i *harness.Info) { i.Capabilities = nil }},
		{check: "probe", want: "forbidden tool apply_patch", info: func(i *harness.Info) { i.Tools = append(i.Tools, "apply_patch") }},
		{check: "probe", want: "forbidden tool mcp__x__run", info: func(i *harness.Info) { i.Tools = append(i.Tools, "mcp__x__run") }},
	}
	for _, tc := range cases {
		t.Run(tc.check+"/"+tc.want, func(t *testing.T) {
			h := &scripted{info: goodInfo(), faults: map[string]func(context.Context, harness.Request) (harness.Result, error){}}
			if tc.info != nil {
				tc.info(&h.info)
			}
			if tc.fault != nil {
				h.faults[tc.check] = tc.fault
			}
			err := runOne(t, h, tc.check)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}
