// Package audit orchestrates an audit run.
//
// The orchestrator owns the lifecycle: it records the repository and
// configuration, exports the read-only snapshot, runs a fixed task graph
// (discovery → plan → reviewers → validation), applies Sift's evidence gate,
// and finalizes the run with a report. Agents only analyze; they never
// decide which step runs next.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/olddognewflex/sift/internal/agent"
	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/findings"
	"github.com/olddognewflex/sift/internal/harness"
	"github.com/olddognewflex/sift/internal/report"
	"github.com/olddognewflex/sift/internal/schema"
	"github.com/olddognewflex/sift/internal/source"
	"github.com/olddognewflex/sift/internal/storage"
)

const (
	taskDiscovery  = "discovery"
	taskPlan       = "plan"
	taskValidation = "validation"
	taskReport     = "report"

	discoveryAgent = "discovery"
	validatorAgent = "pruv"
)

// Options configures one audit.
type Options struct {
	RepoPath    string
	Config      config.Config
	Harness     harness.Harness
	Store       storage.Store
	SiftVersion string
	// Log receives human-readable progress lines. Optional.
	Log io.Writer
	// Now is the clock; defaults to time.Now.
	Now func() time.Time
}

// Result is a finished (or stopped) run.
type Result struct {
	Run      *storage.Run
	Manifest contract.Manifest
}

type auditor struct {
	opts     Options
	run      *storage.Run
	defs     map[string]agent.Definition
	revision string
	snapshot string
	schemas  map[string][]byte

	logMu   sync.Mutex // serializes writes to opts.Log
	mu      sync.Mutex // guards m and saveErr
	m       *contract.Manifest
	saveErr error

	// Set by a task before its dependents start (ordered by the scheduler).
	discovery contract.DiscoveryReport
	plan      contract.ReviewPlan
}

// Run performs an audit. Setup failures (invalid repository, refused dirty
// tree, unsupported harness) return an error before any run is created.
// Once a run exists, Run returns it together with any error; the manifest
// status says whether the audit completed.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	cfg := opts.Config
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	hinfo, err := opts.Harness.Probe(ctx)
	if err != nil {
		return nil, fmt.Errorf("harness %s is not usable: %w", cfg.Harness.Name, err)
	}
	st, err := source.Inspect(ctx, opts.RepoPath)
	if err != nil {
		return nil, err
	}
	policy := cfg.Audit.DirtyPolicy
	if st.Dirty() && policy == contract.DirtyRefuse {
		return nil, source.DirtyError(st)
	}
	if err := checkSeparate(st.Root, opts.Store.Root); err != nil {
		return nil, err
	}
	names := append([]string{discoveryAgent}, cfg.Audit.Reviewers...)
	names = append(names, validatorAgent)
	defs, err := agent.Load(cfg.Agents.Dir, names...)
	if err != nil {
		return nil, err
	}
	schemas, err := agentSchemas()
	if err != nil {
		return nil, err
	}
	before, err := source.Fingerprint(ctx, st.Root)
	if err != nil {
		return nil, err
	}

	id, err := storage.NewRunID(opts.Now())
	if err != nil {
		return nil, err
	}
	run, err := opts.Store.Create(id)
	if err != nil {
		return nil, err
	}
	a := &auditor{opts: opts, run: run, defs: defs, snapshot: run.Path(storage.SourceDir), schemas: schemas}
	if err := a.initialize(ctx, st, hinfo); err != nil {
		a.fail(err)
		return &Result{Run: run, Manifest: a.manifest()}, err
	}

	tasks := a.tasks()
	a.logf("run %s: auditing %s at %s", run.ID, st.Root, a.revision)
	if err := Schedule(ctx, tasks, cfg.Audit.MaxParallelAgents, a.observe); err != nil {
		a.fail(err)
		return &Result{Run: run, Manifest: a.manifest()}, err
	}
	err = a.finalize(ctx, st.Root, before)
	return &Result{Run: run, Manifest: a.manifest()}, err
}

