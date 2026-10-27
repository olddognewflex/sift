package claudecode

import (
	"os"
	"os/exec"
	"testing"

	"github.com/olddognewflex/sift/internal/harness/conformance"
)

// TestConformance runs the live harness conformance suite against the
// installed claude CLI. It makes model calls and costs money, so it only runs
// when SIFT_CONFORMANCE=1:
//
//	make conformance
//
// SIFT_CONFORMANCE_MODEL selects the model (default: haiku).
// SIFT_CONFORMANCE_CLAUDE selects the binary (default: claude).
func TestConformance(t *testing.T) {
	if os.Getenv("SIFT_CONFORMANCE") != "1" {
		t.Skip("set SIFT_CONFORMANCE=1 to run the live harness conformance suite")
	}
	bin := envOr("SIFT_CONFORMANCE_CLAUDE", "claude")
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("%s not installed", bin)
	}
	conformance.Run(t, conformance.Config{
		Harness: New(bin),
		Model:   envOr("SIFT_CONFORMANCE_MODEL", "haiku"),
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
