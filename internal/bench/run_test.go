package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/harness"
	"github.com/olddognewflex/sift/internal/harness/fake"
	"github.com/olddognewflex/sift/internal/storage"
	"github.com/olddognewflex/sift/internal/testutil"
)

const counterGo = "counter/counter.go"

func counterDraft(title, excerpt string, start, end int) contract.FindingDraft {
	loc := contract.Location{Path: counterGo, StartLine: start, EndLine: end}
	return contract.FindingDraft{
		Title: title, Claim: title + " claim", TriggeringConditions: "any input", ExpectedImpact: "wrong result",
		Severity: contract.SeverityHigh, Confidence: contract.ConfidenceHigh,
		Remediation: "fix it", VerificationCriteria: "a test passes",
		Locations: []contract.Location{loc},
		Evidence: []contract.Evidence{{Kind: contract.EvidenceSource, Description: "the code",
			Path: counterGo, StartLine: start, EndLine: start, Excerpt: excerpt}},
	}
}

func review(fs ...contract.FindingDraft) contract.ReviewReport {
	return contract.ReviewReport{Findings: fs, Strengths: []contract.Strength{}, ReviewedScope: []string{"counter"},
		UnreviewedScope: []string{}, Limitations: []string{}}
}

func verdict(v contract.Verdict, c contract.Classification) contract.ValidationDecision {
	return contract.ValidationDecision{
		Verdict: v, Classification: c, EvidenceAssessment: "checked", ContradictoryEvidence: "searched; none",
		TriggerAssessment: "reachable", ImpactAssessment: "real", RemediationAssessment: "proportionate", Rationale: "because",
	}
}

// selftestHarness scripts the selftest fixture (S-D1 defect at lines
// 28-30, S-X1 decoy at line 53):
//
//	ARCH-001 on the decoy, fabricated excerpt, Pruv accepts → gate demotes
//	ARCH-002 unkeyed (New), Pruv accepts as a risk → supported_risk
//	CORR-001 on the defect, exact excerpt, Pruv accepts → confirmed
//	CORR-002 on the decoy, Pruv rejects → rejected
func selftestHarness() *fake.Harness {
	decisions := map[string]contract.ValidationDecision{
		"ARCH-001": verdict(contract.VerdictAccept, contract.ClassDefect),
		"ARCH-002": verdict(contract.VerdictAccept, contract.ClassRisk),
		"CORR-001": verdict(contract.VerdictAccept, contract.ClassDefect),
		"CORR-002": verdict(contract.VerdictReject, ""),
	}
	return &fake.Harness{Responders: map[string]fake.Responder{
		"discovery": fake.JSON(testutil.Discovery()),
		"architecture": fake.JSON(review(
			counterDraft("Top returns one key too many", "return keys[:n+1]", 53, 53),
			counterDraft("New allocates without a capacity hint", "func New() *Counter {", 16, 18),
		)),
		"correctness": fake.JSON(review(
			counterDraft("Get reads the map without the mutex", "return c.counts[key]", 29, 29),
			counterDraft("Top slices past the end of keys", "return keys[:n]", 53, 53),
		)),
		"pruv": func(req harness.Request, _ int) (json.RawMessage, error) {
			d, ok := decisions[testutil.FindingID(req)]
			if !ok {
				return nil, &harness.Error{Kind: harness.ErrProcess, Msg: "unexpected finding " + testutil.FindingID(req)}
			}
			return json.Marshal(d)
		},
	}}
}