// checkSeparate refuses a runs directory inside the audited repository.
func checkSeparate(repoRoot, runsDir string) error {
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return fmt.Errorf("create runs directory: %w", err)
	}
	repo, err1 := filepath.EvalSymlinks(repoRoot)
	runs, err2 := filepath.EvalSymlinks(runsDir)
	if err := errors.Join(err1, err2); err != nil {
		return err
	}
	if rel, err := filepath.Rel(repo, runs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("runs directory %s is inside the audited repository %s; set storage.runs_dir elsewhere so the audit does not write to the repository", runsDir, repoRoot)
	}
	return nil
}

func agentSchemas() (map[string][]byte, error) {
	out := map[string][]byte{}
	var err error
	if out[discoveryAgent], err = schema.For[contract.DiscoveryReport](""); err != nil {
		return nil, err
	}
	if out["review"], err = schema.For[contract.ReviewReport](""); err != nil {
		return nil, err
	}
	if out[validatorAgent], err = schema.For[contract.ValidationDecision](""); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *auditor) initialize(ctx context.Context, st source.State, hinfo harness.Info) error {
	cfg := a.opts.Config
	now := a.opts.Now().UTC()
	host, _ := os.Hostname()
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	a.m = &contract.Manifest{
		Header:      contract.NewHeader(contract.KindManifest, a.run.ID),
		SiftVersion: a.opts.SiftVersion,
		Status:      contract.RunRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
		PID:         os.Getpid(),
		Host:        host,
		Repository: contract.Repository{
			Path: st.Root, Commit: st.Commit, Branch: st.Branch,
			Dirty: st.Dirty(), StatusPorcelain: st.Porcelain, DirtyPolicy: cfg.Audit.DirtyPolicy,
			SourceRevision: st.Commit, // refined after the snapshot is taken
		},
		Config: cfgJSON,
		Harness: contract.HarnessInfo{
			Name: hinfo.Name, Binary: hinfo.Binary, Version: hinfo.Version,
			Model: cfg.Harness.Model, Capabilities: hinfo.Capabilities,
		},
		Policy: contract.Policy{
			Tools: hinfo.Tools, CommandExecution: false, Network: false,
			Enforcement: hinfo.Enforcement, Unenforced: hinfo.Unenforced,
		},
	}
	for _, t := range a.tasks() {
		a.m.Tasks = append(a.m.Tasks, contract.TaskState{ID: t.ID, DependsOn: t.deps(), Status: contract.TaskPending})
	}
	a.m.Tasks = append(a.m.Tasks, contract.TaskState{ID: taskReport, DependsOn: a.taskIDs(), Status: contract.TaskPending})
	for _, name := range slices.Sorted(maps.Keys(a.defs)) {
		d := a.defs[name]
		model, _, _ := cfg.Limits(name)
		a.m.Agents = append(a.m.Agents, contract.AgentRef{Name: d.Name, Version: d.Version, SHA256: d.SHA256, Source: d.Source, Model: model})
		if err := a.run.WriteFile(path.Join(storage.AgentsDir, name+".md"), d.Raw); err != nil {
			return err
		}
	}
	if st.Dirty() && cfg.Audit.DirtyPolicy == contract.DirtyHead {
		a.m.Warnings = append(a.m.Warnings, fmt.Sprintf("working tree has %d uncommitted path(s); they were excluded (dirty_policy=head)", len(st.Porcelain)))
	}
	if err := a.save(); err != nil {
		return err
	}

	snap, err := source.Export(ctx, st, cfg.Audit.DirtyPolicy, a.snapshot)
	if err != nil {
		return fmt.Errorf("export snapshot: %w", err)
	}
	a.revision = source.Revision(st, cfg.Audit.DirtyPolicy, snap)
	return a.update(func(m *contract.Manifest) error {
		r := &m.Repository
		r.SourceRevision, r.SnapshotDigest, r.SnapshotFiles, r.SkippedPaths = a.revision, snap.Digest, snap.Files, snap.Skipped
		if snap.Diff != nil {
			if err := a.run.WriteFile(storage.SourceDiffFile, snap.Diff); err != nil {
				return err
			}
			r.DiffSHA256 = sha256Hex(snap.Diff)
			m.Warnings = append(m.Warnings, "uncommitted changes were audited (dirty_policy=include); evidence is reproducible from the run's source snapshot and source.diff, not from the commit alone")
		}
		if snap.Files == 0 {
			return errors.New("the audited revision contains no regular files")
		}
		return nil
	})
}

