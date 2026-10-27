// Package report renders Markdown reports from a run's persisted artifacts.
//
// Reports are generated deterministically from validated artifacts; no
// agent writes report text. Anything not supported by an artifact is
// reported as missing rather than filled in.
package report

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/findings"
	"github.com/olddognewflex/sift/internal/storage"
)

// Report file names, relative to the run's report directory.
const (
	ExecutiveSummary  = "executive-summary.md"
	ArchitectureAudit = "architecture-audit.md"
	FindingsInventory = "findings.md"
	RemediationPlan   = "remediation-plan.md"
)

// Item is a finding with its validation record and effective ratings.
type Item struct {
	contract.Finding
	Record *contract.DecisionRecord `json:"validation,omitempty"`
	// Effective ratings include any adjustment made by the validator.
	Severity   contract.Severity   `json:"effective_severity"`
	Confidence contract.Confidence `json:"effective_confidence"`
}

// Data is everything a report is built from.
type Data struct {
	Manifest  contract.Manifest
	Discovery *contract.Discovery
	Plan      *contract.ReviewPlan
	Reviews   []contract.Review
	Items     []Item
	// Missing lists expected artifacts that are absent.
	Missing []string
}

// Load reads a run's artifacts. Missing optional artifacts are recorded in
// Data.Missing; present but invalid artifacts are errors.
func Load(run *storage.Run) (*Data, error) {
	m, err := run.Manifest()
	if err != nil {
		return nil, err
	}
	d := &Data{Manifest: *m}
	optional := func(rel string, v contract.Validator) (bool, error) {
		if !run.Exists(rel) {
			d.Missing = append(d.Missing, rel)
			return false, nil
		}
		return true, run.ReadJSON(rel, v)
	}
	var disc contract.Discovery
	if ok, err := optional(storage.DiscoveryFile, &disc); err != nil {
		return nil, err
	} else if ok {
		d.Discovery = &disc
	}
	var plan contract.ReviewPlan
	if ok, err := optional(storage.ReviewPlanFile, &plan); err != nil {
		return nil, err
	} else if ok {
		d.Plan = &plan
		for _, t := range plan.Tasks {
			var rv contract.Review
			if ok, err := optional(fmt.Sprintf(storage.ReviewFilePattn, t.Agent), &rv); err != nil {
				return nil, err
			} else if ok {
				d.Reviews = append(d.Reviews, rv)
			}
		}
	}
	var cands contract.FindingSet
	ok, err := optional(storage.CandidatesFile, &cands)
	if err != nil || !ok {
		return d, err
	}
	var decs contract.DecisionSet
	if _, err := optional(storage.DecisionsFile, &decs); err != nil {
		return nil, err
	}
	merged, err := findings.Merge(cands.Findings, decs.Decisions)
	if err != nil {
		return nil, err
	}
	records := map[string]*contract.DecisionRecord{}
	for i := range decs.Decisions {
		records[decs.Decisions[i].FindingID] = &decs.Decisions[i]
	}
	for _, f := range merged {
		it := Item{Finding: f, Record: records[f.ID], Severity: f.Severity, Confidence: f.Confidence}
		if r := it.Record; r != nil && r.Decision != nil {
			if r.Decision.AdjustedSeverity != "" {
				it.Severity = r.Decision.AdjustedSeverity
			}
			if r.Decision.AdjustedConfidence != "" {
				it.Confidence = r.Decision.AdjustedConfidence
			}
		}
		d.Items = append(d.Items, it)
	}
	return d, nil
}

// Generate renders all reports into the run's report directory.
func Generate(run *storage.Run, now func() time.Time) (contract.ReportMeta, error) {
	d, err := Load(run)
	if err != nil {
		return contract.ReportMeta{}, err
	}
	files := map[string]string{
		ExecutiveSummary:  d.executiveSummary(),
		ArchitectureAudit: d.architectureAudit(),
		FindingsInventory: d.findingsInventory(),
		RemediationPlan:   d.remediationPlan(),
	}
	meta := contract.ReportMeta{
		Header:         contract.NewHeader(contract.KindReportMeta, run.ID),
		GeneratedAt:    now().UTC(),
		RunStatus:      d.Manifest.Status,
		Partial:        d.Manifest.Status != contract.RunCompleted,
		SourceRevision: d.Manifest.Repository.SourceRevision,
		StateCounts:    d.counts(),
		Limitations:    d.limitations(),
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(files)) {
		rel := path.Join(storage.ReportDir, name)
		errs = append(errs, run.WriteFile(rel, []byte(files[name])))
		meta.Files = append(meta.Files, rel)
	}
	if err := errors.Join(errs...); err != nil {
		return meta, err
	}
	return meta, run.WriteJSON(storage.ReportMetaFile, &meta)
}

