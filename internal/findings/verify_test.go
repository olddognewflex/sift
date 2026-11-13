package findings

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/testutil"
)

// loop is the defect line cited at the audit (sample, line 6).
const loop = "for i := 1; i < len(xs)-1; i++ {"

// shifted is a file whose "return nil", cited at line 4 in the audit, moved
// down one line.
const shifted = "package a\n\n// inserted\nfunc f() error {\n\treturn nil\n}\n"

// minSource replaces Max with Min: it shares "m := xs[0]" with sample but
// not the loop.
const minSource = `package stats

// Min returns the smallest value.
func Min(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		if x < m {
			m = x
		}
	}
	return m
}
`

func snapshotOf(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	testutil.WriteFiles(t, dir, files)
	return dir
}

func TestRelocate(t *testing.T) {
	// Max reindented with spaces and moved into its own file.
	spaced := strings.ReplaceAll(strings.Replace(sample, "// Max returns the largest value.\n", "", 1), "\t", "    ")
	cases := []struct {
		name       string
		files      map[string]string
		ev         contract.Evidence
		want       contract.CheckStatus
		path       string // expected actual_path
		start, end int    // expected actual lines (0 when not relocated)
		detail     string // substring of the detail
	}{
		{name: "in place", files: map[string]string{"stats.go": sample},
			ev: src("stats.go", 6, 6, loop), want: contract.CheckVerified, detail: "stats.go:6-6"},
		{name: "same-file move", files: map[string]string{"stats.go": "// Copyright\n// notice\n" + sample},
			ev: src("stats.go", 6, 6, loop), want: contract.CheckRelocated, start: 8, end: 8},
		{name: "cross-file single match", files: map[string]string{"stats.go": minSource, "max.go": spaced},
			ev: src("stats.go", 5, 6, "m := xs[0]\n\t"+loop), want: contract.CheckRelocated, path: "max.go", start: 4, end: 5, detail: "only match"},
		{name: "file renamed", files: map[string]string{"pkg/stats/max.go": sample},
			ev: src("stats.go", 6, 6, loop), want: contract.CheckRelocated, path: "pkg/stats/max.go", start: 6, end: 6},
		{name: "two matches", files: map[string]string{"stats.go": minSource, "a.go": sample, "b.go": sample},
			ev: src("stats.go", 6, 6, loop), want: contract.CheckMismatch, detail: "matches 2 place(s)"},
		{name: "two matches on one line", files: map[string]string{"stats.go": minSource, "a.go": loop + " /* " + loop + " */\n"},
			ev: src("stats.go", 6, 6, loop), want: contract.CheckMismatch, detail: "matches 2 place(s)"},
		{name: "two matches after deletion", files: map[string]string{"a.go": sample, "b/c.go": sample},
			ev: src("stats.go", 6, 6, loop), want: contract.CheckMissingFile, detail: "matches 2 place(s)"},
		{name: "excerpt under 20 characters", files: map[string]string{"stats.go": strings.Repeat("// gone\n", 12), "other.go": sample},
			ev: src("stats.go", 11, 11, "return m"), want: contract.CheckMismatch, detail: "not searched"},
		{name: "short excerpt only shifted", files: map[string]string{"a.go": shifted},
			ev: src("a.go", 4, 4, "return nil"), want: contract.CheckNotVerifiable, detail: "still appears in the file"},
		{name: "short excerpt past new EOF but still in file", files: map[string]string{"stats.go": "return m\n"},
			ev: src("stats.go", 11, 11, "return m"), want: contract.CheckNotVerifiable, detail: "still appears in the file"},
		{name: "short excerpt, file deleted", files: map[string]string{"other.go": sample},
			ev: src("stats.go", 11, 11, "return m"), want: contract.CheckMissingFile, detail: "not searched"},
		{name: "start line past new EOF, two matches in file", files: map[string]string{"stats.go": "package stats\n" + loop + "\n\n" + loop + "\n"},
			ev: src("stats.go", 6, 6, loop), want: contract.CheckRelocated, start: 4, end: 4, detail: "past the end"},
		{name: "start line past new EOF", files: map[string]string{"stats.go": "package stats\n" + loop + "\n"},
			ev: src("stats.go", 6, 6, loop), want: contract.CheckRelocated, start: 2, end: 2},
		{name: "start line past new EOF, excerpt gone", files: map[string]string{"stats.go": "package stats\n"},
			ev: src("stats.go", 6, 6, loop), want: contract.CheckInvalidRange, detail: "matches 0 place(s)"},
		{name: "deleted file", files: map[string]string{"other.go": "package other\n"},
			ev: src("stats.go", 6, 6, loop), want: contract.CheckMissingFile, detail: "matches 0 place(s)"},
		{name: "execution evidence", files: map[string]string{"stats.go": sample},
			ev:   contract.Evidence{Kind: contract.EvidenceExecution, Description: "d", Command: "go test", Result: "FAIL"},
			want: contract.CheckNotVerifiable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Relocate(snapshotOf(t, c.files), c.ev)
			if got.Status != c.want || got.Detail == "" {
				t.Fatalf("status = %s (%s), want %s", got.Status, got.Detail, c.want)
			}
			if got.ActualPath != c.path || got.ActualStartLine != c.start || got.ActualEndLine != c.end {
				t.Errorf("actual = %q:%d-%d, want %q:%d-%d", got.ActualPath, got.ActualStartLine, got.ActualEndLine, c.path, c.start, c.end)
			}
			if !strings.Contains(got.Detail, c.detail) {
				t.Errorf("detail %q does not contain %q", got.Detail, c.detail)
			}
		})
	}
}