func selftestOptions(t *testing.T, h harness.Harness, budget float64, trials int) Options {
	t.Helper()
	fixtures, err := LoadFixtures("testdata", []string{"selftest"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Audit.MaxFindingsPerReviewer = 2
	return Options{
		Fixtures: fixtures, Config: cfg, Harness: h, Budget: budget, BudgetKey: "SIFT_BENCH_BUDGET_USD",
		Trials: trials, RunsDir: filepath.Join(t.TempDir(), "runs"),
		Repo: func(files map[string]string) (string, error) { return testutil.GitRepo(t, files), nil },
	}
}

func fmtRate(r *float64) any {
	if r == nil {
		return "null"
	}
	return *r
}

// One audit of selftest: 2 files, 2 reviewers × 2 findings → high $1.78
// at the default rates (0.50 + 2×2×0.02 + 4×0.30).
func TestRunScoresSelftest(t *testing.T) {
	h := selftestHarness()
	opts := selftestOptions(t, h, 2, 1)
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fixtures) != 1 || len(res.Fixtures[0].Runs) != 1 {
		t.Fatalf("fixtures %+v", res.Fixtures)
	}
	run := res.Fixtures[0].Runs[0]
	if !run.Complete || run.Status != string(contract.RunCompleted) || run.RunID == "" || run.Error != "" {
		t.Fatalf("run %+v", run)
	}
	if run.Approval != contract.ApprovalWithinBudget {
		t.Errorf("approval = %s, want within_budget", run.Approval)
	}
	// discovery + 2 reviewers + 4 Pruv calls at $0.01 each.
	if run.Executions != 7 || math.Abs(run.ActualUSD-0.07) > 1e-9 || res.Cost.Executions != 7 || math.Abs(res.Cost.ActualUSD-0.07) > 1e-9 {
		t.Errorf("spend: run %d/$%v, total %d/$%v", run.Executions, run.ActualUSD, res.Cost.Executions, res.Cost.ActualUSD)
	}
	if res.Cost.Estimate.HighUSD != 1.78 || res.Cost.BudgetUSD != 2 || res.Cost.ChargedUSD != 0.07 || res.Cost.ReportedExecutions != 7 || run.ChargedUSD != 0.07 {
		t.Errorf("cost %+v, run charged %v", res.Cost, run.ChargedUSD)
	}
	// The fake enforces no per-execution cap, so only the estimate guards spend.
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "only the estimate guards spend") {
		t.Errorf("warnings %q", res.Warnings)
	}
	states := map[string]contract.ValidationState{}
	for _, o := range run.Observations {
		states[o.ID] = o.State
	}
	want := map[string]contract.ValidationState{
		"ARCH-001": contract.StateNeedsInvestigation, "ARCH-002": contract.StateSupportedRisk,
		"CORR-001": contract.StateConfirmed, "CORR-002": contract.StateRejected,
	}
	for id, s := range want {
		if states[id] != s {
			t.Errorf("%s = %s, want %s", id, states[id], s)
		}
	}
	if res.Harness.Name != "fake" || res.Harness.Version != "1.0.0" || len(res.Agents) != 4 || res.Agents["pruv"].SHA256 == "" {
		t.Errorf("harness %+v agents %+v", res.Harness, res.Agents)
	}
	if res.Matching != (MatchingInfo{"match/v1", 3, 60}) || res.Trials != 1 || res.BenchVersion != 1 {
		t.Errorf("matching %+v trials %d", res.Matching, res.Trials)
	}

	a := res.Scores.Aggregate
	if a.Runs != 1 || a.IncompleteRuns != 0 {
		t.Errorf("runs %d incomplete %d", a.Runs, a.IncompleteRuns)
	}
	ca, cc, co := a.Candidate.Reviewers["architecture"], a.Candidate.Reviewers["correctness"], a.Candidate.Overall
	if ca.Findings != 2 || ca.DecoysFlagged != 1 || ca.Unkeyed != 1 || cc.TP != 1 || cc.DecoysFlagged != 1 || co.Duplicates != 1 {
		t.Errorf("candidate counts: arch %+v corr %+v overall %+v", *ca, *cc, co)
	}
	wantRate(t, "candidate.architecture.precision", ca.Precision, 0)
	wantNil(t, "candidate.architecture.recall", ca.Recall)
	wantRate(t, "candidate.correctness.precision", cc.Precision, 0.5)
	wantRate(t, "candidate.correctness.recall", cc.Recall, 1)
	wantRate(t, "candidate.overall.precision", co.Precision, 1.0/3)
	wantRate(t, "candidate.overall.recall", co.Recall, 1)
	fa, fc := a.Final.Reviewers["architecture"], a.Final.Reviewers["correctness"]
	wantRate(t, "final.architecture.precision", fa.Precision, 0)
	wantRate(t, "final.correctness.precision", fc.Precision, 1)
	wantRate(t, "final.correctness.recall", fc.Recall, 1)
	p := a.Pruv
	wantRate(t, "pruv.precision", p.Precision, 0.5)
	wantRate(t, "pruv.recall", p.Recall, 1)
	wantRate(t, "pruv.retention", p.Retention, 1)
	wantRate(t, "pruv.decoy_rejection_rate", p.DecoyRejectionRate, 1)
	if p.DecoysFlagged != 1 || p.DecoysRejected != 1 || p.DecoysRejectedBy != (Loss{Gate: 1}) || p.Unvalidated != 0 {
		t.Errorf("pruv %+v", p)
	}

	// Results round-trip through the strict reader unchanged.
	path := filepath.Join(t.TempDir(), "results.json")
	if err := WriteResults(path, res); err != nil {
		t.Fatal(err)
	}
	back, err := ReadResults(path)
	if err != nil {
		t.Fatal(err)
	}
	j1, _ := json.Marshal(res)
	j2, _ := json.Marshal(back)
	if !bytes.Equal(j1, j2) {
		t.Errorf("round trip changed the results:\n%s\n%s", j1, j2)
	}
}

