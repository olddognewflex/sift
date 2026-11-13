package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validKeyYAML = `version: 1
fixture: ledger
entries:
  - id: A-D1
    kind: defect
    category: correctness
    title: Transfer leaves the debit applied when the credit fails
    locations: [{path: ledger/transfer.go, start_line: 3, end_line: 5}]
    anchor: "if err := s.credit(to, amt); err != nil {"
    notes: the debit is never reversed
  - id: A-X1
    kind: decoy
    category: architecture
    title: Store is a global
    locations: [{path: ledger/transfer.go, start_line: 20, end_line: 20}]
    anchor: "var store"
    notes: it is only read at startup
`

var validFiles = map[string]string{
	"ledger/transfer.go": "package ledger\n\nfunc (s *S) Transfer() error {\n\tif err := s.credit(to, amt); err != nil {\n\t\treturn err\n\t}\n" +
		strings.Repeat("\n", 13) + "var store = map[string]int{}\n",
}

func writeKey(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadKeyValid(t *testing.T) {
	k, err := LoadKey(writeKey(t, validKeyYAML))
	if err != nil {
		t.Fatal(err)
	}
	if k.Fixture != "ledger" || len(k.Entries) != 2 || k.Entries[0].Locations[0].EndLine != 5 {
		t.Fatalf("unexpected key: %+v", k)
	}
	if err := k.ValidateAgainst(validFiles); err != nil {
		t.Fatal(err)
	}
}

func TestLoadKeyRejectsUnknownField(t *testing.T) {
	_, err := LoadKey(writeKey(t, strings.Replace(validKeyYAML, "    notes: the debit", "    severity: high\n    notes: the debit", 1)))
	if err == nil || !strings.Contains(err.Error(), "severity") {
		t.Fatalf("want unknown field error, got %v", err)
	}
}

func TestLoadKeyRejectsExtraDocument(t *testing.T) {
	_, err := LoadKey(writeKey(t, validKeyYAML+"---\nversion: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "document") {
		t.Fatalf("want extra document error, got %v", err)
	}
}

func TestValidateAgainstAnchorFirstOccurrence(t *testing.T) {
	k, err := LoadKey(writeKey(t, validKeyYAML))
	if err != nil {
		t.Fatal(err)
	}
	// Line 2 now also contains "var store", before A-X1's location (line
	// 20): the anchor's first occurrence is outside it, as after drift.
	drifted := map[string]string{"ledger/transfer.go": strings.Replace(validFiles["ledger/transfer.go"], "package ledger\n\n", "package ledger\nvar storeA = 1\n", 1)}
	err = k.ValidateAgainst(drifted)
	if err == nil || !strings.Contains(err.Error(), "A-X1") || !strings.Contains(err.Error(), "first occurrence") {
		t.Fatalf("want first occurrence error for A-X1, got %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*Key)
	}{
		{"version", "version", func(k *Key) { k.Version = 2 }},
		{"fixture", "fixture", func(k *Key) { k.Fixture = " " }},
		{"bad kind", "kind", func(k *Key) { k.Entries[0].Kind = "bug" }},
		{"bad category", "category", func(k *Key) { k.Entries[0].Category = "security" }},
		{"duplicate id", "duplicate", func(k *Key) { k.Entries[1].ID = "A-D1" }},
		{"missing id", "id", func(k *Key) { k.Entries[1].ID = "" }},
		{"no locations", "location", func(k *Key) { k.Entries[0].Locations = nil }},
		{"start below 1", "start_line", func(k *Key) { k.Entries[0].Locations[0].StartLine = 0 }},
		{"end before start", "end_line", func(k *Key) { k.Entries[0].Locations[0].EndLine = 2 }},
		{"known without notes", "notes", func(k *Key) { k.Entries[1].Kind = KindKnown; k.Entries[1].Notes = "" }},
		{"empty anchor", "anchor", func(k *Key) { k.Entries[1].Anchor = "" }},
		{"blank anchor", "anchor", func(k *Key) { k.Entries[1].Anchor = " \t " }},
		{"unclean path", "path", func(k *Key) { k.Entries[0].Locations[0].Path = "../ledger/transfer.go" }},
		{"absolute path", "path", func(k *Key) { k.Entries[0].Locations[0].Path = "/ledger/transfer.go" }},
		{"overlapping windows", "overlap", func(k *Key) {
			k.Entries[0].Locations[0] = Location{Path: "ledger/transfer.go", StartLine: 10, EndLine: 12}
			k.Entries[1].Locations[0] = Location{Path: "ledger/transfer.go", StartLine: 15, EndLine: 16}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, err := LoadKey(writeKey(t, validKeyYAML))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&k)
			err = k.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateWindowsInDifferentFilesOrFarApart(t *testing.T) {
	k, _ := LoadKey(writeKey(t, validKeyYAML))
	// 10-12 and 19-20: widened 7-15 and 16-23 do not overlap.
	k.Entries[0].Locations[0] = Location{Path: "ledger/transfer.go", StartLine: 10, EndLine: 12}
	k.Entries[1].Locations[0] = Location{Path: "ledger/transfer.go", StartLine: 19, EndLine: 20}
	if err := k.Validate(); err != nil {
		t.Fatal(err)
	}
	k.Entries[1].Locations[0] = Location{Path: "ledger/other.go", StartLine: 10, EndLine: 12}
	if err := k.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAgainstRejects(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*Key)
	}{
		{"missing path", "does not exist", func(k *Key) { k.Entries[0].Locations[0].Path = "ledger/nope.go" }},
		{"range past end", "beyond", func(k *Key) { k.Entries[1].Locations[0].EndLine = 99 }},
		{"anchor outside range", "anchor", func(k *Key) {
			k.Entries[0].Locations[0] = Location{Path: "ledger/transfer.go", StartLine: 1, EndLine: 3}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, err := LoadKey(writeKey(t, validKeyYAML))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&k)
			err = k.ValidateAgainst(validFiles)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}