func TestPriorMatches(t *testing.T) {
	orig := snapshotOf(t, map[string]string{
		"stats.go": sample,
		"dup/a.go": "\t\t" + loop + "\n",
		"dup/b.go": "package b\n\n" + loop + "\n",
		"dup/c.go": "// twice: Min returns the smallest value.\n// again: Min returns the smallest value.\n",
		"dup/d.go": "total := total + weight; total := total + weight\n// abab abab abab abab abab abab\n",
	})
	exec := contract.Evidence{Kind: contract.EvidenceExecution, Description: "d", Command: "go test", Result: "ok"}
	cases := []struct {
		name string
		ev   contract.Evidence
		want int
	}{
		{"new code", src("guard.go", 1, 1, "if len(xs) == 0 { return 0 }"), 0},
		{"one match", src("max.go", 9, 9, "// Max returns the largest value."), 1},
		{"several matches across files", src("max.go", 2, 2, loop), 3},
		{"two occurrences in one file", src("max.go", 2, 2, "Min returns the smallest value."), 2},
		{"two occurrences on one line", src("max.go", 1, 1, "total := total + weight"), 2},
		{"overlapping occurrences on one line", src("max.go", 1, 1, "abab abab abab abab abab"), 2},
		{"indentation-insensitive", src("max.go", 1, 2, "    m := xs[0]\n\n        "+loop), 1},
		{"short excerpt", src("max.go", 1, 1, "return m"), -1},
		{"execution evidence", exec, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PriorMatches(orig, c.ev); got != c.want {
				t.Fatalf("PriorMatches = %d, want %d", got, c.want)
			}
		})
	}
	// A snapshot that cannot be searched must not make code look new.
	if got := PriorMatches(filepath.Join(orig, "missing"), src("a.go", 1, 1, loop)); got != -1 {
		t.Errorf("unreadable snapshot: PriorMatches = %d, want -1", got)
	}
}