func TestEstimateOnlyWithoutBudget(t *testing.T) {
	h := selftestHarness()
	opts := selftestOptions(t, h, 0, 2)
	var log bytes.Buffer
	opts.Log = &log
	res, err := Run(context.Background(), opts)
	if !errors.Is(err, ErrNoBudget) || res == nil {
		t.Fatalf("Run without a budget: %v, %v", res, err)
	}
	if e := res.Cost.Estimate; e.HighUSD != 3.56 || e.Files != 4 || e.Kind != "total" || len(res.Fixtures[0].Runs) != 0 {
		t.Errorf("estimate %+v runs %+v", e, res.Fixtures)
	}
	if !strings.Contains(log.String(), "no run budget (SIFT_BENCH_BUDGET_USD is 0)") {
		t.Errorf("log lacks the labelled estimate:\n%s", log.String())
	}
	if n := len(h.Calls()); n != 0 {
		t.Errorf("%d harness runs; want 0", n)
	}
}

func TestCheckBudget(t *testing.T) {
	for _, b := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1} {
		if CheckBudget(b) == nil {
			t.Errorf("CheckBudget(%v) accepted it", b)
		}
		h := selftestHarness()
		if res, err := Run(context.Background(), selftestOptions(t, h, b, 1)); err == nil || res != nil || len(h.Calls()) != 0 {
			t.Errorf("Run with budget %v: %v, %v, %d calls", b, res, err, len(h.Calls()))
		}
	}
	for _, b := range []float64{0, 5} {
		if err := CheckBudget(b); err != nil {
			t.Errorf("CheckBudget(%v): %v", b, err)
		}
	}
}

func TestRunRejectsTooManyTrials(t *testing.T) {
	h := selftestHarness()
	if _, err := Run(context.Background(), selftestOptions(t, h, 100, MaxTrials+1)); err == nil || len(h.Calls()) != 0 {
		t.Errorf("err %v, %d calls", err, len(h.Calls()))
	}
}

func TestRunRefusesOverBudget(t *testing.T) {
	h := selftestHarness()
	opts := selftestOptions(t, h, 1.77, 1)
	_, err := Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "exceeds SIFT_BENCH_BUDGET_USD $1.77") || !strings.Contains(err.Error(), "nothing was spent") {
		t.Errorf("err = %v", err)
	}
	if n := len(h.Calls()); n != 0 {
		t.Errorf("%d harness runs; want 0", n)
	}
}

// The total estimate fits the budget, but the first audit's actual spend
// ($0.07 at the fake's $0.01 per execution) exceeds its $0.05 high
// estimate, so the second trial no longer fits.
func TestRunSpendGuardStopsBeforeNextRun(t *testing.T) {
	h := selftestHarness()
	opts := selftestOptions(t, h, 0.10, 2)
	opts.Config.Cost.Rates = config.CostRates{DiscoveryUSD: 0.01, FileReviewUSD: 0.005, PruvCallUSD: 0.005}
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cost.Estimate.HighUSD != 0.10 {
		t.Fatalf("estimate %+v", res.Cost.Estimate)
	}
	runs := res.Fixtures[0].Runs
	if len(runs) != 2 || !runs[0].Complete || runs[1].Complete || runs[1].Status != StatusSkipped || runs[1].Trial != 2 || runs[1].RunID != "" {
		t.Fatalf("runs %+v", runs)
	}
	if n := len(h.Calls()); n != 7 {
		t.Errorf("%d harness runs; want 7 (one audit)", n)
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "spend guard") }) {
		t.Errorf("warnings %q", res.Warnings)
	}
	if a := res.Scores.Aggregate; a.Runs != 2 || a.IncompleteRuns != 1 || a.Candidate.Overall.Defects != 2 || res.Scores.Trials != 2 {
		t.Errorf("scores: runs %d incomplete %d defects %d trials %d", a.Runs, a.IncompleteRuns, a.Candidate.Overall.Defects, res.Scores.Trials)
	}
	if res.Incomplete() != 1 {
		t.Errorf("Incomplete() = %d", res.Incomplete())
	}
}

