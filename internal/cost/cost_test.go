package cost

import (
	"math"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/contract"
)

// testConfig has the default rates (0.50/0.02/0.30), 4 expected and 8 max
// findings per reviewer, and both reviewers.
func testConfig() config.Config {
	cfg := config.Default()
	cfg.Audit.Reviewers = []string{"architecture", "correctness"}
	cfg.Audit.MaxFindingsPerReviewer = 8
	cfg.Cost.ExpectedFindingsPerReviewer = 4
	cfg.Cost.Rates = config.CostRates{DiscoveryUSD: 0.50, FileReviewUSD: 0.02, PruvCallUSD: 0.30}
	return cfg
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestForAudit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*config.Config)
		files    int
		enforced bool
		// want
		low, high, discovery, reviewers, pruvLow, pruvHigh float64
		callsLow, callsHigh                                int
	}{
		{
			// 0.50 + 2×100×0.02 + {8,16}×0.30
			name: "unenforced", files: 100,
			low: 6.90, high: 9.30, discovery: 0.50, reviewers: 4.00, pruvLow: 2.40, pruvHigh: 4.80, callsLow: 8, callsHigh: 16,
		},
		{
			// Unenforced caps are ignored: a $1 cap the harness can't apply
			// changes nothing.
			name: "unenforced ignores cap", files: 100,
			mutate: func(c *config.Config) { c.Agents.MaxBudgetUSD = 1 },
			low:    6.90, high: 9.30, discovery: 0.50, reviewers: 4.00, pruvLow: 2.40, pruvHigh: 4.80, callsLow: 8, callsHigh: 16,
		},
		{
			// Each reviewer execution would cost 2.00 but stops at its $1 cap.
			name: "enforced per-execution cap", files: 100, enforced: true,
			mutate: func(c *config.Config) { c.Agents.MaxBudgetUSD = 1 },
			low:    4.90, high: 7.30, discovery: 0.50, reviewers: 2.00, pruvLow: 2.40, pruvHigh: 4.80, callsLow: 8, callsHigh: 16,
		},
		{
			// A per-agent override caps only that agent's executions.
			name: "enforced pruv override", files: 100, enforced: true,
			mutate: func(c *config.Config) {
				c.Agents.Overrides = map[string]config.AgentOverride{"pruv": {MaxBudgetUSD: 0.10}}
			},
			low: 5.30, high: 6.10, discovery: 0.50, reviewers: 4.00, pruvLow: 0.80, pruvHigh: 1.60, callsLow: 8, callsHigh: 16,
		},
		{
			name: "enforced discovery override", files: 10, enforced: true,
			mutate: func(c *config.Config) {
				c.Agents.Overrides = map[string]config.AgentOverride{"discovery": {MaxBudgetUSD: 0.25}}
			},
			low: 3.05, high: 5.45, discovery: 0.25, reviewers: 0.40, pruvLow: 2.40, pruvHigh: 4.80, callsLow: 8, callsHigh: 16,
		},
		{
			name: "expected above max", files: 100,
			mutate: func(c *config.Config) { c.Cost.ExpectedFindingsPerReviewer = 20 },
			low:    9.30, high: 9.30, discovery: 0.50, reviewers: 4.00, pruvLow: 4.80, pruvHigh: 4.80, callsLow: 16, callsHigh: 16,
		},
		{
			name: "zero files", files: 0,
			low: 2.90, high: 5.30, discovery: 0.50, reviewers: 0, pruvLow: 2.40, pruvHigh: 4.80, callsLow: 8, callsHigh: 16,
		},
		{
			name: "one reviewer", files: 42,
			mutate: func(c *config.Config) { c.Audit.Reviewers = []string{"correctness"} },
			low:    2.54, high: 3.74, discovery: 0.50, reviewers: 0.84, pruvLow: 1.20, pruvHigh: 2.40, callsLow: 4, callsHigh: 8,
		},
		{
			// 3 × 0.0333 = 0.0999 is recorded as 0.10.
			name: "rounds to cents", files: 3,
			mutate: func(c *config.Config) {
				c.Audit.Reviewers = []string{"correctness"}
				c.Cost.Rates.FileReviewUSD = 0.0333
			},
			low: 1.80, high: 3.00, discovery: 0.50, reviewers: 0.10, pruvLow: 1.20, pruvHigh: 2.40, callsLow: 4, callsHigh: 8,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			e := ForAudit(cfg, tc.files, tc.enforced)
			if e.Kind != KindAudit || e.Model != ModelVersion || e.Files != tc.files || e.Reviewers != len(cfg.Audit.Reviewers) || e.HarnessEnforcesBudget != tc.enforced {
				t.Errorf("identity = %+v", e)
			}
			if e.PruvCallsLow != tc.callsLow || e.PruvCallsHigh != tc.callsHigh {
				t.Errorf("pruv calls = %d–%d, want %d–%d", e.PruvCallsLow, e.PruvCallsHigh, tc.callsLow, tc.callsHigh)
			}
			for _, c := range []struct {
				name      string
				got, want float64
			}{
				{"low", e.LowUSD, tc.low}, {"high", e.HighUSD, tc.high}, {"discovery", e.DiscoveryUSD, tc.discovery},
				{"reviewers", e.ReviewersUSD, tc.reviewers}, {"pruv low", e.PruvLowUSD, tc.pruvLow}, {"pruv high", e.PruvHighUSD, tc.pruvHigh},
			} {
				if !near(c.got, c.want) {
					t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
				}
			}
			want := contract.CostRates{DiscoveryUSD: cfg.Cost.Rates.DiscoveryUSD, FileReviewUSD: cfg.Cost.Rates.FileReviewUSD, PruvCallUSD: cfg.Cost.Rates.PruvCallUSD}
			if e.Rates != want {
				t.Errorf("rates = %+v, want the configured %+v", e.Rates, want)
			}
		})
	}
}