// ---------------------------------------------------------------------------
// Shared pieces

var stateOrder = []contract.ValidationState{
	contract.StateConfirmed, contract.StateSupportedRisk, contract.StateNeedsInvestigation,
	contract.StateUnvalidated, contract.StateRejected, contract.StateCandidate,
}

var stateLabels = map[contract.ValidationState]string{
	contract.StateConfirmed:          "Confirmed defects",
	contract.StateSupportedRisk:      "Supported risks",
	contract.StateNeedsInvestigation: "Needs investigation",
	contract.StateUnvalidated:        "Not validated",
	contract.StateRejected:           "Rejected",
	contract.StateCandidate:          "Candidate",
}

func (d *Data) counts() map[contract.ValidationState]int {
	c := map[contract.ValidationState]int{}
	for _, it := range d.Items {
		c[it.State]++
	}
	return c
}

func (d *Data) inState(states ...contract.ValidationState) []Item {
	var out []Item
	for _, it := range d.Items {
		if slices.Contains(states, it.State) {
			out = append(out, it)
		}
	}
	slices.SortStableFunc(out, func(a, b Item) int {
		return cmp.Or(
			cmp.Compare(slices.Index(stateOrder, a.State), slices.Index(stateOrder, b.State)),
			cmp.Compare(a.Severity.Rank(), b.Severity.Rank()),
			cmp.Compare(a.Confidence.Rank(), b.Confidence.Rank()),
			cmp.Compare(a.ID, b.ID),
		)
	})
	return out
}

func (d *Data) strengths() []contract.Strength {
	var out []contract.Strength
	for _, r := range d.Reviews {
		out = append(out, r.Report.Strengths...)
	}
	return out
}

// limitations lists everything that bounds confidence in this audit.
func (d *Data) limitations() []string {
	m := d.Manifest
	var out []string
	if m.Status != contract.RunCompleted {
		out = append(out, fmt.Sprintf("The audit run is %s; results are incomplete.", m.Status))
	}
	for _, t := range m.Tasks {
		if t.ID != "report" && t.Status != contract.TaskSucceeded {
			s := fmt.Sprintf("Task %s %s", t.ID, t.Status)
			if t.Error != "" {
				s += ": " + oneLine(t.Error, 300)
			}
			out = append(out, s+".")
		}
	}
	if d.Discovery != nil && d.Discovery.Report.Completeness == contract.CompletenessPartial {
		out = append(out, "Discovery reported partial completeness; see the unknowns in the architecture audit.")
	}
	if n := d.counts()[contract.StateUnvalidated]; n > 0 {
		out = append(out, fmt.Sprintf("%d finding(s) were not validated and must not be treated as accepted.", n))
	}
	out = append(out, "Agents read source code but could not run it. No finding is backed by test or execution results from this run.")
	if m.Repository.Dirty {
		switch m.Repository.DirtyPolicy {
		case contract.DirtyHead:
			out = append(out, "The working tree had uncommitted changes that were excluded from this audit.")
		case contract.DirtyInclude:
			out = append(out, "Uncommitted changes were audited. Evidence refers to the run's source snapshot, not to the commit alone.")
		}
	}
	if len(m.Repository.SkippedPaths) > 0 {
		out = append(out, fmt.Sprintf("%d path(s) were not included in the snapshot (symlinks, submodules); see the manifest.", len(m.Repository.SkippedPaths)))
	}
	if m.Integrity != nil && !m.Integrity.RepositoryUnchanged {
		out = append(out, "Repository integrity: "+m.Integrity.Detail)
	}
	for _, u := range m.Policy.Unenforced {
		out = append(out, "Isolation: "+u+".")
	}
	return out
}

