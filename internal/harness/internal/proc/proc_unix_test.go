//go:build unix

package proc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/harness"
)

// script writes an executable shell script and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cli")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunCapturesOutput(t *testing.T) {
	dir := t.TempDir()
	bin := script(t, `read line; echo "out:$line:$FOO:$(pwd)"; echo err >&2`)
	o := Run(context.Background(), 5*time.Second, Spec{
		Binary: bin,
		Dir:    dir,
		Env:    []string{"FOO=bar"},
		Stdin:  strings.NewReader("hello\n"),
	})
	if err := o.Failure(harness.Meta{}); err != nil {
		t.Fatalf("failure: %v", err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if got, want := strings.TrimSpace(string(o.Stdout)), "out:hello:bar:"+real; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got := strings.TrimSpace(string(o.Stderr)); got != "err" {
		t.Errorf("stderr = %q", got)
	}
	if o.ExitCode != 0 || o.RunErr != nil {
		t.Errorf("exit = %d, err = %v", o.ExitCode, o.RunErr)
	}
}

func TestRunNonZeroExitIsNotAFailure(t *testing.T) {
	o := Run(context.Background(), 5*time.Second, Spec{Binary: script(t, "echo out; echo err >&2; exit 3")})
	if err := o.Failure(harness.Meta{}); err != nil {
		t.Fatalf("failure: %v", err)
	}
	if o.ExitCode != 3 || o.RunErr == nil {
		t.Errorf("exit = %d, err = %v", o.ExitCode, o.RunErr)
	}
	if string(o.Stdout) != "out\n" || string(o.Stderr) != "err\n" {
		t.Errorf("stdout = %q, stderr = %q", o.Stdout, o.Stderr)
	}
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "child-survived")
	bin := script(t, `(sleep 3; touch `+marker+`) &
sleep 30`)
	start := time.Now()
	o := Run(context.Background(), 300*time.Millisecond, Spec{Binary: bin})
	err := o.Failure(harness.Meta{})
	if harness.KindOf(err) != harness.ErrTimeout {
		t.Fatalf("kind = %s (%v)", harness.KindOf(err), err)
	}
	// SIGTERM at the 300ms timeout ends the group at once. A broken terminate
	// would only return when exec gives up after KillGrace.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %s; terminate did not stop the process group", elapsed)
	}
	time.Sleep(4 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child process outlived the timeout")
	}
}

// QA Author — adversarial tests
// Break-It dimensions covered: Time (cancellation must act promptly, not via
// exec's fallback kill after WaitDelay).
func TestRunCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan time.Time, 1)
	time.AfterFunc(200*time.Millisecond, func() { canceled <- time.Now(); cancel() })
	o := Run(ctx, 30*time.Second, Spec{Binary: script(t, "sleep 30")})
	returned := time.Now()
	err := o.Failure(harness.Meta{})
	if harness.KindOf(err) != harness.ErrCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("kind = %s (%v)", harness.KindOf(err), err)
	}
	// SIGTERM to the group ends sleep at once. If terminate did nothing, Run
	// would still return, but only when exec gives up after KillGrace.
	if lag := returned.Sub(<-canceled); lag > 2*time.Second {
		t.Fatalf("Run returned %s after cancel; terminate did not stop the process group", lag)
	}
}

// QA Author — adversarial tests
// Break-It dimensions covered: Partial Failure (harness exits 0 but leaves a
// background child behind), Time (Run must not stall on that child).
//
// The grandchild detaches from the output pipes, so Wait returns as soon as
// the script exits and nothing but the post-Run group kill can stop it. A
// marker file shows whether it survived; checking its PID instead would
// depend on the host reaping zombies.
func TestRunReapsDetachedGrandchildOnNormalExit(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "grandchild-survived")
	bin := script(t, `(sleep 1; touch `+marker+`) </dev/null >/dev/null 2>&1 &
exit 0`)
	start := time.Now()
	o := Run(context.Background(), 30*time.Second, Spec{Binary: bin})
	elapsed := time.Since(start)
	if err := o.Failure(harness.Meta{}); err != nil {
		t.Fatalf("failure: %v", err)
	}
	if o.ExitCode != 0 || o.RunErr != nil {
		t.Fatalf("exit = %d, err = %v", o.ExitCode, o.RunErr)
	}
	// Must return before the grandchild's 1s sleep ends, or the marker check
	// below proves nothing.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Run took %s for a script that exits immediately", elapsed)
	}
	// Wait past the grandchild's 1s deadline with a 1s margin.
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("grandchild outlived a normal harness exit")
	}
}

// QA Author — adversarial tests
// Break-It dimensions covered: Partial Failure (background child still
// writing to stdout when the harness exits), Time (bounded by KillGrace).
//
// Documents the real exec behaviour: a grandchild that inherits the output
// pipes keeps Wait blocked until it closes them, so Run waits for it (up to
// KillGrace, after which exec closes the pipes and RunErr is
// exec.ErrWaitDelay) and its output is captured in full.
func TestRunWaitsForGrandchildHoldingOutputPipes(t *testing.T) {
	t.Parallel()
	bin := script(t, `(sleep 1; echo late) &
echo early
exit 0`)
	start := time.Now()
	o := Run(context.Background(), 30*time.Second, Spec{Binary: bin})
	elapsed := time.Since(start)
	if err := o.Failure(harness.Meta{}); err != nil {
		t.Fatalf("failure: %v", err)
	}
	if o.ExitCode != 0 || o.RunErr != nil {
		t.Fatalf("exit = %d, err = %v", o.ExitCode, o.RunErr)
	}
	if got := string(o.Stdout); got != "early\nlate\n" {
		t.Errorf("stdout = %q, want the grandchild's late output too", got)
	}
	if elapsed < time.Second || elapsed > KillGrace/2 {
		t.Errorf("Run took %s, want about the grandchild's 1s lifetime", elapsed)
	}
}
