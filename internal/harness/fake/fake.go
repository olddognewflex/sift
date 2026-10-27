// Package fake provides a deterministic harness for tests.
package fake

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/olddognewflex/sift/internal/harness"
)

// Responder produces the output for one request. Returning an error makes
// the execution fail; use *harness.Error to choose the failure kind.
type Responder func(req harness.Request, call int) (json.RawMessage, error)

// Harness answers each request with the responder registered for its agent.
type Harness struct {
	ProbeErr error
	// Responders by agent name. Missing agents fail with ErrUnsupported.
	Responders map[string]Responder
	// Before, if set, runs before each request is answered (e.g. to block
	// until the context is canceled).
	Before func(ctx context.Context, req harness.Request) error

	mu    sync.Mutex
	calls []harness.Request
	count map[string]int
}

func (h *Harness) Probe(context.Context) (harness.Info, error) {
	return harness.Info{
		Name: "fake", Binary: "fake", Version: "1.0.0",
		Capabilities: []string{"structured-output"},
		Tools:        []string{"Read"},
		Enforcement:  []string{"fake harness: no tools are executed"},
		Unenforced:   []string{"fake harness enforces nothing"},
	}, h.ProbeErr
}

func (h *Harness) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	h.mu.Lock()
	h.calls = append(h.calls, req)
	if h.count == nil {
		h.count = map[string]int{}
	}
	h.count[req.Agent]++
	n := h.count[req.Agent]
	r := h.Responders[req.Agent]
	h.mu.Unlock()
	if h.Before != nil {
		if err := h.Before(ctx, req); err != nil {
			return harness.Result{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return harness.Result{}, &harness.Error{Kind: harness.ErrCanceled, Msg: "canceled", Err: err}
	}
	if r == nil {
		return harness.Result{}, &harness.Error{Kind: harness.ErrUnsupported, Msg: "no responder for agent " + req.Agent}
	}
	out, err := r(req, n)
	meta := harness.Meta{Model: "fake-model", SessionID: "fake-session", CostUSD: 0.01, NumTurns: 1}
	if err != nil {
		if he, ok := err.(*harness.Error); ok {
			he.Meta = meta
		}
		return harness.Result{}, err
	}
	return harness.Result{Output: out, Meta: meta}, nil
}

// Calls returns every request received, in order.
func (h *Harness) Calls() []harness.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]harness.Request(nil), h.calls...)
}

// JSON returns a responder that always answers with v.
func JSON(v any) Responder {
	data, err := json.Marshal(v)
	return func(harness.Request, int) (json.RawMessage, error) { return data, err }
}

// Fail returns a responder that always fails with kind.
func Fail(kind harness.ErrorKind, msg string) Responder {
	return func(harness.Request, int) (json.RawMessage, error) {
		return nil, &harness.Error{Kind: kind, Msg: msg}
	}
}
