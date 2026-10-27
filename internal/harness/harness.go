// Package harness defines the narrow boundary between Sift and the coding
// agent platforms that execute its agents.
//
// A harness runs one agent to completion in a scoped, read-only working
// directory and returns structured output that conforms to a JSON Schema.
// Everything else (scheduling, retries, artifacts, validation) belongs to the
// orchestrator.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Harness executes agents.
type Harness interface {
	// Probe verifies the harness is installed and supports every capability
	// Sift relies on. It must fail rather than degrade silently.
	Probe(ctx context.Context) (Info, error)
	// Run executes one agent. Implementations must honor ctx cancellation
	// and Request.Timeout, and must not leave child processes running.
	Run(ctx context.Context, req Request) (Result, error)
}

// Info identifies a harness installation.
type Info struct {
	Name         string
	Binary       string
	Version      string
	Capabilities []string
	// Enforcement lists the restrictions the harness enforces itself.
	Enforcement []string
	// Unenforced lists isolation properties the harness cannot guarantee.
	Unenforced []string
	// Tools lists the tools exposed to agents.
	Tools []string
}

// Request is one agent execution.
type Request struct {
	Agent        string
	Instructions string // the agent definition, used as the system prompt
	Prompt       string // task input and context
	WorkDir      string // the only directory the agent may read
	OutputSchema []byte // JSON Schema the output must satisfy
	Model        string // empty selects the harness default
	Timeout      time.Duration
	MaxBudgetUSD float64
	// LogDir receives raw harness output for audit trails. Optional.
	LogDir string
}

// Result is a successful execution.
type Result struct {
	Output json.RawMessage
	Meta   Meta
}

// Meta describes an execution, successful or not.
type Meta struct {
	Model             string
	SessionID         string
	CostUSD           float64
	NumTurns          int
	PermissionDenials int
	ExitCode          int
	Duration          time.Duration
}

// ErrorKind classifies execution failures so the orchestrator can decide
// whether to retry and how to report.
type ErrorKind string

const (
	ErrProcess       ErrorKind = "process_failed"
	ErrInvalidOutput ErrorKind = "invalid_output"
	ErrTimeout       ErrorKind = "timeout"
	ErrCanceled      ErrorKind = "canceled"
	ErrBudget        ErrorKind = "budget_exceeded"
	ErrUnsupported   ErrorKind = "unsupported"
)

// Retryable reports whether another attempt could plausibly succeed.
func (k ErrorKind) Retryable() bool { return k == ErrProcess || k == ErrInvalidOutput }

// Error is an execution failure. Meta carries whatever was observed.
type Error struct {
	Kind ErrorKind
	Msg  string
	Meta Meta
	Err  error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s: %s", e.Kind, e.Msg)
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the ErrorKind of err, or ErrProcess for foreign errors.
func KindOf(err error) ErrorKind {
	var he *Error
	if errors.As(err, &he) {
		return he.Kind
	}
	if errors.Is(err, context.Canceled) {
		return ErrCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return ErrProcess
}

// MetaOf returns the Meta carried by err, if any.
func MetaOf(err error) Meta {
	var he *Error
	if errors.As(err, &he) {
		return he.Meta
	}
	return Meta{}
}
