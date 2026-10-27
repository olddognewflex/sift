package audit

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/contract"
)

func noop(context.Context) error { return nil }

func TestOrder(t *testing.T) {
	tasks := []Task{
		{ID: "report", After: []string{"b", "c"}},
		{ID: "a"},
		{ID: "b", Needs: []string{"a"}},
		{ID: "c", Needs: []string{"a"}},
	}
	got, err := Order(tasks)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b", "c", "report"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestOrderRejectsBadGraphs(t *testing.T) {
	cases := map[string][]Task{
		"cycle":     {{ID: "a", Needs: []string{"b"}}, {ID: "b", After: []string{"a"}}},
		"self":      {{ID: "a", Needs: []string{"a"}}},
		"unknown":   {{ID: "a", Needs: []string{"ghost"}}},
		"duplicate": {{ID: "a"}, {ID: "a"}},
		"empty id":  {{ID: ""}},
	}
	for name, tasks := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Order(tasks); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

type recorder struct {
	mu     sync.Mutex
	events []string
	final  map[string]contract.TaskStatus
}

func (r *recorder) observe(id string, s contract.TaskStatus, _ error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.final == nil {
		r.final = map[string]contract.TaskStatus{}
	}
	r.events = append(r.events, id+":"+string(s))
	r.final[id] = s
}

func TestScheduleFailurePropagation(t *testing.T) {
	var ran []string
	var mu sync.Mutex
	track := func(id string, err error) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			ran = append(ran, id)
			mu.Unlock()
			return err
		}
	}
	tasks := []Task{
		{ID: "discovery", Run: track("discovery", nil)},
		{ID: "arch", Needs: []string{"discovery"}, Run: track("arch", errors.New("boom"))},
		{ID: "corr", Needs: []string{"discovery"}, Run: track("corr", nil)},
		{ID: "needs-arch", Needs: []string{"arch"}, Run: track("needs-arch", nil)},
		{ID: "validate", Needs: []string{"discovery"}, After: []string{"arch", "corr"}, Run: track("validate", nil)},
	}
	var r recorder
	if err := Schedule(context.Background(), tasks, 2, r.observe); err != nil {
		t.Fatal(err)
	}
	want := map[string]contract.TaskStatus{
		"discovery": contract.TaskSucceeded, "arch": contract.TaskFailed, "corr": contract.TaskSucceeded,
		"needs-arch": contract.TaskSkipped, "validate": contract.TaskSucceeded,
	}
	for id, s := range want {
		if r.final[id] != s {
			t.Errorf("%s = %s, want %s", id, r.final[id], s)
		}
	}
	if slices.Contains(ran, "needs-arch") {
		t.Error("task with failed requirement ran")
	}
	if ran[len(ran)-1] != "validate" {
		t.Errorf("validate must run after its After deps; order %v", ran)
	}
}

func TestScheduleRespectsParallelism(t *testing.T) {
	var cur, peak atomic.Int32
	work := func(context.Context) error {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		return nil
	}
	var tasks []Task
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		tasks = append(tasks, Task{ID: id, Run: work})
	}
	var r recorder
	if err := Schedule(context.Background(), tasks, 2, r.observe); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency = %d, want 2", peak.Load())
	}
}

func TestScheduleCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tasks := []Task{
		{ID: "slow", Run: func(ctx context.Context) error { cancel(); <-ctx.Done(); return ctx.Err() }},
		{ID: "next", Needs: []string{"slow"}, Run: noop},
		{ID: "after", After: []string{"slow"}, Run: noop},
	}
	var r recorder
	if err := Schedule(ctx, tasks, 1, r.observe); err != nil {
		t.Fatal(err)
	}
	if r.final["slow"] != contract.TaskInterrupted {
		t.Errorf("slow = %s", r.final["slow"])
	}
	// "next" is skipped (its requirement did not succeed); "after" never starts.
	if r.final["next"] != contract.TaskSkipped && r.final["next"] != contract.TaskInterrupted {
		t.Errorf("next = %s", r.final["next"])
	}
	if r.final["after"] != contract.TaskInterrupted {
		t.Errorf("after = %s", r.final["after"])
	}
}

func TestSchedulePanicBecomesFailure(t *testing.T) {
	var r recorder
	var gotErr error
	obs := func(id string, s contract.TaskStatus, err error) {
		r.observe(id, s, err)
		if s == contract.TaskFailed {
			gotErr = err
		}
	}
	tasks := []Task{{ID: "p", Run: func(context.Context) error { panic("kaboom") }}}
	if err := Schedule(context.Background(), tasks, 1, obs); err != nil {
		t.Fatal(err)
	}
	if r.final["p"] != contract.TaskFailed || gotErr == nil || !strings.Contains(gotErr.Error(), "kaboom") {
		t.Fatalf("p = %s, err %v", r.final["p"], gotErr)
	}
}
