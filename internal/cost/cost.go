// Package cost estimates what a run will spend before any agent runs.
//
// The model is deliberately simple and pure (no I/O), so every caller
// (audit, verify, benchmark) shares one formula and the estimate can be
// recorded and later compared with the actual spend:
//
//	cap(agent) = effective agents.max_budget_usd for agent, if the harness
//	             enforces budgets; otherwise unlimited
//	discovery  = min(discovery_usd, cap(discovery))
//	reviewers  = Σ over reviewers r: min(files × file_review_usd, cap(r))
//	pruv calls = reviewers × max_findings_per_reviewer (high)
//	             reviewers × min(expected, max_findings_per_reviewer) (low)
//	low/high   = discovery + reviewers + pruv calls × min(pruv_call_usd, cap(pruv))
//
// Amounts are rounded to cents. Retries are not modelled.
package cost

import (
	"fmt"
	"math"
	"strings"

	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/contract"
)

// ModelVersion identifies the formula above in recorded estimates.
const ModelVersion = "files_x_reviewers_plus_pruv/v1"

// Estimate kinds.
const (
	KindAudit  = "audit"
	KindVerify = "verify"
	KindTotal  = "total"
)

// Agent names whose limits cap the discovery and Pruv terms. They match
// the agent definitions internal/audit runs (discoveryAgent,
// validatorAgent); reviewers are capped by their own names.
const (
	discoveryAgent = "discovery"
	pruvAgent      = "pruv"
)

// ForAudit estimates a full audit of a snapshot with files files. enforced
// says whether the harness enforces agents.max_budget_usd (the
// harness.CapBudgetLimit capability); only then does that cap bound each term.
func ForAudit(cfg config.Config, files int, enforced bool) contract.CostEstimate {
	e := base(cfg, KindAudit, enforced)
	e.Files = files
	e.Reviewers = len(cfg.Audit.Reviewers)
	e.DiscoveryUSD = cents(clamp(cfg, discoveryAgent, e.Rates.DiscoveryUSD, enforced))
	var reviewers float64
	for _, r := range cfg.Audit.Reviewers {
		reviewers += clamp(cfg, r, float64(files)*e.Rates.FileReviewUSD, enforced)
	}
	e.ReviewersUSD = cents(reviewers)
	maxFindings := cfg.Audit.MaxFindingsPerReviewer
	e.PruvCallsHigh = e.Reviewers * maxFindings
	e.PruvCallsLow = e.Reviewers * min(cfg.Cost.ExpectedFindingsPerReviewer, maxFindings)
	pruv := clamp(cfg, pruvAgent, e.Rates.PruvCallUSD, enforced)
	e.PruvLowUSD = cents(float64(e.PruvCallsLow) * pruv)
	e.PruvHighUSD = cents(float64(e.PruvCallsHigh) * pruv)
	e.LowUSD = cents(e.DiscoveryUSD + e.ReviewersUSD + e.PruvLowUSD)
	e.HighUSD = cents(e.DiscoveryUSD + e.ReviewersUSD + e.PruvHighUSD)
	return e
}

// ForVerify estimates a verify run of selected findings: one Pruv execution
// each, and no discovery or reviewers (ADR 0006 §3).
func ForVerify(cfg config.Config, selected int, enforced bool) contract.CostEstimate {
	e := base(cfg, KindVerify, enforced)
	e.PruvCallsLow, e.PruvCallsHigh = selected, selected
	e.PruvLowUSD = cents(float64(selected) * clamp(cfg, pruvAgent, e.Rates.PruvCallUSD, enforced))
	e.PruvHighUSD = e.PruvLowUSD
	e.LowUSD, e.HighUSD = e.PruvLowUSD, e.PruvHighUSD
	return e
}

// Total sums estimates, e.g. the runs of a benchmark. A total of one
// estimate is that estimate. A total of several has kind total, keeps the
// first part's rates for display, and is enforced only if every part is.
func Total(ests ...contract.CostEstimate) contract.CostEstimate {
	switch len(ests) {
	case 0:
		return contract.CostEstimate{Kind: KindTotal, Model: ModelVersion}
	case 1:
		return ests[0]
	}
	t := contract.CostEstimate{Kind: KindTotal, Model: ModelVersion, Rates: ests[0].Rates, HarnessEnforcesBudget: true}
	for _, e := range ests {
		t.Files += e.Files
		t.Reviewers += e.Reviewers
		t.PruvCallsLow += e.PruvCallsLow
		t.PruvCallsHigh += e.PruvCallsHigh
		t.HarnessEnforcesBudget = t.HarnessEnforcesBudget && e.HarnessEnforcesBudget
		t.DiscoveryUSD += e.DiscoveryUSD
		t.ReviewersUSD += e.ReviewersUSD
		t.PruvLowUSD += e.PruvLowUSD
		t.PruvHighUSD += e.PruvHighUSD
		t.LowUSD += e.LowUSD
		t.HighUSD += e.HighUSD
	}
	for _, v := range []*float64{&t.DiscoveryUSD, &t.ReviewersUSD, &t.PruvLowUSD, &t.PruvHighUSD, &t.LowUSD, &t.HighUSD} {
		*v = cents(*v)
	}
	return t
}