// Each audit's own preflight checks against the budget that remains.
func TestRunGivesEachAuditTheRemainingBudget(t *testing.T) {
	opts := selftestOptions(t, selftestHarness(), 5, 2)
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float64{5, 4.93} {
		run, err := storage.Store{Root: opts.RunsDir}.Open(res.Fixtures[0].Runs[i].RunID)
		if err != nil {
			t.Fatal(err)
		}
		m, err := run.Manifest()
		if err != nil {
			t.Fatal(err)
		}
		if m.Cost.BudgetUSD != want || m.Cost.Approval != contract.ApprovalWithinBudget {
			t.Errorf("trial %d: budget %v approval %s, want %v within_budget", i+1, m.Cost.BudgetUSD, m.Cost.Approval, want)
		}
	}
}

func TestAuditOptionsRefuseOverBudget(t *testing.T) {
	ao := auditOptions(Options{}, config.Default(), selftestHarness(), "/repo")
	if ao.Preflight == nil {
		t.Fatal("no Preflight: an over-budget audit would run unchecked")
	}
	if approval, err := ao.Preflight(context.Background(), contract.CostEstimate{HighUSD: 1}); err == nil || approval != "" {
		t.Errorf("Preflight = %q, %v", approval, err)
	}
}

// enforcing reports the budget-limit capability, so the guard treats each
// execution's MaxBudgetUSD as a ceiling. noCost drops the reported cost of
// the named agents.
type enforcing struct {
	harness.Harness
	noCost map[string]bool
}

func (e enforcing) Probe(ctx context.Context) (harness.Info, error) {
	info, err := e.Harness.Probe(ctx)
	info.Capabilities = append(info.Capabilities, harness.CapBudgetLimit)
	return info, err
}

func (e enforcing) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	res, err := e.Harness.Run(ctx, req)
	if e.noCost[req.Agent] {
		res.Meta.CostUSD = 0
	}
	return res, err
}

// The overspend case from review: estimate $0.05, budget $0.06, while the
// audit would spend $0.07. With per-execution caps of $0.01 reserved before
// each execution, the guard stops at the budget and the run is incomplete.
func TestRunHardCapStopsAtBudget(t *testing.T) {
	h := selftestHarness()
	opts := selftestOptions(t, enforcing{Harness: h}, 0.06, 1)
	opts.Config.Cost.Rates = config.CostRates{DiscoveryUSD: 0.01, FileReviewUSD: 0.005, PruvCallUSD: 0.005}
	opts.Config.Agents.MaxBudgetUSD = 0.01
	var log bytes.Buffer
	opts.Log = &log
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	// $0.06 < $0.05 high + 2 parallel × $0.01 cap: warned before any audit.
	warnAt, auditAt := strings.Index(log.String(), "likely to end incomplete"), strings.Index(log.String(), "bench: fixture selftest")
	if warnAt < 0 || auditAt < 0 || warnAt > auditAt {
		t.Errorf("no up-front headroom warning before the first audit:\n%s", log.String())
	}
	c := res.Cost
	if c.Estimate.HighUSD != 0.05 || c.ChargedUSD > 0.06+1e-9 || c.ActualUSD > 0.06+1e-9 || c.RefusedExecutions < 1 || len(h.Calls()) > 6 {
		t.Errorf("cost %+v, %d calls", c, len(h.Calls()))
	}
	if run := res.Fixtures[0].Runs[0]; run.Complete || run.Status != string(contract.RunPartial) || run.Executions != c.Executions {
		t.Errorf("run %+v, %d executions started", run, c.Executions)
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "refused by the budget guard") }) {
		t.Errorf("warnings %q", res.Warnings)
	}
}

