package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/olddognewflex/sift/internal/agent"
	"github.com/olddognewflex/sift/internal/audit"
	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/cost"
	"github.com/olddognewflex/sift/internal/harness"
	"github.com/olddognewflex/sift/internal/report"
	"github.com/olddognewflex/sift/internal/source"
	"github.com/olddognewflex/sift/internal/storage"
)

// ErrNoBudget is returned by Run without a budget, together with results
// that hold only the estimate: nothing is spent.
var ErrNoBudget = errors.New("no benchmark budget set: estimate only, nothing spent")

// MaxTrials bounds Options.Trials.
const MaxTrials = 10

// Options configures a live benchmark run.
type Options struct {
	Fixtures []Fixture
	// Config is the audit configuration. Run sets its runs_dir to RunsDir
	// and, for each audit, its cost.budget_usd to the budget left.
	Config  config.Config
	Harness harness.Harness
	// Budget is the spend limit in USD for the whole benchmark; 0 means
	// estimate only.
	Budget float64
	// BudgetKey names the budget setting in messages (default "budget").
	BudgetKey string
	// Trials is the number of audits per fixture, 1 to MaxTrials; 0 means 1.
	Trials  int
	RunsDir string
	// Log receives the estimate and progress lines, the audits' included.
	// Optional.
	Log io.Writer
	// Repo materializes a fixture's files as a committed Git repository
	// outside RunsDir and returns its path.
	Repo func(files map[string]string) (string, error)
	// Now is the clock; defaults to time.Now.
	Now func() time.Time
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, "bench: "+format+"\n", args...)
	}
}

// CheckBudget accepts a finite, non-negative budget.
func CheckBudget(b float64) error {
	if math.IsNaN(b) || math.IsInf(b, 0) || b < 0 {
		return fmt.Errorf("budget must be a finite, non-negative number of dollars, got %v", b)
	}
	return nil
}

type plan struct {
	info     harness.Info
	enforced bool // the harness caps each execution at MaxBudgetUSD
	repos    []string
	per      []contract.CostEstimate // one audit of each fixture
	total    contract.CostEstimate   // every fixture × trial
}

// prepare probes the harness (free), materializes each fixture, and
// estimates one audit of it the way audit.Run will.
func prepare(ctx context.Context, opts Options, trials int) (plan, error) {
	if opts.Repo == nil {
		return plan{}, errors.New("bench: Options.Repo is required")
	}
	info, err := opts.Harness.Probe(ctx)
	if err != nil {
		return plan{}, fmt.Errorf("harness %s is not usable: %w", opts.Config.Harness.Name, err)
	}
	p := plan{info: info, enforced: slices.Contains(info.Capabilities, harness.CapBudgetLimit)}
	for _, f := range opts.Fixtures {
		dir, err := opts.Repo(f.Files)
		if err != nil {
			return plan{}, fmt.Errorf("fixture %s: %w", f.Name, err)
		}
		st, err := source.Inspect(ctx, dir)
		if err != nil {
			return plan{}, fmt.Errorf("fixture %s: %w", f.Name, err)
		}
		files, err := source.CountFiles(ctx, st, opts.Config.Audit.DirtyPolicy)
		if err != nil {
			return plan{}, fmt.Errorf("fixture %s: %w", f.Name, err)
		}
		p.repos = append(p.repos, dir)
		p.per = append(p.per, cost.ForAudit(opts.Config, files, p.enforced))
	}
	one := cost.Total(p.per...)
	p.total = one
	if trials > 1 {
		p.total = cost.Total(slices.Repeat([]contract.CostEstimate{one}, trials)...)
	}
	return p, nil
}

// nextAllowed is the per-audit spend guard: the next audit starts only if
// its high estimate fits in what is left of the budget.
func nextAllowed(spent, nextHigh, budget float64) bool {
	return spent < budget && spent+nextHigh <= budget+1e-9
}

