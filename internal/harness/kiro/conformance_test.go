package kiro

import (
	"os"
	"os/exec"
	"testing"

	"github.com/olddognewflex/sift/internal/harness/conformance"
)

// TestConformance runs the live harness conformance suite against the
// installed kiro-cli. It makes model calls and uses Kiro credits, so it only
// runs when SIFT_CONFORMANCE=1:
//
//	make conformance
//
// SIFT_CONFORMANCE_KIRO_MODEL selects the model (default: claude-haiku-5.5).
// SIFT_CONFORMANCE_KIRO selects the binary (default: kiro-cli).
func TestConformance(t *testing.T) {
	if os.Getenv("SIFT_CONFORMANCE") != "1" {
		t.Skip("set SIFT_CONFORMANCE=1 to run the live harness conformance suite")
	}
	bin := envOr("SIFT_CONFORMANCE_KIRO", DefaultBinary)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("%s not installed", bin)
	}
	conformance.Run(t, conformance.Config{
		Harness: New(bin),
		Model:   envOr("SIFT_CONFORMANCE_KIRO_MODEL", "claude-haiku-5.5"),
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
