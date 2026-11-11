//go:build unix

package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/harness"
)

// escapeEnv makes the test binary act as a helper that leaves its process
// group and then execs its arguments, giving tests a leftover child that the
// group kill cannot reach. sh's set -m only does this when it has a tty.
const escapeEnv = "SIFT_PROC_TEST_ESCAPE"

func TestMain(m *testing.M) {
	if os.Getenv(escapeEnv) == "1" {
		if err := escapeAndExec(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "escape helper:", err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

func escapeAndExec(args []string) error {
	if len(args) == 0 {
		return errors.New("no command")
	}
	if err := syscall.Setpgid(0, 0); err != nil {
		return err
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, args, os.Environ())
}

// escaped returns a shell command prefix that runs the rest of the line in
// a new process group.
func escaped(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return escapeEnv + "=1 '" + exe + "' "
}

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
// writing to stdout when the harness exits), Time (bounded by DrainGrace).
//
// A grandchild that inherits the output pipes keeps them open after the
// script exits. Run keeps reading for up to DrainGrace, so a short-lived
// grandchild's output is captured in full and nothing is reported.
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
	if o.ExitCode != 0 || o.RunErr != nil || o.Lingered {
		t.Fatalf("exit = %d, err = %v, lingered = %t", o.ExitCode, o.RunErr, o.Lingered)
	}
	if got := string(o.Stdout); got != "early\nlate\n" {
		t.Errorf("stdout = %q, want the grandchild's late output too", got)
	}
	if elapsed < time.Second || elapsed >= DrainGrace {
		t.Errorf("Run took %s, want about the grandchild's 1s lifetime", elapsed)
	}
	if w := o.Warnings(); len(w) != 0 {
		t.Errorf("warnings = %q", w)
	}
}

// QA Author — adversarial tests
// Break-It dimensions covered: Partial Failure (harness exits 0 but a
// background child keeps its output pipes open), Time (Run must not stall
// for the full KillGrace on every such execution).
func TestRunKillsGrandchildHoldingOutputPipesAfterDrainGrace(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "grandchild-survived")
	bin := script(t, `(echo mid >&2; sleep 4; touch `+marker+`; echo late) &
echo early
exit 0`)
	start := time.Now()
	o := Run(context.Background(), 30*time.Second, Spec{Binary: bin})
	elapsed := time.Since(start)
	if err := o.Failure(harness.Meta{}); err != nil {
		t.Fatalf("failure: %v", err)
	}
	if o.ExitCode != 0 || !o.Lingered || !errors.Is(o.RunErr, exec.ErrWaitDelay) {
		t.Fatalf("exit = %d, lingered = %t, err = %v", o.ExitCode, o.Lingered, o.RunErr)
	}
	if string(o.Stdout) != "early\n" || string(o.Stderr) != "mid\n" {
		t.Errorf("stdout = %q, stderr = %q", o.Stdout, o.Stderr)
	}
	if elapsed < DrainGrace || elapsed >= DrainGrace+2*time.Second {
		t.Errorf("Run took %s, want just over DrainGrace (%s)", elapsed, DrainGrace)
	}
	if w := o.Warnings(); len(w) != 1 {
		t.Errorf("warnings = %q, want one", w)
	}
	// The grandchild would touch the marker 4s in; check 1.5s after that.
	time.Sleep(time.Until(start.Add(5500 * time.Millisecond)))
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("grandchild survived the post-exit drain")
	}
}

// QA Author — adversarial tests
// Break-It dimensions covered: Time (cancellation during the post-exit drain
// must act at once, not wait out DrainGrace).
func TestRunCancelDuringPostExitDrain(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan time.Time, 1)
	time.AfterFunc(300*time.Millisecond, func() { canceled <- time.Now(); cancel() })
	o := Run(ctx, 30*time.Second, Spec{Binary: script(t, "sleep 15 &\nexit 0")})
	returned := time.Now()
	err := o.Failure(harness.Meta{})
	if harness.KindOf(err) != harness.ErrCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("kind = %s (%v)", harness.KindOf(err), err)
	}
	if lag := returned.Sub(<-canceled); lag >= 1500*time.Millisecond {
		t.Fatalf("Run returned %s after cancel", lag)
	}
}

// QA Author — adversarial tests
// Break-It dimensions covered: Time (a grandchild that ignores SIGTERM keeps
// the output open after the harness dies; the timeout must still end Run
// promptly instead of waiting for exec's SIGKILL after KillGrace).
func TestRunTimeoutWithTermIgnoringGrandchild(t *testing.T) {
	t.Parallel()
	bin := script(t, `(trap '' TERM; sleep 30) &
sleep 30`)
	start := time.Now()
	o := Run(context.Background(), 300*time.Millisecond, Spec{Binary: bin})
	elapsed := time.Since(start)
	err := o.Failure(harness.Meta{})
	if harness.KindOf(err) != harness.ErrTimeout {
		t.Fatalf("kind = %s (%v)", harness.KindOf(err), err)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

// QA Author — adversarial tests
// Break-It dimensions covered: Resource Leaks (every pipe Run opens is
// closed, whether the harness succeeds, fails, or never starts).
//
// Not parallel: it counts the process's open descriptors.
func TestRunClosesPipeDescriptors(t *testing.T) {
	ok := script(t, `cat >/dev/null; echo out; echo err >&2`)
	fail := script(t, `echo out; exit 3`)
	missing := filepath.Join(t.TempDir(), "no-such-cli")
	run := func(bin string) {
		Run(context.Background(), 5*time.Second, Spec{Binary: bin, Stdin: strings.NewReader("in")})
	}
	countFDs := func() int {
		entries, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	run(ok) // warm up anything the runtime opens once
	before := countFDs()
	for i := range 10 {
		run([]string{ok, fail, missing}[i%3])
	}
	if after := countFDs(); after != before {
		t.Fatalf("open descriptors: %d before, %d after 10 runs", before, after)
	}
}

// holderPIDs reads the "harness-pid holder-pid" line a script wrote to
// pidFile and registers a cleanup that kills the holder, so no stray
// process outlives the test whatever it asserts.
func holderPIDs(t *testing.T, pidFile string) (harnessPID, holder int) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("script did not record its PIDs: %v", err)
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		t.Fatalf("pid file = %q", b)
	}
	harnessPID, err1 := strconv.Atoi(f[0])
	holder, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		t.Fatalf("pid file = %q", b)
	}
	t.Cleanup(func() { _ = syscall.Kill(holder, syscall.SIGKILL) })
	return harnessPID, holder
}