// refuse is every audit's preflight: an audit over the budget left is
// never approved.
func refuse(context.Context, contract.CostEstimate) (contract.CostApproval, error) {
	return "", errors.New("the audit's estimate exceeds the benchmark budget left; the benchmark never approves an over-budget audit")
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// guard wraps the harness for every audit of a benchmark. When the harness
// enforces MaxBudgetUSD, the budget is a hard cap: an execution starts only
// if what was charged, plus the caps of executions in flight, plus its own
// cap fit the budget; its cap is reserved while it runs; and it is charged
// its reported cost, or its whole cap when it reports none (a timeout, an
// error, a missing result). Otherwise a cap is no ceiling, so only reported
// cost is charged and the estimate is the guard. Every retry attempt is a
// separate Run, so retries are covered too.
type guard struct {
	h        harness.Harness
	budget   float64
	enforced bool

	mu                        sync.Mutex
	charged, reserved         float64
	reported                  float64
	executions, reportedExecs int
	capped, refused           int
}

func (g *guard) Probe(ctx context.Context) (harness.Info, error) { return g.h.Probe(ctx) }

func (g *guard) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	limit := 0.0
	if g.enforced {
		limit = req.MaxBudgetUSD
	}
	g.mu.Lock()
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return harness.Result{}, &harness.Error{Kind: harness.ErrCanceled, Msg: "canceled before the execution started", Err: err}
	}
	if (g.enforced && limit <= 0) || g.charged+g.reserved+limit > g.budget+1e-9 {
		g.refused++
		msg := fmt.Sprintf("benchmark budget guard: $%.2f charged + $%.2f reserved + this execution's $%.2f cap exceed the $%.2f budget; not started", g.charged, g.reserved, limit, g.budget)
		g.mu.Unlock()
		return harness.Result{}, &harness.Error{Kind: harness.ErrBudget, Msg: msg}
	}
	g.reserved = round6(g.reserved + limit)
	g.mu.Unlock()

	var meta harness.Meta // stays empty if the harness panics: charged at cap
	defer func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.reserved = round6(g.reserved - limit)
		g.executions++
		switch {
		case meta.CostUSD > 0:
			g.reportedExecs++
			g.reported, g.charged = round6(g.reported+meta.CostUSD), round6(g.charged+meta.CostUSD)
		case g.enforced:
			g.capped++
			g.charged = round6(g.charged + limit)
		}
	}()
	res, err := g.h.Run(ctx, req)
	if meta = res.Meta; err != nil {
		meta = harness.MetaOf(err)
	}
	return res, err
}

// spent returns what was charged and how many executions started.
func (g *guard) spent() (float64, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.charged, g.executions
}

// reserve is the most the guard can hold for executions in flight: every
// parallel agent at the largest per-execution cap.
func reserve(cfg config.Config) float64 {
	maxCap := 0.0
	for _, name := range append([]string{"discovery", "pruv"}, cfg.Audit.Reviewers...) {
		_, _, c := cfg.Limits(name)
		maxCap = max(maxCap, c)
	}
	return float64(cfg.Audit.MaxParallelAgents) * maxCap
}

// RecommendedBudget is the budget to give a run that must complete, such
// as a baseline run: the larger of the high estimate plus the guard's
// in-flight reserve and 1.5 × the high estimate (a margin for retries and
// estimate error), rounded up to a whole dollar.
func RecommendedBudget(high float64, cfg config.Config) float64 {
	return math.Ceil(round6(max(high+reserve(cfg), 1.5*high)) - 1e-9)
}

