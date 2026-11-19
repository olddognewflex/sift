package report

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/olddognewflex/sift/internal/contract"
)

// Verify runs (ADR 0006) re-check established findings at a later revision.
// Their single report shows, per finding, the outcome, the reason it follows,
// and the evidence as cited at the audit next to where it stands now.

var outcomeOrder = []contract.VerifyOutcome{
	contract.OutcomeFixed, contract.OutcomeStillPresent, contract.OutcomeCannotTell, contract.OutcomeUnverified,
}

// auditStateLabels describe a single finding's state at the audit; the
// plural stateLabels are the audit reports' section headings.
var auditStateLabels = map[contract.ValidationState]string{
	contract.StateConfirmed:     "a confirmed defect",
	contract.StateSupportedRisk: "a supported risk",
}

func auditState(s contract.ValidationState) string {
	if l, ok := auditStateLabels[s]; ok {
		return l
	}
	return string(s)
}

var outcomeLabels = map[contract.VerifyOutcome]string{
	contract.OutcomeFixed:        "Fixed",
	contract.OutcomeStillPresent: "Still present",
	contract.OutcomeCannotTell:   "Cannot tell",
	contract.OutcomeUnverified:   "Not verified",
}

func (d *Data) outcomeCounts() map[contract.VerifyOutcome]int {
	c := map[contract.VerifyOutcome]int{}
	for _, o := range outcomeOrder {
		c[o] = 0
	}
	for _, it := range d.Items {
		if it.Verification != nil {
			c[it.Verification.Outcome]++
		}
	}
	return c
}

// byOutcome orders items as the report lists them: fixed first, then by
// severity, confidence and ID.
func (d *Data) byOutcome() []Item {
	out := slices.Clone(d.Items)
	slices.SortStableFunc(out, func(a, b Item) int {
		return cmp.Or(
			cmp.Compare(slices.Index(outcomeOrder, outcome(a)), slices.Index(outcomeOrder, outcome(b))),
			cmp.Compare(a.Severity.Rank(), b.Severity.Rank()),
			cmp.Compare(a.Confidence.Rank(), b.Confidence.Rank()),
			cmp.Compare(a.ID, b.ID),
		)
	})
	return out
}

func outcome(it Item) contract.VerifyOutcome {
	if it.Verification == nil {
		return contract.OutcomeUnverified
	}
	return it.Verification.Outcome
}

// verifyLimitations lists what bounds confidence in a verify run's outcomes,
// beyond the run-level limitations shared with audits.
func (d *Data) verifyLimitations() []string {
	c := d.outcomeCounts()
	var out []string
	if n := c[contract.OutcomeUnverified]; n > 0 {
		out = append(out, fmt.Sprintf("%d finding(s) were not verified because the validator produced no decision. Run verify again for them.", n))
	}
	if n := c[contract.OutcomeCannotTell]; n > 0 {
		out = append(out, fmt.Sprintf("%d finding(s) were checked but the result is not actionable; they need human review.", n))
	}
	out = append(out,
		"A fixed outcome means the code changed and the validator judged that the claim no longer holds. No test or execution result backs it.",
		"Locations are shown as cited at the audit. Only source evidence excerpts are relocated in the new snapshot.",
	)
	for _, it := range d.Items {
		if slices.ContainsFunc(it.Evidence, func(e contract.Evidence) bool { return e.Kind != contract.EvidenceSource }) {
			out = append(out, "Execution evidence from the audit cannot be re-checked against the new snapshot.")
			break
		}
	}
	return out
}

func (d *Data) verificationReport() string {
	var b strings.Builder
	m := d.Manifest
	v := m.Verifies
	b.WriteString("# Sift Verify: Verification Report\n\n")
	if m.Status != contract.RunCompleted {
		fmt.Fprintf(&b, "> **%s verify run.** This run did not complete. The results below may be incomplete. See *Confidence and limitations*.\n\n", strings.ToUpper(string(m.Status)))
	}
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Repository | `%s` |\n", m.Repository.Path)
	fmt.Fprintf(&b, "| Audited revision | `%s` (run `%s`) |\n", v.SourceRevision, v.Run)
	fmt.Fprintf(&b, "| Verified revision | `%s`%s |\n", m.Repository.SourceRevision, branch(m.Repository.Branch))
	fmt.Fprintf(&b, "| Run | `%s` (%s) |\n", m.RunID, m.Status)
	fmt.Fprintf(&b, "| Harness | %s %s%s |\n", m.Harness.Name, m.Harness.Version, modelNote(m))
	fmt.Fprintf(&b, "| Started | %s |\n\n", m.CreatedAt.Format(time.RFC3339))

	b.WriteString("## Outcome\n\n| Outcome | Count |\n|---|---|\n")
	c := d.outcomeCounts()
	for _, o := range outcomeOrder {
		fmt.Fprintf(&b, "| %s | %d |\n", outcomeLabels[o], c[o])
	}
	fmt.Fprintf(&b, "| **Findings selected** | %d |\n\n", len(d.Items))

	b.WriteString("## Findings\n\n")
	for _, it := range d.byOutcome() {
		writeVerification(&b, it)
	}
	d.writeLimitations(&b)
	return b.String()
}

