package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/harness"
	"github.com/olddognewflex/sift/internal/harness/claudecode"
	"github.com/olddognewflex/sift/internal/harness/kiro"
)

func TestDefaultHarness(t *testing.T) {
	for _, tc := range []struct{ name, binary, want string }{
		{"claude-code", "", "claude"},
		{"claude-code", "/opt/claude", "/opt/claude"},
		{"kiro", "", "kiro-cli"},
		{"kiro", "/opt/kiro-cli", "/opt/kiro-cli"},
	} {
		cfg := config.Default()
		cfg.Harness.Name, cfg.Harness.Binary = tc.name, tc.binary
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		h, err := DefaultHarness(cfg)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		switch h := h.(type) {
		case *claudecode.Harness:
			got = h.Binary
		case *kiro.Harness:
			got = h.Binary
		}
		if got != tc.want {
			t.Errorf("%s with binary %q: got %q, want %q", tc.name, tc.binary, got, tc.want)
		}
	}
	cfg := config.Default()
	cfg.Harness.Name = "codex"
	if cfg.Validate() == nil {
		t.Error("unknown harness accepted")
	}
}

func TestAuditHarnessFlag(t *testing.T) {
	e := newCLI(t)
	cfg := "version: 1\nstorage: {runs_dir: " + e.runs + "}\nharness: {name: claude-code, binary: /opt/claude, model: opus}\n"
	if err := os.WriteFile(e.cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want config.HarnessConfig
	}{
		{nil, config.HarnessConfig{Name: "claude-code", Binary: "/opt/claude", Model: "opus"}},
		{[]string{"--harness", "claude-code"}, config.HarnessConfig{Name: "claude-code", Binary: "/opt/claude", Model: "opus"}},
		{[]string{"--harness", "kiro"}, config.HarnessConfig{Name: "kiro"}},
		{[]string{"--harness", "kiro", "--model", "claude-sonnet-5.5"}, config.HarnessConfig{Name: "kiro", Model: "claude-sonnet-5.5"}},
	} {
		var got config.HarnessConfig
		d := Deps{Stdout: io.Discard, Stderr: io.Discard, Version: "test",
			NewHarness: func(c config.Config) (harness.Harness, error) {
				got = c.Harness
				return nil, errors.New("stop")
			}}
		Execute(context.Background(), d, append([]string{"--config", e.cfgPath, "audit", e.repo}, tc.args...))
		if got != tc.want {
			t.Errorf("%v: harness config %+v, want %+v", tc.args, got, tc.want)
		}
	}
	var stderr bytes.Buffer
	code := Execute(context.Background(), Deps{Stdout: io.Discard, Stderr: &stderr, Version: "test"}, []string{"--config", e.cfgPath, "audit", e.repo, "--harness", "codex"})
	if code == ExitOK || !strings.Contains(stderr.String(), "codex") {
		t.Errorf("unknown harness: code %d, stderr %q", code, stderr.String())
	}
}