// checkEscapedAndAlive fails unless holder runs outside the harness's
// process group and survived Run. Otherwise the group kill, not Run's
// close fallback, is what unblocked the copies, and the test proves
// nothing about the fallback.
func checkEscapedAndAlive(t *testing.T, harnessPID, holder int) {
	t.Helper()
	pgid, err := syscall.Getpgid(holder)
	if err != nil {
		t.Fatalf("holder %d did not survive Run: %v", holder, err)
	}
	// Setpgid makes the harness its own group leader, so its PID is the
	// group the kill targets.
	if pgid == harnessPID {
		t.Fatalf("holder %d is in the harness's process group %d; the escape helper did not detach it", holder, pgid)
	}
}

// QA Author — adversarial tests
// Break-It dimensions covered: Partial Failure (a leftover child that left
// the harness's process group survives the group kill and keeps stdout
// open), Time (Run must still return, bounded by DrainGrace + killDrain).
//
// The escape helper puts the holder in its own process group, so killGroup
// cannot reach it. Only closing Run's own read ends unblocks the copy.
func TestRunReturnsWhenOutputHolderEscapesProcessGroup(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "pids")
	bin := script(t, escaped(t)+`sleep 6 &
echo "$$ $!" > `+pidFile+`
echo early
exit 0`)
	start := time.Now()
	o := Run(context.Background(), 30*time.Second, Spec{Binary: bin})
	elapsed := time.Since(start)
	harnessPID, holder := holderPIDs(t, pidFile)
	// The close fallback fires only after DrainGrace and then killDrain.
	// Without it Run waits for the holder to exit by itself.
	if elapsed < DrainGrace+killDrain || elapsed >= DrainGrace+killDrain+1500*time.Millisecond {
		t.Fatalf("Run took %s, want just over DrainGrace+killDrain (%s)", elapsed, DrainGrace+killDrain)
	}
	checkEscapedAndAlive(t, harnessPID, holder)
	if err := o.Failure(harness.Meta{}); err != nil {
		t.Fatalf("failure: %v", err)
	}
	if o.ExitCode != 0 || !o.Lingered || !errors.Is(o.RunErr, exec.ErrWaitDelay) {
		t.Fatalf("exit = %d, lingered = %t, err = %v", o.ExitCode, o.Lingered, o.RunErr)
	}
	if string(o.Stdout) != "early\n" || len(o.Stderr) != 0 {
		t.Errorf("stdout = %q, stderr = %q", o.Stdout, o.Stderr)
	}
	if w := o.Warnings(); len(w) != 1 {
		t.Errorf("warnings = %q, want one", w)
	}
}

// QA Author — adversarial tests
// Break-It dimensions covered: Boundary (stdin larger than the pipe buffer),
// Partial Failure (the harness exits without reading stdin while a child
// outside its process group keeps the read end open), Time (the blocked
// stdin copy must not hang Run).
//
// stdout and stderr close when the harness exits; only the stdin writer is
// stuck, on a pipe whose reader never reads and never dies to the group
// kill. Lingered is expected: the stdin copy had not finished.
//
// Without job control, POSIX gives a background command /dev/null as stdin
// before its own redirections apply, so dash turns "<&0 &" into /dev/null.
// Saving stdin on fd 3 first keeps the pipe.
func TestRunReturnsWhenEscapedChildHoldsUnreadStdin(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "pids")
	bin := script(t, `exec 3<&0
`+escaped(t)+`sleep 6 <&3 3<&- >/dev/null 2>&1 &
echo "$$ $!" > `+pidFile+`
exit 0`)
	start := time.Now()
	o := Run(context.Background(), 30*time.Second, Spec{
		Binary: bin,
		Stdin:  strings.NewReader(strings.Repeat("x", 1<<20)),
	})
	elapsed := time.Since(start)
	harnessPID, holder := holderPIDs(t, pidFile)
	if elapsed < DrainGrace+killDrain || elapsed >= DrainGrace+killDrain+1500*time.Millisecond {
		t.Fatalf("Run took %s, want just over DrainGrace+killDrain (%s)", elapsed, DrainGrace+killDrain)
	}
	checkEscapedAndAlive(t, harnessPID, holder)
	if err := o.Failure(harness.Meta{}); err != nil {
		t.Fatalf("failure: %v", err)
	}
	if o.ExitCode != 0 || !o.Lingered || !errors.Is(o.RunErr, exec.ErrWaitDelay) {
		t.Fatalf("exit = %d, lingered = %t, err = %v", o.ExitCode, o.Lingered, o.RunErr)
	}
}