func (a *auditor) tasks() []Task {
	cfg := a.opts.Config
	tasks := []Task{
		{ID: taskDiscovery, Run: a.runDiscovery},
		{ID: taskPlan, Needs: []string{taskDiscovery}, Run: a.runPlan},
	}
	var reviews []string
	for _, r := range cfg.Audit.Reviewers {
		id := reviewTaskID(r)
		reviews = append(reviews, id)
		tasks = append(tasks, Task{ID: id, Needs: []string{taskPlan}, Run: func(ctx context.Context) error { return a.runReview(ctx, r) }})
	}
	tasks = append(tasks, Task{ID: taskValidation, Needs: []string{taskPlan}, After: reviews, Run: a.runValidation})
	return tasks
}

func (a *auditor) taskIDs() []string {
	var ids []string
	for _, t := range a.tasks() {
		ids = append(ids, t.ID)
	}
	return ids
}

// ---------------------------------------------------------------------------
// Tasks

func (a *auditor) runDiscovery(ctx context.Context) error {
	rep, err := execute[contract.DiscoveryReport](ctx, a, taskDiscovery, taskDiscovery, a.defs[discoveryAgent], discoveryPrompt(a.revision, a.limits(discoveryAgent)), a.schemas[discoveryAgent])
	if err != nil {
		return err
	}
	a.discovery = rep
	if rep.Completeness == contract.CompletenessPartial {
		a.warn("discovery reported partial completeness; reviews may have missed areas (see discovery unknowns)")
	}
	return a.writeArtifact(taskDiscovery, storage.DiscoveryFile, &contract.Discovery{
		Header: contract.NewHeader(contract.KindDiscovery, a.run.ID), SourceRevision: a.revision, Report: rep,
	})
}

func (a *auditor) runPlan(context.Context) error {
	cfg := a.opts.Config
	a.plan = planReview(a.run.ID, cfg.Audit.Reviewers, cfg.Audit.MaxFindingsPerReviewer, a.discovery)
	return a.writeArtifact(taskPlan, storage.ReviewPlanFile, &a.plan)
}

func (a *auditor) runReview(ctx context.Context, reviewer string) error {
	var task contract.ReviewTask
	for _, t := range a.plan.Tasks {
		if t.Agent == reviewer {
			task = t
		}
	}
	max := a.plan.MaxFindingsPerRun
	rep, err := execute[contract.ReviewReport](ctx, a, task.ID, task.ID, a.defs[reviewer], reviewPrompt(a.revision, task, max, a.discovery, a.limits(reviewer)), a.schemas["review"])
	if err != nil {
		return err
	}
	if len(rep.Findings) > max {
		a.warn(fmt.Sprintf("%s reviewer returned %d findings; only the first %d were kept", reviewer, len(rep.Findings), max))
		rep.Findings = rep.Findings[:max]
	}
	return a.writeArtifact(task.ID, fmt.Sprintf(storage.ReviewFilePattn, reviewer), &contract.Review{
		Header: contract.NewHeader(contract.KindReview, a.run.ID), TaskID: task.ID, Agent: reviewer, Category: task.Category, Report: rep,
	})
}