func TestRunChargesUnreportedCostAtCap(t *testing.T) {
	h := selftestHarness()
	opts := selftestOptions(t, enforcing{Harness: h, noCost: map[string]bool{"discovery": true}}, 2, 1)
	opts.Config.Agents.MaxBudgetUSD = 0.25
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	// 6 reported executions at $0.01, discovery charged at its $0.25 cap.
	if c := res.Cost; c.ActualUSD != 0.06 || c.ChargedUSD != 0.31 || c.CappedExecutions != 1 || c.ReportedExecutions != 6 || c.Executions != 7 {
		t.Errorf("cost %+v", c)
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "charged at its cap") }) ||
		slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "likely to end incomplete") }) {
		t.Errorf("warnings %q", res.Warnings) // $2 covers $1.33 high + 2 × $0.25
	}
}

// stub is a harness whose executions block until released.
type stub struct {
	mu            sync.Mutex
	inFlight, max int
	release       chan struct{}
	meta          harness.Meta
	err           error
}

func (s *stub) Probe(context.Context) (harness.Info, error) { return harness.Info{}, nil }

func (s *stub) Run(context.Context, harness.Request) (harness.Result, error) {
	s.mu.Lock()
	s.inFlight++
	s.max = max(s.max, s.inFlight)
	s.mu.Unlock()
	if s.release != nil {
		<-s.release
	}
	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
	if s.err != nil {
		return harness.Result{}, &harness.Error{Kind: harness.ErrTimeout, Msg: "timed out", Meta: s.meta}
	}
	return harness.Result{Meta: s.meta}, nil
}

func TestGuardChargesUnreportedAtCap(t *testing.T) {
	ctx := context.Background()
	for name, inner := range map[string]*stub{
		"success without a cost": {},
		"timeout without a cost": {err: errors.New("timeout")},
	} {
		g := &guard{h: inner, budget: 1, enforced: true}
		g.Run(ctx, harness.Request{MaxBudgetUSD: 0.4})
		if g.charged != 0.4 || g.capped != 1 || g.reported != 0 || g.reserved != 0 {
			t.Errorf("%s: %+v", name, g)
		}
	}
	g := &guard{h: &stub{meta: harness.Meta{CostUSD: 0.15}}, budget: 1, enforced: true}
	g.Run(ctx, harness.Request{MaxBudgetUSD: 0.4})
	if g.charged != 0.15 || g.reported != 0.15 || g.reportedExecs != 1 {
		t.Errorf("reported: %+v", g)
	}
	// Without enforcement a cap is no ceiling, so nothing is charged for it.
	g = &guard{h: &stub{}, budget: 1}
	g.Run(ctx, harness.Request{MaxBudgetUSD: 0.4})
	if g.charged != 0 || g.executions != 1 {
		t.Errorf("unenforced: %+v", g)
	}
}