func TestLinesDoNotMislabelRoundingAsCapped(t *testing.T) {
	cfg := testConfig()
	for _, files := range []int{1, 3, 7, 25, 33, 101, 333, 1001} {
		for _, rate := range []float64{0.0025, 0.0125, 0.0333, 0.015, 0.02} {
			cfg.Cost.Rates.FileReviewUSD = rate
			for _, enforced := range []bool{false, true} {
				c := cfg
				c.Agents.MaxBudgetUSD = 1000 // enforced, but never reached
				for _, l := range Lines(ForAudit(c, files, enforced), 0) {
					if strings.Contains(l, "capped") {
						t.Errorf("files %d, rate %v, enforced %t: uncapped estimate marked capped: %q", files, rate, enforced, l)
					}
				}
			}
		}
	}
}

func TestLinesShowSubCentRates(t *testing.T) {
	cfg := testConfig()
	cfg.Cost.Rates.FileReviewUSD = 0.0125
	got := strings.Join(Lines(ForAudit(cfg, 100, false), 0), "\n")
	if !strings.Contains(got, "2 × 100 files × $0.0125") {
		t.Errorf("sub-cent rate must not be rounded for display:\n%s", got)
	}
}

func TestForVerify(t *testing.T) {
	cfg := testConfig()
	e := ForVerify(cfg, 5, false)
	if e.Kind != KindVerify || !near(e.LowUSD, 1.50) || !near(e.HighUSD, 1.50) || e.PruvCallsLow != 5 || e.PruvCallsHigh != 5 {
		t.Errorf("verify = %+v", e)
	}
	if e.DiscoveryUSD != 0 || e.ReviewersUSD != 0 || e.Files != 0 || e.Reviewers != 0 {
		t.Errorf("verify runs neither discovery nor reviewers: %+v", e)
	}
	cfg.Agents.Overrides = map[string]config.AgentOverride{"pruv": {MaxBudgetUSD: 0.20}}
	if e := ForVerify(cfg, 5, true); !near(e.HighUSD, 1.00) {
		t.Errorf("enforced pruv cap: high = %v, want 1.00", e.HighUSD)
	}
}