// OverBudget reports whether the high estimate exceeds a positive budget.
// A budget of 0 disables the check.
func OverBudget(est contract.CostEstimate, budget float64) bool {
	return budget > 0 && est.HighUSD > budget
}

// Lines renders est and its budget check for people, one line each,
// without a program prefix.
func Lines(est contract.CostEstimate, budget float64) []string {
	var head string
	switch est.Kind {
	case KindVerify:
		head = fmt.Sprintf("cost estimate (verify, %d findings)", est.PruvCallsHigh)
	default:
		head = fmt.Sprintf("cost estimate (%s, %d files, %d reviewers)", est.Kind, est.Files, est.Reviewers)
	}
	lines := []string{head + ": " + usdRange(est.LowUSD, est.HighUSD)}
	row := func(label, detail, amount string) {
		lines = append(lines, fmt.Sprintf("  %-10s %-22s %s", label, detail, amount))
	}
	// Whether a term hit its cap is visible by recomputing it uncapped,
	// with the same arithmetic ForAudit uses, so an uncapped term compares
	// equal; the half-cent margin guards against float noise all the same.
	// A total mixes estimates, so its terms can't be recomputed.
	const capped = " (capped by agents.max_budget_usd)"
	wasCapped := func(got, uncapped float64) bool { return est.Kind != KindTotal && got < cents(uncapped)-0.005 }
	if est.Kind != KindVerify {
		amount := usd(est.DiscoveryUSD)
		if wasCapped(est.DiscoveryUSD, est.Rates.DiscoveryUSD) {
			amount += capped
		}
		row("discovery", "", amount)
		amount = usd(est.ReviewersUSD)
		var uncapped float64
		for range est.Reviewers {
			uncapped += float64(est.Files) * est.Rates.FileReviewUSD
		}
		if wasCapped(est.ReviewersUSD, uncapped) {
			amount += capped
		}
		detail := fmt.Sprintf("%d × %d files × %s", est.Reviewers, est.Files, rate(est.Rates.FileReviewUSD))
		if est.Kind == KindTotal {
			// Reviewers × files would multiply across runs.
			detail = fmt.Sprintf("%d executions", est.Reviewers)
		}
		row("reviewers", detail, amount)
	}
	calls := fmt.Sprintf("%d", est.PruvCallsLow)
	if est.PruvCallsLow != est.PruvCallsHigh {
		calls = fmt.Sprintf("%d–%d", est.PruvCallsLow, est.PruvCallsHigh)
	}
	amount := usdRange(est.PruvLowUSD, est.PruvHighUSD)
	if wasCapped(est.PruvHighUSD, float64(est.PruvCallsHigh)*est.Rates.PruvCallUSD) {
		amount += capped
	}
	row("pruv", fmt.Sprintf("%s calls × %s", calls, rate(est.Rates.PruvCallUSD)), amount)
	switch {
	case budget <= 0:
		lines = append(lines, "  no run budget (cost.budget_usd is 0)")
	case OverBudget(est, budget):
		lines = append(lines, fmt.Sprintf("  budget %s (cost.budget_usd): estimate up to %s exceeds the budget", usd(budget), usd(est.HighUSD)))
	default:
		lines = append(lines, fmt.Sprintf("  budget %s (cost.budget_usd): within budget", usd(budget)))
	}
	if !est.HarnessEnforcesBudget {
		lines = append(lines, "  the harness cannot enforce agents.max_budget_usd (ADR 0005); this estimate is the only spend guard")
	}
	return append(lines, "  configured rates, not a quote; retries not included")
}

func base(cfg config.Config, kind string, enforced bool) contract.CostEstimate {
	r := cfg.Cost.Rates
	return contract.CostEstimate{
		Kind: kind, Model: ModelVersion, HarnessEnforcesBudget: enforced,
		Rates: contract.CostRates{DiscoveryUSD: r.DiscoveryUSD, FileReviewUSD: r.FileReviewUSD, PruvCallUSD: r.PruvCallUSD},
	}
}

// clamp caps the cost of one execution of agent at its effective
// max_budget_usd when the harness enforces it. config.Limits resolves
// per-agent overrides; an override of 0 inherits the global value, which
// Validate requires to be positive, so the cap is always a real limit.
func clamp(cfg config.Config, agent string, usd float64, enforced bool) float64 {
	if !enforced {
		return usd
	}
	_, _, budget := cfg.Limits(agent)
	if budget <= 0 {
		return usd
	}
	return min(usd, budget)
}

func cents(v float64) float64 { return math.Round(v*100) / 100 }

func usd(v float64) string { return fmt.Sprintf("$%.2f", v) }

// rate shows a per-unit rate in cents, or with up to four decimals when it
// is a fraction of a cent, so it is never rounded away for display.
func rate(v float64) string {
	if v == cents(v) {
		return usd(v)
	}
	return "$" + strings.TrimRight(fmt.Sprintf("%.4f", v), "0")
}

func usdRange(low, high float64) string {
	if usd(low) == usd(high) {
		return usd(low)
	}
	return usd(low) + " – " + usd(high)
}
