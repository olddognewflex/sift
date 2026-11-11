// Package proc owns the harness subprocess lifecycle: timeout, cancellation,
// and the process-group kill. Every adapter runs its harness executions
// through Run so that the guarantee of leaving no child processes behind
// exists in one place.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/olddognewflex/sift/internal/harness"
)

// KillGrace is how long the harness gets to exit after SIGTERM.
const KillGrace = 10 * time.Second

// Spec describes one harness process.
type Spec struct {
	Binary string
	Args   []string
	Dir    string
	Env    []string // nil inherits the current environment
	Stdin  io.Reader
}

// Outcome is what Run observed.
type Outcome struct {
	Stdout, Stderr []byte
	Duration       time.Duration
	ExitCode       int // -1 if the process did not start or was killed by a signal
	RunErr         error

	binary   string
	timeout  time.Duration
	ctxErr   error // the parent context's error
	deadline bool  // the timeout expired
	started  bool
}

// Run executes s with timeout, terminating the process group on timeout or
// cancellation and reaping anything left behind when it exits.
func Run(ctx context.Context, timeout time.Duration, s Spec) Outcome {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, s.Binary, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	cmd.Stdin = s.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return terminate(cmd) }
	cmd.WaitDelay = KillGrace

	start := time.Now()
	runErr := cmd.Run()
	if cmd.Process != nil {
		killGroup(cmd) // reap anything the harness left behind
	}
	return Outcome{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: time.Since(start),
		ExitCode: cmd.ProcessState.ExitCode(),
		RunErr:   runErr,
		binary:   s.Binary,
		timeout:  timeout,
		ctxErr:   ctx.Err(),
		deadline: errors.Is(runCtx.Err(), context.DeadlineExceeded),
		started:  cmd.ProcessState != nil,
	}
}

// Failure classifies cancellation, timeout and start failures. It returns
// nil when the process ran to exit, whatever its exit code; the adapter then
// interprets the output.
func (o Outcome) Failure(meta harness.Meta) error {
	switch {
	case o.ctxErr != nil:
		return &harness.Error{Kind: harness.ErrCanceled, Msg: "execution canceled", Meta: meta, Err: o.ctxErr}
	case o.deadline:
		return &harness.Error{Kind: harness.ErrTimeout, Msg: fmt.Sprintf("no result within %s", o.timeout), Meta: meta}
	case !o.started:
		return &harness.Error{Kind: harness.ErrUnsupported, Msg: "start " + filepath.Base(o.binary), Meta: meta, Err: o.RunErr}
	}
	return nil
}

// ResolveBinary returns binary, or def if binary is empty. A relative path
// containing a separator, like ./bin/x, is relative to Sift's working
// directory but the harness runs elsewhere, so it is made absolute now. Bare
// names are left for PATH lookup.
func ResolveBinary(binary, def string) string {
	if binary == "" {
		binary = def
	}
	if strings.ContainsRune(binary, filepath.Separator) && !filepath.IsAbs(binary) {
		if abs, err := filepath.Abs(binary); err == nil {
			binary = abs
		}
	}
	return binary
}

// Tail formats the end of stderr for an error message, or "" if empty.
func Tail(stderr []byte) string {
	s := strings.TrimSpace(string(stderr))
	if s == "" {
		return ""
	}
	if len(s) > 500 {
		s = "…" + s[len(s)-500:]
	}
	return ": " + s
}

// Truncate shortens s to n bytes, marking the cut with "…".
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
