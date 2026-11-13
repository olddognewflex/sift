package bench

import (
	"reflect"
	"slices"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
)

func loc(path string, start, end int) Location {
	return Location{Path: path, StartLine: start, EndLine: end}
}

func entry(id string, kind Kind, cat contract.Category, locs ...Location) Entry {
	return Entry{ID: id, Kind: kind, Category: cat, Title: id, Locations: locs, Anchor: "x", Notes: "n"}
}

func obs(id string, locs ...Location) Observation {
	cat, by := contract.CategoryCorrectness, "correctness"
	if id[:4] == "ARCH" {
		cat, by = contract.CategoryArchitecture, "architecture"
	}
	return Observation{ID: id, ReportedBy: by, Category: cat, Title: id, Locations: locs, State: contract.StateCandidate}
}

func matchKey() Key {
	return Key{Version: 1, Fixture: "m", Entries: []Entry{
		entry("D1", KindDefect, contract.CategoryCorrectness, loc("a.go", 10, 14)),
		entry("X1", KindDecoy, contract.CategoryCorrectness, loc("a.go", 30, 32)),
		entry("D2", KindDefect, contract.CategoryCorrectness, loc("b.go", 5, 5)),
	}}
}

func TestMatchKeyIsValid(t *testing.T) {
	if err := matchKey().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMatchSingle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		o       Observation
		entry   string
		outcome Outcome
		catOK   bool
	}{
		{"within tolerance", obs("CORR-001", loc("a.go", 16, 16)), "D1", OutcomeTP, true},
		{"just past tolerance", obs("CORR-001", loc("a.go", 18, 18)), "", OutcomeUnkeyed, false},
		{"span over 60 ignored", obs("CORR-001", loc("a.go", 1, 80)), "", OutcomeUnkeyed, false},
		{"span of exactly 60 used", obs("CORR-001", loc("a.go", 1, 60)), "D1", OutcomeTP, true},
		{"earliest location wins", obs("CORR-001", loc("b.go", 5, 5), loc("a.go", 31, 31)), "D2", OutcomeTP, true},
		{"later location used when first misses", obs("CORR-001", loc("a.go", 50, 50), loc("a.go", 31, 31)), "X1", OutcomeDecoy, true},
		{"wrong path", obs("CORR-001", loc("c.go", 10, 14)), "", OutcomeUnkeyed, false},
		{"off category", obs("ARCH-001", loc("a.go", 12, 12)), "D1", OutcomeTP, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Match(matchKey(), []Observation{tc.o})
			if len(got) != 1 {
				t.Fatalf("got %d assignments", len(got))
			}
			a := got[0]
			if a.EntryID != tc.entry || a.Outcome != tc.outcome || a.CategoryMatch != tc.catOK {
				t.Fatalf("got entry %q outcome %q catOK %v, want %q %q %v", a.EntryID, a.Outcome, a.CategoryMatch, tc.entry, tc.outcome, tc.catOK)
			}
		})
	}
}

func TestMatchDuplicateAndOrder(t *testing.T) {
	// Given out of order: CORR-010 sorts after CORR-002 numerically, and
	// ARCH sorts before CORR by registry order.
	in := []Observation{
		obs("CORR-010", loc("a.go", 11, 11)),
		obs("CORR-002", loc("a.go", 13, 13)),
		obs("ARCH-001", loc("a.go", 31, 31)),
	}
	got := Match(matchKey(), in)
	var ids []string
	var outcomes []Outcome
	for _, a := range got {
		ids = append(ids, a.Observation.ID)
		outcomes = append(outcomes, a.Outcome)
	}
	if !reflect.DeepEqual(ids, []string{"ARCH-001", "CORR-002", "CORR-010"}) {
		t.Fatalf("order %v", ids)
	}
	if !reflect.DeepEqual(outcomes, []Outcome{OutcomeDecoy, OutcomeTP, OutcomeDuplicate}) {
		t.Fatalf("outcomes %v", outcomes)
	}
	if got[2].EntryID != "D1" || got[2].Kind != KindDefect || got[2].EntryCategory != contract.CategoryCorrectness {
		t.Fatalf("duplicate should keep its entry: %+v", got[2])
	}
	if in[0].ID != "CORR-010" {
		t.Fatal("Match must not reorder its input")
	}
}

func TestMatchKnownIsNeutral(t *testing.T) {
	k := matchKey()
	k.Entries[1].Kind = KindKnown
	got := Match(k, []Observation{obs("CORR-001", loc("a.go", 30, 30))})
	if got[0].Outcome != OutcomeKnown || got[0].EntryID != "X1" {
		t.Fatalf("got %+v", got[0])
	}
}

func TestCompareIDs(t *testing.T) {
	ids := []string{"CORR-002", "ARCH-010", "CORR-001", "ARCH-002", "ZZZ-1", "CORR-100"}
	slices.SortFunc(ids, CompareIDs)
	want := []string{"ARCH-002", "ARCH-010", "CORR-001", "CORR-002", "CORR-100", "ZZZ-1"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
}
