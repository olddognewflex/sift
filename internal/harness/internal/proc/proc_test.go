package proc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/harness"
)

func TestResolveBinary(t *testing.T) {
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(wd, "bin", "x")
	for _, tc := range []struct{ in, want string }{
		{"", "claude"},
		{"kiro-cli", "kiro-cli"},
		{filepath.Join(".", "bin", "x"), abs},
		{filepath.Join("bin", "x"), abs},
		{"/usr/local/bin/claude", "/usr/local/bin/claude"},
	} {
		if got := ResolveBinary(tc.in, "claude"); got != tc.want {
			t.Errorf("ResolveBinary(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTail(t *testing.T) {
	long := strings.Repeat("a", 600) + "END"
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"  \n\t", ""},
		{" oops \n", ": oops"},
		{long, ": …" + long[len(long)-500:]},
	} {
		if got := Tail([]byte(tc.in)); got != tc.want {
			t.Errorf("Tail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"", 3, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc…"},
	} {
		if got := Truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestMissingBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "no-such-cli")
	o := Run(context.Background(), 5*time.Second, Spec{Binary: bin})
	if o.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1", o.ExitCode)
	}
	err := o.Failure(harness.Meta{})
	if harness.KindOf(err) != harness.ErrUnsupported || !strings.Contains(err.Error(), "start no-such-cli") {
		t.Fatalf("got %v (kind %s)", err, harness.KindOf(err))
	}
}