func (a *auditor) runValidation(ctx context.Context) error {
	// Reviews are read back from disk: the stage boundary is the artifact.
	var candidates []contract.Finding
	for _, t := range a.plan.Tasks {
		rel := fmt.Sprintf(storage.ReviewFilePattn, t.Agent)
		if !a.run.Exists(rel) {
			continue // that reviewer failed; the run is already partial
		}
		var rv contract.Review
		if err := a.run.ReadJSON(rel, &rv); err != nil {
			return err
		}
		candidates = append(candidates, findings.Assign(rv.Report.Findings, rv.Category, rv.Agent, a.revision, a.snapshot)...)
	}
	cands := &contract.FindingSet{Header: contract.NewHeader(contract.KindFindings, a.run.ID), Findings: candidates}
	if err := a.writeArtifact(taskValidation, storage.CandidatesFile, cands); err != nil {
		return err
	}

	pruv := a.defs[validatorAgent]
	records := make([]*contract.DecisionRecord, len(candidates))
	var mu sync.Mutex
	persist := func() error {
		mu.Lock()
		defer mu.Unlock()
		set := &contract.DecisionSet{Header: contract.NewHeader(contract.KindDecisions, a.run.ID), Decisions: []contract.DecisionRecord{}}
		for _, r := range records {
			if r != nil {
				set.Decisions = append(set.Decisions, *r)
			}
		}
		return a.run.WriteJSON(storage.DecisionsFile, set)
	}
	if err := persist(); err != nil {
		return err
	}

	sem := make(chan struct{}, a.opts.Config.Audit.MaxParallelAgents)
	var wg sync.WaitGroup
	var failed, persistErrs []error
	for i, f := range candidates {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			rec := contract.DecisionRecord{FindingID: f.ID, Validator: pruv.Name, ValidatorVersion: pruv.Version}
			dec, err := execute[contract.ValidationDecision](ctx, a, taskValidation, path.Join(taskValidation, f.ID), pruv, pruvPrompt(a.revision, f, a.discovery, a.limits(validatorAgent)), a.schemas[validatorAgent])
			if err != nil {
				rec.Error = err.Error()
				rec.FinalState, rec.GateReason = findings.Gate(f.EvidenceChecks, nil, nil)
			} else {
				rec.Decision = &dec
				rec.AdditionalChecks = findings.CheckAll(a.snapshot, dec.AdditionalEvidence)
				rec.FinalState, rec.GateReason = findings.Gate(f.EvidenceChecks, &dec, rec.AdditionalChecks)
			}
			a.logf("[validation] %s → %s", f.ID, rec.FinalState)
			mu.Lock()
			records[i] = &rec
			if err != nil && ctx.Err() == nil {
				failed = append(failed, fmt.Errorf("%s: %w", f.ID, err))
			}
			mu.Unlock()
			if err := persist(); err != nil {
				mu.Lock()
				persistErrs = append(persistErrs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(persistErrs...); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	var decisions contract.DecisionSet
	if err := a.run.ReadJSON(storage.DecisionsFile, &decisions); err != nil {
		return err
	}
	merged, err := findings.Merge(candidates, decisions.Decisions)
	if err != nil {
		return err
	}
	final := &contract.FindingSet{Header: contract.NewHeader(contract.KindFindings, a.run.ID), Findings: merged}
	if err := a.writeArtifact(taskValidation, storage.FindingsFile, final); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d findings could not be validated: %w", len(failed), len(candidates), errors.Join(failed...))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Agent execution

// execute runs one agent with retries, persisting the prompt, raw output
// and an execution record for every attempt, and returns the strictly
// decoded, validated output.
func execute[T any, PT interface {
	*T
	contract.Validator
}](ctx context.Context, a *auditor, taskID, execKey string, def agent.Definition, prompt string, outSchema []byte) (T, error) {
	cfg := a.opts.Config
	model, timeout, budget := cfg.Limits(def.Name)
	attempts := 1 + cfg.Agents.Retries
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		rel := path.Join(storage.ExecutionsDir, execKey, fmt.Sprintf("attempt-%d", attempt))
		p := prompt
		if lastErr != nil {
			p += fmt.Sprintf("\n## Previous attempt\n\nThe previous attempt was rejected: %s\nReturn output that satisfies the schema and the rules in your instructions.\n", truncate(lastErr.Error(), 2000))
		}
		if err := a.run.WriteFile(path.Join(rel, "prompt.md"), []byte(p)); err != nil {
			return *new(T), err
		}
		start := a.opts.Now()
		res, err := a.opts.Harness.Run(ctx, harness.Request{
			Agent: def.Name, Instructions: def.Instructions, Prompt: p, WorkDir: a.snapshot,
			OutputSchema: outSchema, Model: model, Timeout: timeout, MaxBudgetUSD: budget,
			LogDir: a.run.Path(rel),
		})
		meta := res.Meta
		var out T
		if err != nil {
			meta = harness.MetaOf(err)
		} else {
			if werr := a.run.WriteFile(path.Join(rel, "output.json"), res.Output); werr != nil {
				return out, werr
			}
			if derr := contract.Decode(res.Output, PT(&out)); derr != nil {
				err = &harness.Error{Kind: harness.ErrInvalidOutput, Msg: "structured output failed validation", Meta: meta, Err: derr}
			}
		}
		end := a.opts.Now()
		rec := &contract.ExecutionResult{
			Header: contract.NewHeader(contract.KindExecution, a.run.ID),
			TaskID: taskID, Agent: def.Name, AgentVersion: def.Version, Attempt: attempt,
			Succeeded: err == nil, StartedAt: start.UTC(), EndedAt: end.UTC(), DurationMS: end.Sub(start).Milliseconds(),
			Harness: a.m.Harness.Name, HarnessVersion: a.m.Harness.Version,
			Model: meta.Model, SessionID: meta.SessionID, CostUSD: meta.CostUSD, NumTurns: meta.NumTurns,
			PermissionDenials: meta.PermissionDenials, ExitCode: meta.ExitCode, Dir: rel,
		}
		if err != nil {
			rec.ErrorKind, rec.Error = string(harness.KindOf(err)), err.Error()
		}
		resultRel := path.Join(rel, "result.json")
		if werr := a.run.WriteJSON(resultRel, rec); werr != nil {
			return out, werr
		}
		if uerr := a.update(func(m *contract.Manifest) error {
			t := m.Task(taskID)
			t.Executions = append(t.Executions, resultRel)
			return nil
		}); uerr != nil {
			return out, uerr
		}
		if meta.PermissionDenials > 0 {
			a.warn(fmt.Sprintf("%s (%s) attempted %d out-of-policy tool call(s); the harness denied them (see %s)", def.Name, execKey, meta.PermissionDenials, rel))
		}
		if err == nil {
			return out, nil
		}
		lastErr = err
		a.logf("[%s] %s attempt %d failed: %s", taskID, def.Name, attempt, truncate(err.Error(), 300))
		if ctx.Err() != nil || !harness.KindOf(err).Retryable() {
			break
		}
	}
	return *new(T), fmt.Errorf("agent %s: %w", def.Name, lastErr)
}

// ---------------------------------------------------------------------------
// State

func (a *auditor) observe(id string, status contract.TaskStatus, err error) {
	now := a.opts.Now().UTC()
	_ = a.update(func(m *contract.Manifest) error {
		t := m.Task(id)
		t.Status = status
		switch {
		case status == contract.TaskRunning:
			t.StartedAt = &now
		case status.Done():
			t.EndedAt = &now
		}
		if err != nil {
			t.Error = err.Error()
		}
		return nil
	})
	msg := string(status)
	if err != nil {
		msg += ": " + truncate(err.Error(), 300)
	}
	a.logf("[%s] %s", id, msg)
}

func (a *auditor) writeArtifact(taskID, rel string, v contract.Validator) error {
	if err := a.run.WriteJSON(rel, v); err != nil {
		return err
	}
	return a.update(func(m *contract.Manifest) error {
		t := m.Task(taskID)
		t.Artifacts = append(t.Artifacts, rel)
		return nil
	})
}

func (a *auditor) warn(msg string) {
	_ = a.update(func(m *contract.Manifest) error {
		m.Warnings = append(m.Warnings, msg)
		return nil
	})
	a.logf("warning: %s", msg)
}

// update applies fn to the manifest and persists it atomically.
func (a *auditor) update(fn func(*contract.Manifest) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := fn(a.m); err != nil {
		return err
	}
	return a.saveLocked()
}

func (a *auditor) save() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.saveLocked()
}

func (a *auditor) saveLocked() error {
	a.m.UpdatedAt = a.opts.Now().UTC()
	err := a.run.WriteJSON(storage.ManifestFile, a.m)
	if err != nil && a.saveErr == nil {
		a.saveErr = err
	}
	return err
}

func (a *auditor) manifest() contract.Manifest {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		return contract.Manifest{}
	}
	return *a.m
}

// fail marks the run failed after an orchestration error.
func (a *auditor) fail(cause error) {
	if a.m == nil {
		return
	}
	now := a.opts.Now().UTC()
	_ = a.update(func(m *contract.Manifest) error {
		m.Status, m.FinishedAt = contract.RunFailed, &now
		m.Warnings = append(m.Warnings, "run failed: "+cause.Error())
		return nil
	})
}

func (a *auditor) finalize(ctx context.Context, repoRoot, before string) error {
	status := a.status(ctx)
	integrity := &contract.Integrity{RepositoryUnchanged: true, Detail: "HEAD, status and uncommitted diff are identical before and after the audit"}
	// Use a fresh context: an interrupted audit still records its state.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if after, err := source.Fingerprint(fctx, repoRoot); err != nil {
		integrity = &contract.Integrity{Detail: "could not re-inspect the repository: " + err.Error()}
	} else if after != before {
		integrity = &contract.Integrity{Detail: "the repository changed during the audit (HEAD, status or uncommitted diff differ); agents had no write access, so this was most likely an outside change, but findings may not match the current working tree"}
	}
	now := a.opts.Now().UTC()
	if err := a.update(func(m *contract.Manifest) error {
		m.Status, m.Integrity = status, integrity
		t := m.Task(taskReport)
		t.Status, t.StartedAt = contract.TaskRunning, &now
		return nil
	}); err != nil {
		return err
	}

	_, rerr := report.Generate(a.run, a.opts.Now)
	end := a.opts.Now().UTC()
	err := a.update(func(m *contract.Manifest) error {
		t := m.Task(taskReport)
		t.EndedAt = &end
		m.FinishedAt = &end
		if rerr != nil {
			t.Status, t.Error = contract.TaskFailed, rerr.Error()
			m.Status = contract.RunFailed
			return nil
		}
		t.Status = contract.TaskSucceeded
		t.Artifacts = []string{storage.ReportDir}
		return nil
	})
	final := a.manifest()
	a.logf("[report] %s", final.Task(taskReport).Status)
	if rerr != nil {
		return fmt.Errorf("generate report: %w", rerr)
	}
	if err != nil {
		return err
	}
	return a.saveErr
}

// status derives the run outcome from task outcomes.
func (a *auditor) status(ctx context.Context) contract.RunStatus {
	m := a.manifest()
	if ctx.Err() != nil {
		return contract.RunInterrupted
	}
	if t := m.Task(taskDiscovery); t == nil || t.Status != contract.TaskSucceeded {
		return contract.RunFailed
	}
	for _, t := range m.Tasks {
		if t.ID != taskReport && t.Status != contract.TaskSucceeded {
			return contract.RunPartial
		}
	}
	return contract.RunCompleted
}

func (a *auditor) limits(agent string) limits {
	_, timeout, budget := a.opts.Config.Limits(agent)
	return limits{Timeout: timeout, Budget: budget}
}

func (a *auditor) logf(format string, args ...any) {
	if a.opts.Log != nil {
		a.logMu.Lock()
		defer a.logMu.Unlock()
		fmt.Fprintf(a.opts.Log, "sift: "+format+"\n", args...)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
