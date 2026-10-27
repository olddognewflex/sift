// Package findings assigns finding identity, mechanically checks evidence
// against the audited snapshot, and decides final validation states.
//
// The gate in this package is deterministic and has the last word: a
// validator's acceptance cannot confirm a finding whose evidence fails
// Sift's own checks.
package findings

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olddognewflex/sift/internal/contract"
)

var prefixes = map[contract.Category]string{
	contract.CategoryArchitecture: "ARCH",
	contract.CategoryCorrectness:  "CORR",
}

// Assign turns reviewer drafts into candidate findings with stable IDs
// (ARCH-001, ...) and evidence checks against snapshotDir.
func Assign(drafts []contract.FindingDraft, cat contract.Category, reportedBy, revision, snapshotDir string) []contract.Finding {
	out := make([]contract.Finding, 0, len(drafts))
	for i, d := range drafts {
		out = append(out, contract.Finding{
			ID:             fmt.Sprintf("%s-%03d", prefixes[cat], i+1),
			SourceRevision: revision,
			Category:       cat,
			ReportedBy:     reportedBy,
			FindingDraft:   d,
			EvidenceChecks: CheckAll(snapshotDir, d.Evidence),
			State:          contract.StateCandidate,
		})
	}
	return out
}

// CheckAll checks each evidence item.
func CheckAll(snapshotDir string, evidence []contract.Evidence) []contract.EvidenceCheck {
	checks := make([]contract.EvidenceCheck, len(evidence))
	for i, e := range evidence {
		checks[i] = Check(snapshotDir, e)
		checks[i].Index = i
	}
	return checks
}

// minRelocatableChars is the shortest normalized excerpt that may be matched
// outside its cited range; shorter ones ("}", "return nil") match anywhere.
const minRelocatableChars = 20

// Check verifies one evidence item against the snapshot.
func Check(snapshotDir string, e contract.Evidence) contract.EvidenceCheck {
	if e.Kind != contract.EvidenceSource {
		return contract.EvidenceCheck{Status: contract.CheckNotVerifiable,
			Detail: "execution evidence cannot be checked: the audit policy does not permit agents to run commands, so the result was not observed during this run"}
	}
	if !contract.ValidRelPath(e.Path) {
		return contract.EvidenceCheck{Status: contract.CheckMissingFile, Detail: fmt.Sprintf("%q is not a repository-relative path", e.Path)}
	}
	data, err := os.ReadFile(filepath.Join(snapshotDir, filepath.FromSlash(e.Path)))
	if err != nil {
		return contract.EvidenceCheck{Status: contract.CheckMissingFile, Detail: fmt.Sprintf("%s does not exist in the audited snapshot", e.Path)}
	}
	file := normalize(string(data))
	lineCount := strings.Count(string(data), "\n")
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lineCount++
	}
	if e.StartLine < 1 || e.EndLine < e.StartLine || e.StartLine > lineCount {
		return contract.EvidenceCheck{Status: contract.CheckInvalidRange,
			Detail: fmt.Sprintf("lines %d-%d are outside %s (%d lines)", e.StartLine, e.EndLine, e.Path, lineCount)}
	}
	excerpt := normalize(e.Excerpt)
	if len(excerpt) == 0 {
		return contract.EvidenceCheck{Status: contract.CheckMismatch, Detail: "excerpt is empty"}
	}
	matches := find(file, excerpt)
	for _, m := range matches {
		if m.start >= e.StartLine && m.end <= e.EndLine {
			return contract.EvidenceCheck{Status: contract.CheckVerified,
				Detail: fmt.Sprintf("excerpt found at %s:%d-%d", e.Path, m.start, m.end)}
		}
	}
	if len(matches) > 0 && excerptChars(excerpt) >= minRelocatableChars {
		best := matches[0]
		for _, m := range matches[1:] {
			if abs(m.start-e.StartLine) < abs(best.start-e.StartLine) {
				best = m
			}
		}
		return contract.EvidenceCheck{Status: contract.CheckRelocated,
			Detail:          fmt.Sprintf("excerpt not at cited lines %d-%d but found at %d-%d", e.StartLine, e.EndLine, best.start, best.end),
			ActualStartLine: best.start, ActualEndLine: best.end}
	}
	return contract.EvidenceCheck{Status: contract.CheckMismatch,
		Detail: fmt.Sprintf("excerpt does not appear at %s:%d-%d", e.Path, e.StartLine, e.EndLine)}
}

type line struct {
	no   int
	text string
}

