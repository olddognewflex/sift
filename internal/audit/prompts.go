package audit

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/olddognewflex/sift/internal/contract"
)

// Prompts carry task input only: run facts and upstream artifacts. Analysis
// instructions live in the agent definitions (agents/*.md).

// limits describes the execution limits so agents can calibrate effort.
type limits struct {
	Timeout time.Duration
	Budget  float64
}

func (l limits) String() string {
	return fmt.Sprintf("Execution limits: %s wall-clock, $%.2f spend", l.Timeout, l.Budget)
}

func discoveryPrompt(rev string, l limits) string {
	return fmt.Sprintf(`# Task: repository discovery

Source revision: %s
Working directory: repository root (read-only snapshot)
%s

Survey this repository and return the discovery report.
`, rev, l)
}

func reviewPrompt(rev string, task contract.ReviewTask, max int, d contract.DiscoveryReport, l limits) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Task: %s review\n\n", task.Category)
	fmt.Fprintf(&b, "Source revision: %s\nWorking directory: repository root (read-only snapshot)\n%s\n", rev, l)
	fmt.Fprintf(&b, "Maximum findings: %d\n\n", max)
	if len(task.Focus) > 0 {
		b.WriteString("## Focus areas identified by discovery\n\n")
		for _, f := range task.Focus {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Discovery context\n\nVerify this against the source before relying on it.\n\n")
	writeJSON(&b, d)
	return b.String()
}

func pruvPrompt(rev string, f contract.Finding, d contract.DiscoveryReport, l limits) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Task: validate finding %s\n\n", f.ID)
	fmt.Fprintf(&b, "Source revision: %s\nWorking directory: repository root (read-only snapshot)\n%s\n\n", rev, l)
	b.WriteString("## Candidate finding\n\n")
	draft := f.FindingDraft
	writeJSON(&b, struct {
		ID         string            `json:"id"`
		Category   contract.Category `json:"category"`
		ReportedBy string            `json:"reported_by"`
		contract.FindingDraft
	}{f.ID, f.Category, f.ReportedBy, draft})
	b.WriteString("\n## Sift mechanical evidence checks\n\nOne check per evidence item, by index.\n\n")
	writeJSON(&b, f.EvidenceChecks)
	b.WriteString("\n## Sift mechanical location checks\n\nOne check per finding location, by index.\n\n")
	writeJSON(&b, f.LocationChecks)
	b.WriteString("\n## System context (from discovery)\n\n")
	fmt.Fprintf(&b, "%s\n\nPurpose: %s\n", d.Summary, d.Purpose)
	return b.String()
}

func writeJSON(b *strings.Builder, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Contract types always marshal; this would be a programming error.
		panic(err)
	}
	b.WriteString("```json\n")
	b.Write(data)
	b.WriteString("\n```\n")
}

// planReview derives the review plan deterministically from configuration
// and discovery: one task per configured reviewer, focused on discovery's
// critical paths.
func planReview(runID string, reviewers []string, max int, d contract.DiscoveryReport) contract.ReviewPlan {
	plan := contract.ReviewPlan{Header: contract.NewHeader(contract.KindReviewPlan, runID), MaxFindingsPerRun: max}
	for _, r := range reviewers {
		plan.Tasks = append(plan.Tasks, contract.ReviewTask{
			ID:       reviewTaskID(r),
			Agent:    r,
			Category: contract.Category(r),
			Focus:    d.CriticalPaths,
		})
	}
	return plan
}

func reviewTaskID(reviewer string) string { return "review-" + reviewer }