// Ten parallel executions with $0.30 caps against a $1 budget: only three
// fit at once, and once they are charged nothing more starts.
func TestGuardConcurrentReservationsStayWithinBudget(t *testing.T) {
	inner := &stub{release: make(chan struct{}), meta: harness.Meta{CostUSD: 0.30}}
	g := &guard{h: inner, budget: 1, enforced: true}
	errs := make(chan error, 10)
	for range 10 {
		go func() {
			_, err := g.Run(context.Background(), harness.Request{MaxBudgetUSD: 0.30})
			errs <- err
		}()
	}
	var once sync.Once
	release := func() { once.Do(func() { close(inner.release) }) }
	defer release() // unblock admitted executions on failure
	for range 7 {
		select {
		case err := <-errs:
			if harness.KindOf(err) != harness.ErrBudget {
				t.Fatalf("want a budget refusal, got %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("fewer than 7 executions were refused: more than the budget's worth are in flight")
		}
	}
	release()
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if inner.max != 3 || g.executions != 3 || g.refused != 7 || math.Abs(g.charged-0.9) > 1e-9 {
		t.Errorf("max in flight %d, guard %+v", inner.max, g)
	}
	if _, err := g.Run(context.Background(), harness.Request{MaxBudgetUSD: 0.30}); harness.KindOf(err) != harness.ErrBudget {
		t.Errorf("after $0.90 charged, another $0.30 execution: %v", err)
	}
}

// Canceling mid-audit stops it, starts nothing more, and still returns
// results with every run incomplete.
func TestRunCancelMidRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := selftestHarness()
	callsAtCancel := -1
	h.Before = func(_ context.Context, req harness.Request) error {
		if req.Agent == "pruv" && callsAtCancel < 0 {
			callsAtCancel = len(h.Calls())
			cancel()
		}
		return nil
	}
	opts := selftestOptions(t, h, 10, 2)
	opts.Config.Audit.MaxParallelAgents = 1
	res, err := Run(ctx, opts)
	if err != nil || res == nil {
		t.Fatalf("%v, %v", res, err)
	}
	runs := res.Fixtures[0].Runs
	if len(runs) != 2 || runs[0].Complete || runs[0].Status != string(contract.RunInterrupted) || runs[0].RunID == "" ||
		runs[1].Complete || runs[1].Status != StatusInterrupted || runs[1].RunID != "" {
		t.Errorf("runs %+v", runs)
	}
	if n := len(h.Calls()); n != callsAtCancel {
		t.Errorf("%d harness runs, %d at cancel: work started after the cancel", n, callsAtCancel)
	}
	if res.Scores.Aggregate.IncompleteRuns != 2 || !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "interrupted") }) {
		t.Errorf("scores %+v warnings %q", res.Scores.Aggregate, res.Warnings)
	}
	if err := WriteResults(filepath.Join(t.TempDir(), "r.json"), res); err != nil {
		t.Fatal(err)
	}
}

func TestNextAllowed(t *testing.T) {
	for _, tc := range []struct {
		spent, next, budget float64
		want                bool
	}{
		{0, 5, 5, true},
		{0.07, 0.03, 0.10, true}, // exactly fits, despite float noise
		{0.07, 0.05, 0.10, false},
		{0, 5.01, 5, false},
		{5, 0, 5, false}, // nothing left
	} {
		if got := nextAllowed(tc.spent, tc.next, tc.budget); got != tc.want {
			t.Errorf("nextAllowed(%v, %v, %v) = %v", tc.spent, tc.next, tc.budget, got)
		}
	}
}

func TestGuardStartsNothingAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inner := &stub{}
	g := &guard{h: inner, budget: 1, enforced: true}
	if _, err := g.Run(ctx, harness.Request{MaxBudgetUSD: 0.1}); harness.KindOf(err) != harness.ErrCanceled || inner.max != 0 || g.executions != 0 {
		t.Errorf("err %v, inner ran %d, guard %+v", err, inner.max, g)
	}
}

type panicker struct{}

func (panicker) Probe(context.Context) (harness.Info, error) { return harness.Info{}, nil }
func (panicker) Run(context.Context, harness.Request) (harness.Result, error) {
	panic("harness bug")
}

// A panic in the wrapped harness releases the reservation and charges the
// cap, so the guard never leaks a reservation and errs toward spending less.
func TestGuardReleasesReservationOnPanic(t *testing.T) {
	g := &guard{h: panicker{}, budget: 1, enforced: true}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not propagate")
			}
		}()
		g.Run(context.Background(), harness.Request{MaxBudgetUSD: 0.4})
	}()
	if g.reserved != 0 || g.charged != 0.4 || g.capped != 1 || g.executions != 1 {
		t.Errorf("guard %+v", g)
	}
}

func TestRecommendedBudget(t *testing.T) {
	cfg := config.Default() // 2 parallel agents × $3 cap
	for _, tc := range []struct{ high, want float64 }{
		{17.14, 26}, // 1.5 × 17.14 = 25.71 beats 17.14 + 6
		{4, 10},     // 4 + 6 beats 1.5 × 4
		{12, 18},    // equal: exactly 18, not rounded up past it
	} {
		if got := RecommendedBudget(tc.high, cfg); got != tc.want {
			t.Errorf("RecommendedBudget(%v) = %v, want %v", tc.high, got, tc.want)
		}
	}
	cfg.Agents.Overrides = map[string]config.AgentOverride{"pruv": {MaxBudgetUSD: 5}}
	if got := RecommendedBudget(4, cfg); got != 14 { // the largest cap counts: 4 + 2 × 5
		t.Errorf("with a $5 pruv cap: %v, want 14", got)
	}
}
