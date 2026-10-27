package cli

import (
	"testing"

	"github.com/olddognewflex/sift/internal/config"
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
