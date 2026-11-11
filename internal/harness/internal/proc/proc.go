// Package proc owns the harness subprocess lifecycle: timeout, cancellation,
// the post-exit output drain, and the process-group kill. Every adapter runs
// its harness executions through Run so that the guarantee of leaving no
// child processes behind exists in one place.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/olddognewflex/sift/internal/harness"
)

// KillGrace is how long the harness gets to exit after SIGTERM.
const KillGrace = 10 * time.Second

// DrainGrace is how long Run keeps reading output after the harness exits.
// A background child that inherited stdout or stderr keeps the pipes open;
// once DrainGrace passes, Run kills the process group and reports a warning
// instead of waiting for it.
const DrainGrace = 2 * time.Second

// killDrain is how long the output pipes get to close after the group kill
// before Run closes its own ends. A holder outside the process group, or any
// holder on platforms without group kill, never closes them.
const killDrain = 500 * time.Millisecond

// Spec describes one harness process.
type Spec struct {
	Binary string
	Args   []string
	Dir    string
	Env    []string  // nil inherits the current environment
	Stdin  io.Reader // must not block: Run waits for it to be fully copied
}

// Outcome is what Run observed.
type Outcome struct {
	Stdout, Stderr []byte
	Duration       time.Duration
	ExitCode       int // -1 if the process did not start or was killed by a signal
	RunErr         error
	// Lingered reports that the output was still open DrainGrace after the
	// harness exited, so Run killed whatever held it. The output captured up
	// to then is kept.
	Lingered bool

	binary   string
	timeout  time.Duration
	ctxErr   error // the parent context's error
	deadline bool  // the timeout expired
	started  bool
}

// Run executes s with timeout, terminating the process group on timeout or
// cancellation and reaping anything left behind when it exits.
//
// Output is read until the pipes close. If a leftover child still holds them
// DrainGrace after the harness exits, Run kills the process group, keeps the
// output read so far, and sets Lingered. Timeout or cancellation during that
// drain kills the group at once.
func Run(ctx context.Context, timeout time.Duration, s Spec) Outcome {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, s.Binary, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return terminate(cmd) }
	// With *os.File stdio, exec starts no copying goroutines, so Wait returns
	// once the harness is reaped and WaitDelay only bounds SIGTERM → SIGKILL.
	cmd.WaitDelay = KillGrace

	start := time.Now()
	p, err := newPipes(s.Stdin != nil)
	if err == nil {
		p.attach(cmd)
		err = cmd.Start()
		if err != nil {
			p.closeAll()
		}
	}
	if err != nil {
		return Outcome{
			Duration: time.Since(start),
			ExitCode: cmd.ProcessState.ExitCode(),
			RunErr:   err,
			binary:   s.Binary,
			timeout:  timeout,
			ctxErr:   ctx.Err(),
			deadline: errors.Is(runCtx.Err(), context.DeadlineExceeded),
			started:  cmd.ProcessState != nil,
		}
	}
	p.closeChildEnds()
	var stdout, stderr bytes.Buffer
	done := p.copy(s.Stdin, &stdout, &stderr)

	runErr := cmd.Wait()
	lingered := drain(runCtx, cmd, p, done)
	killGroup(cmd) // reap anything the harness left behind
	if lingered && runErr == nil {
		runErr = fmt.Errorf("output still open %s after harness exit: %w", DrainGrace, exec.ErrWaitDelay)
	}
	return Outcome{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: time.Since(start),
		ExitCode: cmd.ProcessState.ExitCode(),
		RunErr:   runErr,
		Lingered: lingered,
		binary:   s.Binary,
		timeout:  timeout,
		ctxErr:   ctx.Err(),
		deadline: errors.Is(runCtx.Err(), context.DeadlineExceeded),
		started:  cmd.ProcessState != nil,
	}
}

// drain waits for the output copies to finish after the harness has exited.
// It reports whether the output was still open after DrainGrace. Timeout or
// cancellation cuts the wait short without counting as lingering.
func drain(runCtx context.Context, cmd *exec.Cmd, p *pipes, done <-chan struct{}) (lingered bool) {
	defer p.closeParentEnds()
	select {
	case <-done:
		return false
	default:
	}
	grace := time.NewTimer(DrainGrace)
	defer grace.Stop()
	select {
	case <-done:
		return false
	case <-runCtx.Done():
	case <-grace.C:
		lingered = true
	}
	killGroup(cmd)
	wait := time.NewTimer(killDrain)
	defer wait.Stop()
	select {
	case <-done:
	case <-wait.C:
		// Unblocks the copies when the holder survived the group kill.
		p.closeParentEnds()
		<-done
	}
	return lingered
}

// pipes are the harness's stdio. The child ends go to the process; Run keeps
// the parent ends.
type pipes struct {
	stdinR, stdinW   *os.File // nil without stdin
	stdoutR, stdoutW *os.File
	stderrR, stderrW *os.File
}

func newPipes(stdin bool) (*pipes, error) {
	p := &pipes{}
	var err error
	if p.stdoutR, p.stdoutW, err = os.Pipe(); err != nil {
		return nil, err
	}
	if p.stderrR, p.stderrW, err = os.Pipe(); err != nil {
		p.closeAll()
		return nil, err
	}
	if stdin {
		if p.stdinR, p.stdinW, err = os.Pipe(); err != nil {
			p.closeAll()
			return nil, err
		}
	}
	return p, nil
}

func (p *pipes) attach(cmd *exec.Cmd) {
	if p.stdinR != nil {
		cmd.Stdin = p.stdinR // otherwise exec supplies the null device
	}
	cmd.Stdout, cmd.Stderr = p.stdoutW, p.stderrW
}

// copy starts the goroutines that feed stdin and collect stdout and stderr.
// The returned channel closes when all of them have finished; the buffers
// must not be read before then.
func (p *pipes) copy(stdin io.Reader, stdout, stderr *bytes.Buffer) <-chan struct{} {
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = io.Copy(stdout, p.stdoutR) })
	wg.Go(func() { _, _ = io.Copy(stderr, p.stderrR) })
	if p.stdinW != nil {
		wg.Go(func() {
			// A harness that exits without reading all of stdin breaks the
			// pipe; that is its business.
			_, _ = io.Copy(p.stdinW, stdin)
			_ = p.stdinW.Close()
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	return done
}

// closeChildEnds closes Run's copies of the process's ends, so that the
// pipes report EOF once the process and everything it spawned let go.
func (p *pipes) closeChildEnds() { closeFiles(p.stdinR, p.stdoutW, p.stderrW) }

func (p *pipes) closeParentEnds() { closeFiles(p.stdinW, p.stdoutR, p.stderrR) }

func (p *pipes) closeAll() {
	p.closeChildEnds()
	p.closeParentEnds()
}

// closeFiles closes each non-nil file. Closing twice is harmless.
func closeFiles(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

// Warnings describes non-fatal anomalies Run observed.
func (o Outcome) Warnings() []string {
	if !o.Lingered {
		return nil
	}
	return []string{fmt.Sprintf("a leftover process held the harness's stdio open %s after it exited; Run killed its process group and stopped reading", DrainGrace)}
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
