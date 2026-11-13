package findings

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/olddognewflex/sift/internal/contract"
)

// Relocate checks one evidence item against a later snapshot (ADR 0006 §4).
// It first applies Check's rules at the cited path, which verify the excerpt
// or relocate it within the file; a cited range now past the end of the file
// is relocated within the file the same way. If that does not support the
// item, it searches every file of the snapshot for the normalized excerpt:
// exactly one match relocates the item, recording the new path when it
// differs from the cited one. With zero or several matches the first check's
// status stands and the match count is noted in the detail, because a guess
// between several places would let unrelated code stand in for the cited
// code.
//
// An excerpt under minRelocatableChars is never relocated. If it still
// appears in the cited file it may only have shifted, so it is reported as
// not_verifiable rather than as a contradiction that would read as a fix.
//
// Git rename detection is deliberately not used: include snapshots aren't
// commits, and the content search depends only on the excerpt and the
// snapshot. The returned check's Index is zero; callers set it.
func Relocate(snapshotDir string, e contract.Evidence) contract.Relocation {
	c := Check(snapshotDir, e)
	excerpt := normalize(e.Excerpt)
	if e.Kind != contract.EvidenceSource || c.Status.Supports() || len(excerpt) == 0 {
		return contract.Relocation{EvidenceCheck: c}
	}
	var inFile []span
	if data, _, failed := readSnapshotFile(snapshotDir, e.Path); failed == nil {
		inFile = find(normalize(string(data)), excerpt)
	}
	switch {
	case excerptChars(excerpt) < minRelocatableChars && len(inFile) > 0:
		return contract.Relocation{EvidenceCheck: contract.EvidenceCheck{Status: contract.CheckNotVerifiable,
			Detail: fmt.Sprintf("excerpt under %d characters is no longer at %s:%d-%d but still appears in the file; it cannot be relocated reliably", minRelocatableChars, e.Path, e.StartLine, e.EndLine)}}
	case excerptChars(excerpt) < minRelocatableChars:
		c.Detail += fmt.Sprintf("; the excerpt is under %d characters, so the snapshot was not searched for it", minRelocatableChars)
		return contract.Relocation{EvidenceCheck: c}
	case len(inFile) > 0:
		// Check found the excerpt in the file only when it did not stop at
		// a start line past the end of the file.
		m := nearest(inFile, e.StartLine)
		return contract.Relocation{EvidenceCheck: contract.EvidenceCheck{Status: contract.CheckRelocated,
			Detail:          fmt.Sprintf("cited lines %d-%d are past the end of %s, but the excerpt was found at %d-%d", e.StartLine, e.EndLine, e.Path, m.start, m.end),
			ActualStartLine: m.start, ActualEndLine: m.end}}
	}
	matches, err := searchSnapshot(snapshotDir, excerpt)
	switch {
	case err != nil:
		c.Detail += fmt.Sprintf("; the snapshot could not be searched for the excerpt: %v", err)
	case len(matches) == 1:
		m := matches[0]
		r := contract.Relocation{EvidenceCheck: contract.EvidenceCheck{
			Status:          contract.CheckRelocated,
			Detail:          fmt.Sprintf("excerpt not at cited %s:%d-%d but found at %s:%d-%d, its only match in the snapshot", e.Path, e.StartLine, e.EndLine, m.path, m.start, m.end),
			ActualStartLine: m.start, ActualEndLine: m.end,
		}}
		if m.path != e.Path {
			r.ActualPath = m.path
		}
		return r
	default:
		c.Detail += fmt.Sprintf("; the excerpt matches %d place(s) in the snapshot, so it was not relocated", len(matches))
	}
	return contract.Relocation{EvidenceCheck: c}
}

// PriorMatches counts the places an evidence item's normalized excerpt
// occurs anywhere in the original (audited) snapshot, with the same
// normalization and minimum length as Relocate. VerifyGate treats added
// evidence as new code only when it has zero prior matches, so code that
// merely moved from another file is not mistaken for a fix.
//
// It returns -1 when the item cannot be searched: execution evidence, an
// excerpt under the minimum length, or a snapshot that cannot be read. -1
// never counts as new code.
func PriorMatches(origSnapshotDir string, e contract.Evidence) int {
	if e.Kind != contract.EvidenceSource {
		return -1
	}
	excerpt := normalize(e.Excerpt)
	if excerptChars(excerpt) < minRelocatableChars {
		return -1
	}
	matches, err := searchSnapshot(origSnapshotDir, excerpt)
	if err != nil {
		return -1
	}
	return len(matches)
}

type fileSpan struct {
	path string
	span
}