func (d *Data) header(b *strings.Builder, title string) {
	m := d.Manifest
	fmt.Fprintf(b, "# %s\n\n", title)
	if m.Status != contract.RunCompleted {
		fmt.Fprintf(b, "> **%s audit.** This run did not complete all stages. The results below are incomplete. See *Confidence and limitations*.\n\n", strings.ToUpper(string(m.Status)))
	}
	fmt.Fprintf(b, "| | |\n|---|---|\n")
	fmt.Fprintf(b, "| Repository | `%s` |\n", m.Repository.Path)
	fmt.Fprintf(b, "| Revision | `%s`%s |\n", m.Repository.SourceRevision, branch(m.Repository.Branch))
	fmt.Fprintf(b, "| Run | `%s` (%s) |\n", m.RunID, m.Status)
	fmt.Fprintf(b, "| Harness | %s %s%s |\n", m.Harness.Name, m.Harness.Version, modelNote(m))
	fmt.Fprintf(b, "| Started | %s |\n\n", m.CreatedAt.Format(time.RFC3339))
}

func branch(b string) string {
	if b == "" {
		return ""
	}
	return " on `" + b + "`"
}

func modelNote(m contract.Manifest) string {
	if m.Harness.Model != "" {
		return ", model " + m.Harness.Model
	}
	return ", harness default model"
}

func (d *Data) writeLimitations(b *strings.Builder) {
	b.WriteString("## Confidence and limitations\n\n")
	for _, l := range d.limitations() {
		fmt.Fprintf(b, "- %s\n", l)
	}
	b.WriteString("\n")
}

func (d *Data) writeCounts(b *strings.Builder) {
	c := d.counts()
	b.WriteString("| Outcome | Count |\n|---|---|\n")
	for _, s := range stateOrder {
		if s == contract.StateCandidate && c[s] == 0 {
			continue
		}
		fmt.Fprintf(b, "| %s | %d |\n", stateLabels[s], c[s])
	}
	fmt.Fprintf(b, "| **Total candidates** | %d |\n\n", len(d.Items))
}