func TestVerifyGate(t *testing.T) {
	r := func(statuses ...contract.CheckStatus) []contract.Relocation {
		out := make([]contract.Relocation, len(statuses))
		for i, s := range statuses {
			out[i] = contract.Relocation{EvidenceCheck: contract.EvidenceCheck{Index: i, Status: s}}
		}
		return out
	}
	const (
		verified  = contract.CheckVerified
		relocated = contract.CheckRelocated
		mismatch  = contract.CheckMismatch
		missing   = contract.CheckMissingFile
		invalid   = contract.CheckInvalidRange
		execution = contract.CheckNotVerifiable
	)
	accept := &contract.ValidationDecision{Verdict: contract.VerdictAccept, Classification: contract.ClassDefect}
	reject := &contract.ValidationDecision{Verdict: contract.VerdictReject}
	investigate := &contract.ValidationDecision{Verdict: contract.VerdictInvestigate}
	withAdded := func(d *contract.ValidationDecision, n int) *contract.ValidationDecision {
		c := *d
		c.AdditionalEvidence = make([]contract.Evidence, n)
		return &c
	}
	cases := []struct {
		name   string
		cited  []contract.Relocation
		dec    *contract.ValidationDecision
		added  []contract.Relocation
		prior  []int
		want   contract.VerifyOutcome
		reason string // substring of the reason
	}{
		{"no decision", r(verified), nil, nil, nil, contract.OutcomeUnverified, "did not complete"},
		{"no decision despite changed code", r(mismatch), nil, nil, nil, contract.OutcomeUnverified, "did not complete"},

		{"accept, cited verified", r(verified), accept, nil, nil, contract.OutcomeStillPresent, "1/1 cited"},
		{"accept, cited relocated", r(relocated), accept, nil, nil, contract.OutcomeStillPresent, "1/1 cited"},
		{"accept, only added evidence supports", r(execution), accept, r(verified), []int{1}, contract.OutcomeStillPresent, "1/1 added"},
		{"accept, cited mismatch", r(verified, mismatch), accept, nil, nil, contract.OutcomeCannotTell, "validator says still present, but 1 cited item(s) changed"},
		{"accept, cited file deleted", r(missing, invalid), accept, r(verified), []int{0}, contract.OutcomeCannotTell, "2 cited item(s) changed"},
		{"accept, added contradicts", r(verified), accept, r(mismatch), []int{-1}, contract.OutcomeCannotTell, "1 added item(s)"},
		{"accept, no support", r(execution), accept, nil, nil, contract.OutcomeCannotTell, "none of the cited or added evidence"},
		{"accept, short excerpt only shifted", r(execution), accept, nil, nil, contract.OutcomeCannotTell, "none of the cited or added evidence"},

		{"reject, cited mismatch", r(verified, mismatch), reject, nil, nil, contract.OutcomeFixed, "1 cited item(s) changed"},
		{"reject, cited file deleted", r(missing), reject, nil, nil, contract.OutcomeFixed, "1 cited item(s) changed"},
		{"reject, cited range gone", r(invalid), reject, r(verified), []int{4}, contract.OutcomeFixed, "1 cited item(s) changed"},
		{"reject, new code", r(verified), reject, r(verified), []int{0}, contract.OutcomeFixed, "1 item(s) of new code"},
		{"reject, new code relocated", r(relocated), reject, r(relocated, verified), []int{0, 2}, contract.OutcomeFixed, "1 item(s) of new code"},
		{"reject, added code pre-existed once", r(verified), reject, r(verified), []int{1}, contract.OutcomeCannotTell, "already existed"},
		{"reject, added code pre-existed often", r(verified), reject, r(relocated), []int{3}, contract.OutcomeCannotTell, "already existed"},
		{"reject, added code unsearchable", r(verified), reject, r(verified), []int{-1}, contract.OutcomeCannotTell, "disagrees with the original validation"},
		{"reject, prior count missing", r(verified), reject, r(verified), nil, contract.OutcomeCannotTell, "disagrees with the original validation"},
		{"reject, added item not in new snapshot", r(verified), reject, r(execution), []int{-1}, contract.OutcomeCannotTell, "disagrees with the original validation"},
		{"reject, code unchanged", r(verified, execution), reject, nil, nil, contract.OutcomeCannotTell, "disagrees with the original validation rather than showing a fix"},
		{"reject, code only moved", r(relocated, relocated), reject, nil, nil, contract.OutcomeCannotTell, "disagrees with the original validation"},
		{"reject, short excerpt only shifted", r(execution), reject, nil, nil, contract.OutcomeCannotTell, "disagrees with the original validation"},
		{"reject, added checks missing", r(mismatch), withAdded(reject, 1), nil, nil, contract.OutcomeCannotTell, "0 check(s) were recorded for the validator's 1 added"},
		{"accept, extra added check", r(verified), withAdded(accept, 1), r(verified, verified), []int{0, 0}, contract.OutcomeCannotTell, "2 check(s) were recorded for the validator's 1 added"},
		{"reject, added contradicts", r(verified), reject, r(mismatch), []int{0}, contract.OutcomeCannotTell, "1 added item(s) that do not match"},
		{"reject, added contradicts despite changed code", r(mismatch), reject, r(verified, missing), []int{0, 0}, contract.OutcomeCannotTell, "1 added item(s) that do not match"},

		{"investigate", r(verified), investigate, nil, nil, contract.OutcomeCannotTell, "could not tell"},
		{"investigate with changed code", r(mismatch), investigate, r(verified), []int{0}, contract.OutcomeCannotTell, "could not tell"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dec := c.dec
			if dec != nil && dec.AdditionalEvidence == nil {
				// One added evidence item per added check.
				dec = withAdded(dec, len(c.added))
			}
			got, reason := VerifyGate(c.cited, dec, c.added, c.prior)
			if got != c.want || reason == "" {
				t.Fatalf("VerifyGate = %s (%q), want %s", got, reason, c.want)
			}
			if !strings.Contains(reason, c.reason) {
				t.Errorf("reason %q does not contain %q", reason, c.reason)
			}
		})
	}
}