// searchSnapshot returns every occurrence of the normalized excerpt in the
// snapshot. WalkDir visits each directory's entries in lexical order, so the
// results are deterministic. Paths are repository-relative with forward slashes.
func searchSnapshot(snapshotDir string, excerpt []line) ([]fileSpan, error) {
	if len(excerpt) == 0 {
		return nil, nil
	}
	// Every matching line contains its excerpt line's trimmed text
	// verbatim, so a file without the longest such text cannot match and
	// need not be normalized.
	fragment := []byte(excerpt[0].text)
	for _, l := range excerpt[1:] {
		if len(l.text) > len(fragment) {
			fragment = []byte(l.text)
		}
	}
	var out []fileSpan
	err := filepath.WalkDir(snapshotDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Contains(data, fragment) {
			return nil
		}
		rel, err := filepath.Rel(snapshotDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, s := range find(normalize(string(data)), excerpt) {
			out = append(out, fileSpan{rel, s})
		}
		return nil
	})
	return out, err
}

// VerifyGate decides a verify outcome from the validator's decision and
// Sift's checks against the new snapshot (ADR 0006 §5). cited holds the
// relocation of each evidence item cited at the audit, added the relocation
// of each decision.additional_evidence item, and prior the PriorMatches
// count of each added item. dec is nil when the validator produced no
// decision.
//
// As with Gate, the validator's verdict alone cannot produce an outcome a
// user would act on: still_present needs the evidence to still hold, and
// fixed needs code that visibly changed.
func VerifyGate(cited []contract.Relocation, dec *contract.ValidationDecision, added []contract.Relocation, prior []int) (contract.VerifyOutcome, string) {
	if dec == nil {
		return contract.OutcomeUnverified, "verification did not complete; the finding has not been re-checked at this revision"
	}
	if len(added) != len(dec.AdditionalEvidence) {
		return contract.OutcomeCannotTell, fmt.Sprintf("%d check(s) were recorded for the validator's %d added evidence item(s), so its added evidence cannot be assessed", len(added), len(dec.AdditionalEvidence))
	}
	var citedSupported, citedContradicted, addedSupported, addedContradicted, newCode, preexisting int
	for _, c := range cited {
		switch {
		case c.Status.Supports():
			citedSupported++
		case c.Status.Contradicts():
			citedContradicted++
		}
	}
	for i, c := range added {
		switch {
		case c.Status.Contradicts():
			addedContradicted++
		case c.Status.Supports():
			addedSupported++
			// A missing count is treated as unsearchable (-1): never new code.
			n := -1
			if i < len(prior) {
				n = prior[i]
			}
			switch {
			case n == 0:
				newCode++
			case n > 0:
				preexisting++
			}
		}
	}
	switch dec.Verdict {
	case contract.VerdictInvestigate:
		return contract.OutcomeCannotTell, "validator could not tell whether the finding still holds at this revision; review it by hand"
	case contract.VerdictAccept:
		switch {
		case citedContradicted > 0:
			return contract.OutcomeCannotTell, fmt.Sprintf("validator says still present, but %d cited item(s) changed", citedContradicted)
		case addedContradicted > 0:
			return contract.OutcomeCannotTell, fmt.Sprintf("validator says still present, citing %d added item(s) that do not match this revision", addedContradicted)
		case citedSupported+addedSupported == 0:
			return contract.OutcomeCannotTell, "validator says still present, but none of the cited or added evidence could be found at this revision"
		default:
			return contract.OutcomeStillPresent, fmt.Sprintf("validator says still present; %d/%d cited and %d/%d added evidence item(s) found at this revision", citedSupported, len(cited), addedSupported, len(added))
		}
	case contract.VerdictReject:
		switch {
		case addedContradicted > 0:
			return contract.OutcomeCannotTell, fmt.Sprintf("validator says fixed, citing %d added item(s) that do not match this revision", addedContradicted)
		case citedContradicted > 0:
			return contract.OutcomeFixed, fmt.Sprintf("validator says fixed, and %d cited item(s) changed or disappeared", citedContradicted)
		case newCode > 0:
			return contract.OutcomeFixed, fmt.Sprintf("validator says fixed, citing %d item(s) of new code absent from the audited snapshot", newCode)
		case preexisting > 0:
			return contract.OutcomeCannotTell, fmt.Sprintf("validator says fixed, but none of the cited code changed and the %d added item(s) it cites already existed in the audited snapshot, so they are not new code", preexisting)
		default:
			return contract.OutcomeCannotTell, "validator says fixed, but none of the cited code changed and it cites no new code; it disagrees with the original validation rather than showing a fix"
		}
	}
	return contract.OutcomeCannotTell, fmt.Sprintf("validator returned unknown verdict %q", dec.Verdict)
}