func cell(s string) string {
	return strings.ReplaceAll(oneLine(s, 200), "|", `\|`)
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func loc(l contract.Location) string {
	if l.StartLine == l.EndLine {
		return fmt.Sprintf("%s:%d", l.Path, l.StartLine)
	}
	return fmt.Sprintf("%s:%d-%d", l.Path, l.StartLine, l.EndLine)
}

func locs(ls []contract.Location) string {
	parts := make([]string, len(ls))
	for i, l := range ls {
		parts[i] = "`" + loc(l) + "`"
	}
	return strings.Join(parts, ", ")
}

func bullet(b *strings.Builder, label, text string) {
	if strings.TrimSpace(text) != "" {
		fmt.Fprintf(b, "- **%s:** %s\n", label, strings.TrimSpace(text))
	}
}

func list(b *strings.Builder, title string, items []string, empty string) {
	fmt.Fprintf(b, "**%s**\n\n", title)
	if len(items) == 0 {
		fmt.Fprintf(b, "- %s\n\n", empty)
		return
	}
	for _, it := range items {
		fmt.Fprintf(b, "- %s\n", it)
	}
	b.WriteString("\n")
}

// ---------------------------------------------------------------------------
// Executive summary

func (d *Data) executiveSummary() string {
	var b strings.Builder
	d.header(&b, "Sift Audit: Executive Summary")
	b.WriteString("## System\n\n")
	if d.Discovery != nil {
		fmt.Fprintf(&b, "%s\n\n", d.Discovery.Report.Summary)
	} else {
		b.WriteString("Discovery did not produce a system description.\n\n")
	}
	b.WriteString("## Outcome\n\n")
	d.writeCounts(&b)

	top := d.inState(contract.StateConfirmed, contract.StateSupportedRisk)
	b.WriteString("## Significant findings\n\n")
	if len(top) == 0 {
		b.WriteString("No finding was confirmed or accepted as a supported risk.")
		if len(d.inState(contract.StateNeedsInvestigation, contract.StateUnvalidated)) > 0 {
			b.WriteString(" Some findings remain unresolved; see the findings inventory.")
		}
		b.WriteString("\n\n")
	}
	for i, it := range top {
		if i == 5 {
			fmt.Fprintf(&b, "\n…and %d more in the remediation plan.\n", len(top)-5)
			break
		}
		fmt.Fprintf(&b, "%d. **%s** %s (%s, %s severity, %s confidence)\n", i+1, it.ID, it.Title, stateLabels[it.State], it.Severity, it.Confidence)
	}
	b.WriteString("\n## Key strengths\n\n")
	st := d.strengths()
	if len(st) == 0 {
		b.WriteString("No strengths were recorded.\n")
	}
	for i, s := range st {
		if i == 5 {
			break
		}
		fmt.Fprintf(&b, "- **%s**: %s\n", s.Title, oneLine(s.Description, 240))
	}
	b.WriteString("\n## Recommended priorities\n\n")
	if len(top) == 0 {
		b.WriteString("No remediation is recommended on the current evidence.\n")
	}
	for i, it := range top {
		if i == 3 {
			break
		}
		fmt.Fprintf(&b, "%d. %s: %s\n", i+1, it.ID, oneLine(it.Remediation, 240))
	}
	b.WriteString("\n## Scorecard\n\nNo scorecard is produced. Sift does not assign numeric scores unless measurements support them. This run collected source evidence only.\n\n")
	d.writeLimitations(&b)
	return b.String()
}

// ---------------------------------------------------------------------------
// Architecture audit

func (d *Data) architectureAudit() string {
	var b strings.Builder
	d.header(&b, "Sift Audit: Detailed Report")

	b.WriteString("## System context\n\n")
	if d.Discovery == nil {
		b.WriteString("Discovery did not complete, so no system context is available.\n\n")
	} else {
		r := d.Discovery.Report
		fmt.Fprintf(&b, "%s\n\n**Purpose:** %s\n\n", r.Summary, r.Purpose)
		fmt.Fprintf(&b, "**Languages:** %s. **Frameworks:** %s. **Discovery completeness:** %s.\n\n", orNone(r.Languages), orNone(r.Frameworks), r.Completeness)
		if len(r.Modules) > 0 {
			b.WriteString("| Module | Path | Responsibility |\n|---|---|---|\n")
			for _, m := range r.Modules {
				fmt.Fprintf(&b, "| %s | `%s` | %s |\n", cell(m.Name), m.Path, cell(m.Responsibility))
			}
			b.WriteString("\n")
		}
		var eps []string
		for _, e := range r.EntryPoints {
			eps = append(eps, fmt.Sprintf("`%s`: %s", e.Path, e.Description))
		}
		list(&b, "Entry points", eps, "None identified.")
		var deps []string
		for _, dep := range r.Dependencies {
			deps = append(deps, fmt.Sprintf("%s (%s): %s", dep.Name, dep.Source, dep.Purpose))
		}
		list(&b, "Key dependencies", deps, "None identified.")
		list(&b, "Critical paths", r.CriticalPaths, "None identified.")
		list(&b, "Unknowns", r.Unknowns, "None recorded.")
	}

	b.WriteString("## Review coverage\n\n")
	reviewed := map[string]contract.Review{}
	for _, r := range d.Reviews {
		reviewed[r.Agent] = r
	}
	if d.Plan == nil {
		b.WriteString("No review plan was produced; no specialist review ran.\n\n")
	} else {
		for _, t := range d.Plan.Tasks {
			fmt.Fprintf(&b, "### %s review\n\n", title(string(t.Category)))
			r, ok := reviewed[t.Agent]
			if !ok {
				status := "did not produce a review"
				if ts := d.Manifest.Task(t.ID); ts != nil {
					status = fmt.Sprintf("%s: %s", ts.Status, oneLine(ts.Error, 300))
				}
				fmt.Fprintf(&b, "**Not reviewed.** Task %s %s.\n\n", t.ID, status)
				continue
			}
			list(&b, "Reviewed scope", r.Report.ReviewedScope, "Not reported.")
			list(&b, "Unreviewed scope", r.Report.UnreviewedScope, "Not reported.")
			list(&b, "Reviewer limitations", r.Report.Limitations, "None reported.")
		}
	}

	b.WriteString("## Confirmed findings\n\n")
	d.writeDetails(&b, d.inState(contract.StateConfirmed), "No findings were confirmed.")
	b.WriteString("## Supported risks\n\n")
	d.writeDetails(&b, d.inState(contract.StateSupportedRisk), "No supported risks.")
	b.WriteString("## Unresolved findings\n\nThese were not accepted. They need investigation or were never validated. They are hypotheses, not defects.\n\n")
	d.writeDetails(&b, d.inState(contract.StateNeedsInvestigation, contract.StateUnvalidated), "None.")

	b.WriteString("## Rejected findings\n\n")
	rej := d.inState(contract.StateRejected)
	if len(rej) == 0 {
		b.WriteString("None.\n\n")
	} else {
		b.WriteString("| ID | Title | Why rejected |\n|---|---|---|\n")
		for _, it := range rej {
			why := it.StateReason
			if it.Record != nil && it.Record.Decision != nil {
				why = it.Record.Decision.Rationale
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", it.ID, cell(it.Title), cell(why))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Architectural strengths\n\n")
	st := d.strengths()
	if len(st) == 0 {
		b.WriteString("None recorded.\n\n")
	}
	for _, s := range st {
		fmt.Fprintf(&b, "- **%s**: %s", s.Title, strings.TrimSpace(s.Description))
		if len(s.Locations) > 0 {
			fmt.Fprintf(&b, " (%s)", locs(s.Locations))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	d.writeLimitations(&b)
	return b.String()
}

func (d *Data) writeDetails(b *strings.Builder, items []Item, empty string) {
	if len(items) == 0 {
		fmt.Fprintf(b, "%s\n\n", empty)
		return
	}
	for _, it := range items {
		fmt.Fprintf(b, "### %s: %s\n\n", it.ID, it.Title)
		fmt.Fprintf(b, "%s · %s severity · %s confidence · %s · reported by %s\n\n", stateLabels[it.State], it.Severity, it.Confidence, it.Category, it.ReportedBy)
		bullet(b, "Claim", it.Claim)
		bullet(b, "Locations", locs(it.Locations))
		bullet(b, "Triggering conditions", it.TriggeringConditions)
		bullet(b, "Expected impact", it.ExpectedImpact)
		bullet(b, "Recommended remediation", it.Remediation)
		bullet(b, "Alternatives", it.Alternatives)
		bullet(b, "Verification", it.VerificationCriteria)
		bullet(b, "Validation", it.StateReason)
		b.WriteString("\n**Evidence**\n\n")
		for i, e := range it.Evidence {
			check := it.EvidenceChecks[i]
			if e.Kind == contract.EvidenceSource {
				fmt.Fprintf(b, "%d. `%s` [%s]: %s\n", i+1, loc(contract.Location{Path: e.Path, StartLine: e.StartLine, EndLine: e.EndLine}), check.Status, strings.TrimSpace(e.Description))
				if check.Status == contract.CheckRelocated {
					fmt.Fprintf(b, "   (%s)\n", check.Detail)
				}
				fmt.Fprintf(b, "\n   ```\n%s\n   ```\n\n", indent(strings.TrimRight(e.Excerpt, "\n"), "   "))
			} else {
				fmt.Fprintf(b, "%d. Execution [%s]: %s\n   - Command: `%s`\n   - Result: %s\n", i+1, check.Status, e.Description, e.Command, oneLine(e.Result, 400))
				if e.Limitations != "" {
					fmt.Fprintf(b, "   - Limitations: %s\n", e.Limitations)
				}
				b.WriteString("\n")
			}
		}
		if r := it.Record; r != nil && r.Decision != nil {
			dec := r.Decision
			fmt.Fprintf(b, "**Pruv (%s v%s): %s", r.Validator, r.ValidatorVersion, dec.Verdict)
			if dec.Classification != "" {
				fmt.Fprintf(b, " as %s", dec.Classification)
			}
			b.WriteString("**\n\n")
			bullet(b, "Rationale", dec.Rationale)
			bullet(b, "Evidence assessment", dec.EvidenceAssessment)
			bullet(b, "Contradictory evidence sought", dec.ContradictoryEvidence)
			bullet(b, "Trigger assessment", dec.TriggerAssessment)
			bullet(b, "Impact assessment", dec.ImpactAssessment)
			bullet(b, "Remediation assessment", dec.RemediationAssessment)
			for _, q := range dec.OpenQuestions {
				bullet(b, "Open question", q)
			}
			b.WriteString("\n")
		} else if it.Record != nil && it.Record.Error != "" {
			fmt.Fprintf(b, "**Validation failed:** %s\n\n", oneLine(it.Record.Error, 400))
		}
	}
}

// ---------------------------------------------------------------------------
// Findings inventory

func (d *Data) findingsInventory() string {
	var b strings.Builder
	d.header(&b, "Sift Audit: Findings Inventory")
	d.writeCounts(&b)
	if len(d.Items) == 0 {
		b.WriteString("No candidate findings were produced.\n\n")
	} else {
		b.WriteString("| ID | Category | Severity | Confidence | State | Title | Evidence checks | Locations |\n|---|---|---|---|---|---|---|---|\n")
		for _, it := range d.inState(stateOrder...) {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n",
				it.ID, it.Category, it.Severity, it.Confidence, it.State, cell(it.Title), checkSummary(it.EvidenceChecks), locs(it.Locations))
		}
		b.WriteString("\nSeverity and confidence include any adjustment made by the validator.\n\n")
	}
	d.writeLimitations(&b)
	return b.String()
}

func checkSummary(cs []contract.EvidenceCheck) string {
	n := 0
	for _, c := range cs {
		if c.Status.Supports() {
			n++
		}
	}
	return fmt.Sprintf("%d/%d verified", n, len(cs))
}

// ---------------------------------------------------------------------------
// Remediation plan

func (d *Data) remediationPlan() string {
	var b strings.Builder
	d.header(&b, "Sift Audit: Remediation Plan")
	b.WriteString("Ordered by validation state (confirmed defects first), then severity, then confidence. Only findings accepted by the validator and backed by verified evidence are included.\n\n")
	items := d.inState(contract.StateConfirmed, contract.StateSupportedRisk)
	if len(items) == 0 {
		b.WriteString("No remediation is recommended on the current evidence.\n\n")
	}
	for i, it := range items {
		fmt.Fprintf(&b, "## %d. %s: %s\n\n", i+1, it.ID, it.Title)
		fmt.Fprintf(&b, "%s · %s severity · %s confidence · %s\n\n", stateLabels[it.State], it.Severity, it.Confidence, locs(it.Locations))
		bullet(&b, "Change", it.Remediation)
		bullet(&b, "Alternatives and trade-offs", it.Alternatives)
		if it.Record != nil && it.Record.Decision != nil {
			bullet(&b, "Validator's view of the remediation", it.Record.Decision.RemediationAssessment)
		}
		bullet(&b, "Acceptance and verification", it.VerificationCriteria)
		b.WriteString("\n")
	}
	open := d.inState(contract.StateNeedsInvestigation, contract.StateUnvalidated)
	if len(open) > 0 {
		b.WriteString("## Open investigations\n\nResolve these before acting on them.\n\n")
		for _, it := range open {
			fmt.Fprintf(&b, "- **%s** %s (%s): %s\n", it.ID, it.Title, it.State, oneLine(it.StateReason, 240))
			if it.Record != nil && it.Record.Decision != nil {
				for _, q := range it.Record.Decision.OpenQuestions {
					fmt.Fprintf(&b, "  - %s\n", q)
				}
			}
		}
		b.WriteString("\n")
	}
	d.writeLimitations(&b)
	return b.String()
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none identified"
	}
	return strings.Join(xs, ", ")
}

func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// Paths returns absolute paths of the generated report files.
func Paths(run *storage.Run) ([]string, error) {
	var meta contract.ReportMeta
	if err := run.ReadJSON(storage.ReportMetaFile, &meta); err != nil {
		return nil, err
	}
	out := make([]string, len(meta.Files))
	for i, f := range meta.Files {
		out[i] = run.Path(f)
		if _, err := os.Stat(out[i]); err != nil {
			return nil, fmt.Errorf("report file %s is missing; run `sift report --regenerate`", f)
		}
	}
	return out, nil
}