// Run runs the benchmark: an audit of every fixture for each trial, then
// the scores. It logs the estimate first. Without a budget it returns
// results holding only the estimate, with ErrNoBudget; over the budget it
// refuses. Either way no agent runs. Audits run through a guard shared by
// all of them (see guard), and before each audit the spend so far plus its
// high estimate must fit the budget; once that fails, or ctx is done, the
// remaining runs are recorded as skipped or interrupted (incomplete). A
// failed, partial or interrupted audit is incomplete too, so every fixture
// has a run for every trial.
func Run(ctx context.Context, opts Options) (*Results, error) {
	if err := CheckBudget(opts.Budget); err != nil {
		return nil, err
	}
	trials := max(opts.Trials, 1)
	if trials > MaxTrials {
		return nil, fmt.Errorf("%d trials: at most %d are allowed", trials, MaxTrials)
	}
	if opts.BudgetKey == "" {
		opts.BudgetKey = "budget"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	p, err := prepare(ctx, opts, trials)
	if err != nil {
		return nil, err
	}
	for _, line := range cost.LinesFor(p.total, opts.Budget, opts.BudgetKey) {
		opts.logf("%s", line)
	}
	cfg := opts.Config
	cfg.Storage.RunsDir = opts.RunsDir
	res, err := newResults(opts, cfg, p, trials)
	if err != nil {
		return nil, err
	}
	if opts.Budget == 0 {
		return res, ErrNoBudget
	}
	if cost.OverBudget(p.total, opts.Budget) {
		return res, fmt.Errorf("benchmark estimate up to $%.2f exceeds %s $%.2f; nothing was spent", p.total.HighUSD, opts.BudgetKey, opts.Budget)
	}
	warn := func(msg string) {
		res.Warnings = append(res.Warnings, msg)
		opts.logf("warning: %s", msg)
	}
	g := &guard{h: opts.Harness, budget: opts.Budget, enforced: p.enforced}
	if !p.enforced {
		warn(fmt.Sprintf("harness %s does not enforce a per-execution budget; only the estimate guards spend", p.info.Name))
	} else if need := p.total.HighUSD + reserve(cfg); opts.Budget < need-1e-9 {
		warn(fmt.Sprintf("%s $%.2f is below the high estimate plus the in-flight reserve ($%.2f + $%.2f); executions near the end will be refused, so the run is likely to end incomplete (recommended: $%.0f)",
			opts.BudgetKey, opts.Budget, p.total.HighUSD, reserve(cfg), RecommendedBudget(p.total.HighUSD, cfg)))
	}
	stopped := false
	// Trial-major, so a stop leaves every fixture's earlier trials done.
	for trial := 1; trial <= trials; trial++ {
		for i, f := range opts.Fixtures {
			run := RunResult{Trial: trial, Status: StatusSkipped, Observations: []Observation{}}
			spent, started := g.spent()
			switch {
			case ctx.Err() != nil:
				if run.Status = StatusInterrupted; !stopped {
					warn("interrupted: the remaining runs were not started")
				}
				stopped = true
			case stopped:
			case !nextAllowed(spent, p.per[i].HighUSD, opts.Budget):
				stopped = true
				warn(fmt.Sprintf("spend guard: $%.2f spent + $%.2f (next audit's high estimate) exceeds %s $%.2f; the remaining runs were not started", spent, p.per[i].HighUSD, opts.BudgetKey, opts.Budget))
			default:
				opts.logf("fixture %s, trial %d of %d", f.Name, trial, trials)
				cfg.Cost.BudgetUSD = round6(opts.Budget - spent)
				run = auditOnce(ctx, auditOptions(opts, cfg, g, p.repos[i]), trial)
				charged, now := g.spent()
				run.ChargedUSD, run.Executions = round6(charged-spent), now-started
			}
			res.Fixtures[i].Runs = append(res.Fixtures[i].Runs, run)
		}
	}
	c := &res.Cost
	c.ActualUSD, c.ChargedUSD, c.Executions = g.reported, g.charged, g.executions
	c.ReportedExecutions, c.CappedExecutions, c.RefusedExecutions = g.reportedExecs, g.capped, g.refused
	if g.capped > 0 {
		warn(fmt.Sprintf("%d execution(s) reported no cost and were each charged at its cap", g.capped))
	}
	if g.refused > 0 {
		warn(fmt.Sprintf("%d execution(s) were refused by the budget guard; their runs are incomplete", g.refused))
	}
	if !p.enforced && g.reportedExecs < g.executions {
		warn(fmt.Sprintf("harness %s reported no cost for %d of %d executions; only the estimate guards spend", p.info.Name, g.executions-g.reportedExecs, g.executions))
	}
	if _, err := res.Rescore(opts.Fixtures); err != nil {
		return res, err
	}
	return res, nil
}

func newResults(opts Options, cfg config.Config, p plan, trials int) (*Results, error) {
	names := append(append([]string{"discovery"}, cfg.Audit.Reviewers...), "pruv")
	defs, err := agent.Load(cfg.Agents.Dir, names...)
	if err != nil {
		return nil, err
	}
	res := &Results{
		BenchVersion: ResultsVersion, CreatedAt: opts.Now().UTC(),
		Harness:  HarnessInfo{Name: p.info.Name, Version: p.info.Version, Model: cfg.Harness.Model},
		Agents:   map[string]AgentInfo{},
		Config:   ConfigInfo{Reviewers: cfg.Audit.Reviewers, MaxFindingsPerReviewer: cfg.Audit.MaxFindingsPerReviewer},
		Matching: currentMatching(),
		Trials:   trials,
		Cost:     CostInfo{Estimate: p.total, BudgetUSD: opts.Budget},
	}
	for name, d := range defs {
		res.Agents[name] = AgentInfo{Version: d.Version, SHA256: d.SHA256}
	}
	for _, f := range opts.Fixtures {
		res.Fixtures = append(res.Fixtures, FixtureResult{Name: f.Name, SourceDigest: f.SourceDigest, KeyDigest: f.KeyDigest, Runs: []RunResult{}})
	}
	return res, nil
}

// auditOptions configures one audit: through the guard, and with a
// preflight that refuses, so an audit over the budget left never runs.
func auditOptions(opts Options, cfg config.Config, h harness.Harness, repo string) audit.Options {
	return audit.Options{
		RepoPath: repo, Config: cfg, Harness: h, Store: storage.Store{Root: cfg.Storage.RunsDir},
		SiftVersion: "bench", Log: opts.Log, Now: opts.Now, Preflight: refuse,
	}
}

// auditOnce runs one audit and reads its findings back from the run's
// artifacts.
func auditOnce(ctx context.Context, ao audit.Options, trial int) RunResult {
	run := RunResult{Trial: trial, RunsDir: ao.Store.Root, Status: string(contract.RunFailed), Observations: []Observation{}}
	r, err := audit.Run(ctx, ao)
	if err != nil {
		run.Error = err.Error()
	}
	if r == nil || r.Run == nil {
		return run
	}
	m := r.Manifest
	run.RunID, run.Status = r.Run.ID, string(m.Status)
	if m.Cost != nil {
		run.Approval, run.ActualUSD = m.Cost.Approval, m.Cost.Actual.USD
	}
	data, lerr := report.Load(r.Run)
	if lerr != nil {
		run.Error = errors.Join(err, lerr).Error()
		return run
	}
	run.Observations = FromReport(data)
	run.Complete = err == nil && m.Status == contract.RunCompleted
	return run
}