func TestOverBudget(t *testing.T) {
	e := contract.CostEstimate{LowUSD: 5, HighUSD: 9.30}
	for _, tc := range []struct {
		budget float64
		want   bool
	}{
		{9.30, false}, // equal is within budget
		{9.29, true},
		{10, false},
		{0, false}, // 0 disables the budget
	} {
		if got := OverBudget(e, tc.budget); got != tc.want {
			t.Errorf("OverBudget(high 9.30, budget %v) = %v, want %v", tc.budget, got, tc.want)
		}
	}
	if OverBudget(contract.CostEstimate{HighUSD: 1e6}, 0) {
		t.Error("budget 0 must never be exceeded")
	}
}

func TestTotal(t *testing.T) {
	cfg := testConfig()
	a := ForAudit(cfg, 100, false)
	b := ForAudit(cfg, 42, false)
	tot := Total(a, b)
	if tot.Kind != KindTotal || tot.Files != 142 || tot.Reviewers != 4 || tot.PruvCallsLow != 16 || tot.PruvCallsHigh != 32 {
		t.Errorf("total counts = %+v", tot)
	}
	if !near(tot.LowUSD, a.LowUSD+b.LowUSD) || !near(tot.HighUSD, a.HighUSD+b.HighUSD) || !near(tot.ReviewersUSD, 4.00+1.68) {
		t.Errorf("total usd = %+v", tot)
	}
	if tot.HarnessEnforcesBudget {
		t.Error("a total is enforced only if every part is")
	}
	if got := strings.Join(Lines(tot, 0), "\n"); !strings.Contains(got, "(total, 142 files, 4 reviewers): $11.48 – $16.28") || !strings.Contains(got, "reviewers  4 executions") {
		t.Errorf("total lines:\n%s", got)
	}
	if same := Total(a); same.Kind != KindAudit || same.LowUSD != a.LowUSD {
		t.Errorf("total of one estimate keeps its kind: %+v", same)
	}
	if empty := Total(); empty.LowUSD != 0 || empty.HighUSD != 0 || empty.Model != ModelVersion {
		t.Errorf("empty total = %+v", empty)
	}
}

func TestLines(t *testing.T) {
	cfg := testConfig()
	for _, tc := range []struct {
		name   string
		est    contract.CostEstimate
		budget float64
		want   string
	}{
		{
			name: "unenforced within budget", est: ForAudit(cfg, 42, false), budget: 10,
			want: `cost estimate (audit, 42 files, 2 reviewers): $4.58 – $6.98
  discovery                         $0.50
  reviewers  2 × 42 files × $0.02   $1.68
  pruv       8–16 calls × $0.30     $2.40 – $4.80
  budget $10.00 (cost.budget_usd): within budget
  the harness cannot enforce agents.max_budget_usd (ADR 0005); this estimate is the only spend guard
  configured rates, not a quote; retries not included`,
		},
		{
			name: "enforced over budget, capped", budget: 5,
			est: func() contract.CostEstimate {
				c := testConfig()
				c.Agents.MaxBudgetUSD = 1
				return ForAudit(c, 100, true)
			}(),
			want: `cost estimate (audit, 100 files, 2 reviewers): $4.90 – $7.30
  discovery                         $0.50
  reviewers  2 × 100 files × $0.02  $2.00 (capped by agents.max_budget_usd)
  pruv       8–16 calls × $0.30     $2.40 – $4.80
  budget $5.00 (cost.budget_usd): estimate up to $7.30 exceeds the budget
  configured rates, not a quote; retries not included`,
		},
		{
			name: "verify without budget", est: ForVerify(cfg, 5, true), budget: 0,
			want: `cost estimate (verify, 5 findings): $1.50
  pruv       5 calls × $0.30        $1.50
  no run budget (cost.budget_usd is 0)
  configured rates, not a quote; retries not included`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(Lines(tc.est, tc.budget), "\n")
			if got != tc.want {
				t.Errorf("Lines:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
