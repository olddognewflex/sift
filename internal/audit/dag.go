package audit

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/olddognewflex/sift/internal/contract"
)

// Task is one deterministic unit of audit work.
type Task struct {
	ID string
	// Needs lists tasks that must succeed first; if one does not, this task
	// is skipped.
	Needs []string
	// After lists tasks that must finish first, whatever their outcome.
	After []string
	Run   func(ctx context.Context) error
}

func (t Task) deps() []string { return append(slices.Clone(t.Needs), t.After...) }

// Order validates the task graph and returns task IDs in a deterministic
// topological order (ties broken by declaration order).
func Order(tasks []Task) ([]string, error) {
	index := map[string]int{}
	for i, t := range tasks {
		if t.ID == "" {
			return nil, errors.New("task with empty id")
		}
		if _, dup := index[t.ID]; dup {
			return nil, fmt.Errorf("duplicate task %q", t.ID)
		}
		index[t.ID] = i
	}
	for _, t := range tasks {
		for _, d := range t.deps() {
			if _, ok := index[d]; !ok {
				return nil, fmt.Errorf("task %q depends on unknown task %q", t.ID, d)
			}
			if d == t.ID {
				return nil, fmt.Errorf("task %q depends on itself", t.ID)
			}
		}
	}
	done := map[string]bool{}
	var order []string
	for len(order) < len(tasks) {
		progressed := false
		for _, t := range tasks {
			if done[t.ID] {
				continue
			}
			ready := true
			for _, d := range t.deps() {
				ready = ready && done[d]
			}
			if ready {
				done[t.ID] = true
				order = append(order, t.ID)
				progressed = true
			}
		}
		if !progressed {
			var stuck []string
			for _, t := range tasks {
				if !done[t.ID] {
					stuck = append(stuck, t.ID)
				}
			}
			return nil, fmt.Errorf("dependency cycle among tasks %v", stuck)
		}
	}
	return order, nil
}

// Observer is told about every task status change. Calls are serialized.
type Observer func(id string, status contract.TaskStatus, err error)

// Schedule runs tasks respecting dependencies, with at most parallel tasks
// running at once. It returns when every task has a final status. After
// cancellation no new task starts; unstarted tasks are marked interrupted.
func Schedule(ctx context.Context, tasks []Task, parallel int, observe Observer) error {
	order, err := Order(tasks)
	if err != nil {
		return err
	}
	if parallel < 1 {
		parallel = 1
	}
	byID := map[string]Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	status := map[string]contract.TaskStatus{}
	for _, id := range order {
		status[id] = contract.TaskPending
	}
	type outcome struct {
		id  string
		err error
	}
	results := make(chan outcome)
	running := 0

	for {
		// Settle tasks that can no longer run, then start ready ones.
		for _, id := range order {
			if status[id] != contract.TaskPending {
				continue
			}
			t := byID[id]
			if ctx.Err() != nil {
				status[id] = contract.TaskInterrupted
				observe(id, contract.TaskInterrupted, ctx.Err())
				continue
			}
			ready, blocked := true, ""
			for _, d := range t.Needs {
				switch s := status[d]; {
				case !s.Done():
					ready = false
				case s != contract.TaskSucceeded:
					blocked = d
				}
			}
			for _, d := range t.After {
				ready = ready && status[d].Done()
			}
			if blocked != "" {
				status[id] = contract.TaskSkipped
				observe(id, contract.TaskSkipped, fmt.Errorf("required task %q did not succeed", blocked))
				continue
			}
			if !ready || running >= parallel {
				continue
			}
			status[id] = contract.TaskRunning
			observe(id, contract.TaskRunning, nil)
			running++
			go func() { results <- outcome{id, runTask(ctx, t)} }()
		}
		if running == 0 {
			break
		}
		r := <-results
		running--
		switch {
		case r.err == nil:
			status[r.id] = contract.TaskSucceeded
		case ctx.Err() != nil:
			status[r.id] = contract.TaskInterrupted
		default:
			status[r.id] = contract.TaskFailed
		}
		observe(r.id, status[r.id], r.err)
	}
	for _, id := range order {
		if !status[id].Done() {
			return fmt.Errorf("internal error: task %q left in status %s", id, status[id])
		}
	}
	return nil
}

// runTask converts a panic in a task into an error so one task cannot take
// down the run without recording state.
func runTask(ctx context.Context, t Task) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("task %s panicked: %v", t.ID, p)
		}
	}()
	return t.Run(ctx)
}
