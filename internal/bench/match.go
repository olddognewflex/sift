package bench

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/olddognewflex/sift/internal/contract"
)

// Matching rules (docs/benchmark.md). Changing them changes every score,
// so bump RulesVersion with any change.
const (
	RulesVersion = "match/v1"
	// LineTolerance widens each key location on both sides.
	LineTolerance = 3
	// MaxLocationSpan is the longest observation location, in lines, that
	// can match; longer ones are too vague to credit.
	MaxLocationSpan = 60
)

// Outcome is how an observation scores against a key.
type Outcome string

const (
	OutcomeTP        Outcome = "tp"        // first match of a defect
	OutcomeDecoy     Outcome = "decoy"     // first match of a decoy: a false positive
	OutcomeUnkeyed   Outcome = "unkeyed"   // matches nothing: a false positive
	OutcomeKnown     Outcome = "known"     // first match of a known issue: neutral
	OutcomeDuplicate Outcome = "duplicate" // a later match of an entry already claimed
)

// Assignment is the result of matching one observation.
type Assignment struct {
	Observation   Observation       `json:"observation"`
	EntryID       string            `json:"entry_id,omitempty"`
	Kind          Kind              `json:"kind,omitempty"`
	EntryCategory contract.Category `json:"entry_category,omitempty"`
	Outcome       Outcome           `json:"outcome"`
	CategoryMatch bool              `json:"category_match"`
}

// Match assigns each observation to at most one key entry. Observations
// are processed in ID order (CompareIDs); the first to match an entry claims
// it and later ones are duplicates. The input is not modified.
func Match(key Key, obs []Observation) []Assignment {
	sorted := slices.Clone(obs)
	slices.SortStableFunc(sorted, func(a, b Observation) int { return CompareIDs(a.ID, b.ID) })
	claimed := map[string]bool{}
	out := make([]Assignment, 0, len(sorted))
	for _, o := range sorted {
		a := Assignment{Observation: o, Outcome: OutcomeUnkeyed}
		if e := matchEntry(key, o); e != nil {
			a.EntryID, a.Kind, a.EntryCategory, a.CategoryMatch = e.ID, e.Kind, e.Category, e.Category == o.Category
			switch {
			case claimed[e.ID]:
				a.Outcome = OutcomeDuplicate
			case e.Kind == KindDefect:
				a.Outcome = OutcomeTP
			case e.Kind == KindDecoy:
				a.Outcome = OutcomeDecoy
			default:
				a.Outcome = OutcomeKnown
			}
			claimed[e.ID] = true
		}
		out = append(out, a)
	}
	return out
}

// matchEntry returns the entry hit by the observation's earliest location
// that hits any. Within one location, key order breaks a tie (possible
// only for multi-line locations).
func matchEntry(key Key, o Observation) *Entry {
	for _, ol := range o.Locations {
		end := max(ol.EndLine, ol.StartLine)
		if end-ol.StartLine+1 > MaxLocationSpan {
			continue
		}
		for i, e := range key.Entries {
			for _, el := range e.Locations {
				if ol.Path == el.Path && ol.StartLine <= el.EndLine+LineTolerance && end >= el.StartLine-LineTolerance {
					return &key.Entries[i]
				}
			}
		}
	}
	return nil
}

// CompareIDs orders finding IDs by reviewer registry order of their prefix
// (ARCH before CORR), then by number. Unrecognized IDs sort last, by text.
func CompareIDs(a, b string) int {
	pa, na := splitID(a)
	pb, nb := splitID(b)
	return cmp.Or(cmp.Compare(pa, pb), cmp.Compare(na, nb), strings.Compare(a, b))
}

func splitID(id string) (rank, num int) {
	prefix, digits, _ := strings.Cut(id, "-")
	rank = slices.IndexFunc(contract.Reviewers, func(r contract.Reviewer) bool { return r.Prefix == prefix })
	n, err := strconv.Atoi(digits)
	if rank < 0 || err != nil {
		return len(contract.Reviewers), 0
	}
	return rank, n
}