// A short cited excerpt that merely shifted down a line must not read as
// changed code: a reject would otherwise become fixed.
func TestShortExcerptShiftIsNotAFix(t *testing.T) {
	cited := []contract.Relocation{Relocate(snapshotOf(t, map[string]string{"a.go": shifted}), src("a.go", 4, 4, "return nil"))}
	reject := &contract.ValidationDecision{Verdict: contract.VerdictReject}
	if got, reason := VerifyGate(cited, reject, nil, nil); got != contract.OutcomeCannotTell {
		t.Fatalf("VerifyGate = %s (%q), want cannot_tell", got, reason)
	}
	// The same excerpt removed from the file is a change.
	gone := []contract.Relocation{Relocate(snapshotOf(t, map[string]string{"a.go": "package a\n\nfunc f() {\n}\n"}), src("a.go", 4, 4, "return nil"))}
	if got, reason := VerifyGate(gone, reject, nil, nil); got != contract.OutcomeFixed {
		t.Fatalf("VerifyGate = %s (%q), want fixed", got, reason)
	}
}

// An unreadable snapshot file aborts the search: Relocate keeps the first
// check's status and PriorMatches reports the item as unsearchable.
func TestSearchUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file permissions do not make the file unreadable here")
	}
	dir := snapshotOf(t, map[string]string{"a.go": sample, "b.go": sample})
	if err := os.Chmod(filepath.Join(dir, "b.go"), 0); err != nil {
		t.Fatal(err)
	}
	got := Relocate(dir, src("stats.go", 6, 6, loop))
	if got.Status != contract.CheckMissingFile || !strings.Contains(got.Detail, "could not be searched") {
		t.Errorf("Relocate = %s (%s), want missing_file noting the failed search", got.Status, got.Detail)
	}
	if n := PriorMatches(dir, src("stats.go", 6, 6, loop)); n != -1 {
		t.Errorf("PriorMatches = %d, want -1", n)
	}
}