func writeVerification(b *strings.Builder, it Item) {
	r := it.Verification
	fmt.Fprintf(b, "### %s: %s\n\n", it.ID, it.Title)
	fmt.Fprintf(b, "%s · was %s at the audit · %s severity · %s confidence · %s\n\n",
		outcomeLabels[outcome(it)], auditState(it.State), it.Severity, it.Confidence, it.Category)
	if r != nil {
		bullet(b, "Outcome reason", r.OutcomeReason)
	}
	bullet(b, "Claim", it.Claim)
	bullet(b, "Locations at the audit", locs(it.Locations))

	b.WriteString("\n**Evidence: at the audit → now**\n\n")
	for i, e := range it.Evidence {
		rl := relocationAt(r, i)
		if e.Kind != contract.EvidenceSource {
			fmt.Fprintf(b, "%d. Execution: %s (`%s`); not re-checked\n", i+1, strings.TrimSpace(e.Description), e.Command)
			continue
		}
		fmt.Fprintf(b, "%d. %s → %s: %s\n", i+1, evidenceLoc(e), nowLoc(e, rl), strings.TrimSpace(e.Description))
		if rl != nil && rl.Detail != "" && rl.Status != contract.CheckVerified {
			fmt.Fprintf(b, "   (%s)\n", oneLine(rl.Detail, 300))
		}
	}
	b.WriteString("\n")

	if r == nil {
		return
	}
	if dec := r.Decision; dec != nil {
		if len(dec.AdditionalEvidence) > 0 {
			b.WriteString("**Evidence added by the validator**\n\n")
			for i, e := range dec.AdditionalEvidence {
				if e.Kind != contract.EvidenceSource {
					fmt.Fprintf(b, "%d. Execution: %s (`%s`)\n", i+1, strings.TrimSpace(e.Description), e.Command)
					continue
				}
				fmt.Fprintf(b, "%d. %s: %s; %s\n", i+1, addedLoc(e, additionalAt(r, i)), strings.TrimSpace(e.Description), priorMatches(r, i))
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "**Pruv (%s v%s): %s**\n\n", r.Validator, r.ValidatorVersion, dec.Verdict)
		bullet(b, "Rationale", dec.Rationale)
		bullet(b, "Evidence assessment", dec.EvidenceAssessment)
		for _, q := range dec.OpenQuestions {
			bullet(b, "Open question", q)
		}
		b.WriteString("\n")
	} else if r.Error != "" {
		fmt.Fprintf(b, "**Not verified:** %s\n\n", oneLine(r.Error, 400))
	}
}

func evidenceLoc(e contract.Evidence) string {
	return "`" + loc(contract.Location{Path: e.Path, StartLine: e.StartLine, EndLine: e.EndLine}) + "`"
}

// nowLoc describes where cited source evidence stands in the new snapshot.
func nowLoc(e contract.Evidence, rl *contract.Relocation) string {
	if rl == nil {
		return "not checked"
	}
	switch rl.Status {
	case contract.CheckVerified:
		return evidenceLoc(e) + " [verified]"
	case contract.CheckRelocated:
		l := contract.Location{
			Path:      cmp.Or(rl.ActualPath, e.Path),
			StartLine: cmp.Or(rl.ActualStartLine, e.StartLine),
			EndLine:   cmp.Or(rl.ActualEndLine, e.EndLine),
		}
		return "`" + loc(l) + "` [relocated]"
	default:
		return fmt.Sprintf("[%s]", rl.Status)
	}
}

// addedLoc describes evidence the validator added. Unlike cited evidence it
// has no audit-time location to show beside it, so its own citation is kept
// whatever the check found.
func addedLoc(e contract.Evidence, rl *contract.Relocation) string {
	if rl != nil && rl.Status == contract.CheckRelocated {
		return nowLoc(e, rl)
	}
	status := "not checked"
	if rl != nil {
		status = string(rl.Status)
	}
	return evidenceLoc(e) + " [" + status + "]"
}

func relocationAt(r *contract.VerificationRecord, i int) *contract.Relocation {
	if r == nil {
		return nil
	}
	return checkAt(r.Relocations, i)
}

func additionalAt(r *contract.VerificationRecord, i int) *contract.Relocation {
	return checkAt(r.AdditionalChecks, i)
}

func checkAt(rs []contract.Relocation, i int) *contract.Relocation {
	for j := range rs {
		if rs[j].Index == i {
			return &rs[j]
		}
	}
	return nil
}

// priorMatches reports how often an added excerpt occurs in the audited
// snapshot, which decides whether it counts as new code (ADR 0006 §5).
func priorMatches(r *contract.VerificationRecord, i int) string {
	if i >= len(r.AdditionalPriorMatches) {
		return "not searched in the audited snapshot"
	}
	switch n := r.AdditionalPriorMatches[i]; {
	case n < 0:
		return "could not be searched in the audited snapshot"
	case n == 0:
		return "absent from the audited snapshot"
	default:
		return fmt.Sprintf("found %d time(s) in the audited snapshot", n)
	}
}