type span struct{ start, end int }

// normalize trims each line and drops blank lines, keeping line numbers, so
// that indentation and blank-line differences do not defeat matching.
func normalize(s string) []line {
	var out []line
	for i, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, line{i + 1, t})
		}
	}
	return out
}

// find returns every place the excerpt occurs in file. The first and last
// excerpt lines may be partial lines; inner lines must match exactly.
func find(file, ex []line) []span {
	var out []span
	k := len(ex)
	for i := 0; i+k <= len(file); i++ {
		if matchAt(file[i:i+k], ex) {
			out = append(out, span{file[i].no, file[i+k-1].no})
		}
	}
	return out
}

func matchAt(f, ex []line) bool {
	if len(ex) == 1 {
		return strings.Contains(f[0].text, ex[0].text)
	}
	last := len(ex) - 1
	if !strings.HasSuffix(f[0].text, ex[0].text) || !strings.HasPrefix(f[last].text, ex[last].text) {
		return false
	}
	for j := 1; j < last; j++ {
		if f[j].text != ex[j].text {
			return false
		}
	}
	return true
}

func excerptChars(ex []line) int {
	n := 0
	for _, l := range ex {
		n += len(l.text)
	}
	return n
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Transition validates a state change. Candidates may move to any final
// state; final states are terminal within a run.
func Transition(from, to contract.ValidationState) error {
	if !contract.Valid(to) || to == contract.StateCandidate {
		return fmt.Errorf("invalid target state %q", to)
	}
	if from != contract.StateCandidate {
		return fmt.Errorf("finding is already %s; re-validation is not supported", from)
	}
	return nil
}

// Gate combines the validator's decision with Sift's mechanical evidence
// checks into a final state. decision is nil when validation failed.
func Gate(checks []contract.EvidenceCheck, decision *contract.ValidationDecision, extra []contract.EvidenceCheck) (contract.ValidationState, string) {
	if decision == nil {
		return contract.StateUnvalidated, "validation did not complete; the finding has not been independently checked"
	}
	var supported, contradicted, extraContradicted int
	for _, c := range checks {
		switch {
		case c.Status.Supports():
			supported++
		case c.Status.Contradicts():
			contradicted++
		}
	}
	for _, c := range extra {
		if c.Status.Contradicts() {
			extraContradicted++
		}
	}
	switch decision.Verdict {
	case contract.VerdictReject:
		return contract.StateRejected, "rejected by validator"
	case contract.VerdictInvestigate:
		return contract.StateNeedsInvestigation, "validator requested additional investigation"
	}
	// Accepted by the validator: Sift's evidence checks must also hold.
	switch {
	case contradicted > 0:
		return contract.StateNeedsInvestigation, fmt.Sprintf("validator accepted, but %d cited evidence item(s) do not match the audited revision", contradicted)
	case supported == 0:
		return contract.StateNeedsInvestigation, "validator accepted, but no cited evidence could be verified against the audited revision"
	case extraContradicted > 0:
		return contract.StateNeedsInvestigation, fmt.Sprintf("validator accepted citing %d evidence item(s) that do not match the audited revision", extraContradicted)
	case decision.Classification == contract.ClassDefect:
		return contract.StateConfirmed, fmt.Sprintf("accepted as a defect by validator; %d/%d evidence item(s) verified", supported, len(checks))
	default:
		return contract.StateSupportedRisk, fmt.Sprintf("accepted as a supported risk by validator; %d/%d evidence item(s) verified", supported, len(checks))
	}
}

// Merge applies validation records to candidate findings, returning the
// current state of every finding. The candidates themselves are not
// modified. A candidate without a decision record is unvalidated.
func Merge(candidates []contract.Finding, decisions []contract.DecisionRecord) ([]contract.Finding, error) {
	byID := make(map[string]contract.DecisionRecord, len(decisions))
	for _, d := range decisions {
		byID[d.FindingID] = d
	}
	out := make([]contract.Finding, len(candidates))
	for i, c := range candidates {
		f := c
		f.EvidenceChecks = slices.Clone(c.EvidenceChecks)
		to, reason := contract.StateUnvalidated, "no validation decision was recorded (validation did not run or was interrupted)"
		if d, ok := byID[c.ID]; ok {
			to, reason = d.FinalState, d.GateReason
		}
		if err := Transition(c.State, to); err != nil {
			return nil, fmt.Errorf("finding %s: %w", c.ID, err)
		}
		f.State, f.StateReason = to, reason
		out[i] = f
	}
	return out, nil
}
