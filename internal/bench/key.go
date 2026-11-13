// Package bench scores Sift's findings against benchmark answer keys: fixture
// repositories with planted defects and decoys (docs/benchmark.md, ADR 0008).
// Everything here is offline and deterministic; the live driver that runs
// audits over the fixtures builds on it.
package bench

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/olddognewflex/sift/internal/contract"
)

// KeyVersion is the supported answer key schema version.
const KeyVersion = 1

// Kind is what an answer key entry plants.
type Kind string

const (
	// KindDefect is a real problem a reviewer should report and Pruv should
	// accept.
	KindDefect Kind = "defect"
	// KindDecoy looks like a problem but is refutable from the source; Pruv
	// or the gate should reject it.
	KindDecoy Kind = "decoy"
	// KindKnown is a real issue that was not planted. Matching it is
	// neutral.
	KindKnown Kind = "known"
)

// Location is a line range in a fixture repository.
type Location struct {
	Path      string `yaml:"path" json:"path"`
	StartLine int    `yaml:"start_line" json:"start_line"`
	EndLine   int    `yaml:"end_line" json:"end_line"`
}

// Entry is one planted (or known) item in an answer key.
type Entry struct {
	ID        string            `yaml:"id" json:"id"`
	Kind      Kind              `yaml:"kind" json:"kind"`
	Category  contract.Category `yaml:"category" json:"category"`
	Title     string            `yaml:"title" json:"title"`
	Locations []Location        `yaml:"locations" json:"locations"`
	// Anchor is text copied verbatim from the file whose first occurrence
	// starts within Locations[0], so a key that drifts from its source
	// fails validation.
	Anchor string `yaml:"anchor" json:"anchor"`
	Notes  string `yaml:"notes" json:"notes"`
}

// Key is a fixture's answer key (key.yaml).
type Key struct {
	Version int     `yaml:"version" json:"version"`
	Fixture string  `yaml:"fixture" json:"fixture"`
	Entries []Entry `yaml:"entries" json:"entries"`
}

// LoadKey reads an answer key, decoding it strictly (unknown fields and
// more than one YAML document are rejected), and checks it with Validate.
// Checks against the fixture source (ValidateAgainst) are LoadFixtures'.
func LoadKey(path string) (Key, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Key{}, err
	}
	return parseKey(data, path)
}

func parseKey(data []byte, path string) (Key, error) {
	var k Key
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&k); err != nil {
		return Key{}, fmt.Errorf("key %s: %w", path, err)
	}
	if err := dec.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
		return Key{}, fmt.Errorf("key %s: more than one YAML document (%v)", path, err)
	}
	if err := k.Validate(); err != nil {
		return Key{}, fmt.Errorf("key %s: %w", path, err)
	}
	return k, nil
}

// Validate checks the key on its own, without the fixture source.
func (k Key) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if k.Version != KeyVersion {
		bad("version: unsupported %d (supported: %d)", k.Version, KeyVersion)
	}
	if strings.TrimSpace(k.Fixture) == "" {
		bad("fixture: required")
	}
	seen := map[string]bool{}
	for i, e := range k.Entries {
		at := fmt.Sprintf("entries[%d] (%s)", i, e.ID)
		switch {
		case strings.TrimSpace(e.ID) == "":
			bad("%s: id: required", at)
		case seen[e.ID]:
			bad("%s: duplicate id", at)
		}
		seen[e.ID] = true
		if !slices.Contains([]Kind{KindDefect, KindDecoy, KindKnown}, e.Kind) {
			bad("%s: kind: %q is not one of defect, decoy, known", at, e.Kind)
		}
		if !contract.Valid(e.Category) {
			bad("%s: category: %q is not a registered reviewer %v", at, e.Category, e.Category.Enum())
		}
		if strings.TrimSpace(e.Title) == "" {
			bad("%s: title: required", at)
		}
		if e.Kind == KindKnown && strings.TrimSpace(e.Notes) == "" {
			bad("%s: notes: required for kind known", at)
		}
		if strings.TrimSpace(e.Anchor) == "" {
			bad("%s: anchor: required", at)
		}
		if len(e.Locations) == 0 {
			bad("%s: at least one location is required", at)
		}
		for j, l := range e.Locations {
			switch {
			case !contract.ValidRelPath(l.Path):
				bad("%s: locations[%d]: path: %q is not a clean repository-relative path", at, j, l.Path)
			case l.StartLine < 1:
				bad("%s: locations[%d]: start_line must be at least 1", at, j)
			case l.EndLine < l.StartLine:
				bad("%s: locations[%d]: end_line %d is before start_line %d", at, j, l.EndLine, l.StartLine)
			}
		}
	}
	// Widened windows must not overlap, so a single-line observation can
	// match at most one entry.
	for i, a := range k.Entries {
		for _, b := range k.Entries[i+1:] {
			for _, la := range a.Locations {
				for _, lb := range b.Locations {
					if la.Path == lb.Path && la.StartLine-LineTolerance <= lb.EndLine+LineTolerance && lb.StartLine-LineTolerance <= la.EndLine+LineTolerance {
						bad("entries %s and %s: windows overlap in %s (lines %d-%d and %d-%d, widened by %d)", a.ID, b.ID, la.Path, la.StartLine, la.EndLine, lb.StartLine, lb.EndLine, LineTolerance)
					}
				}
			}
		}
	}
	return errors.Join(errs...)
}

// ValidateAgainst checks a key that passed Validate against the fixture
// source (repository-relative slash path to raw content): every location
// exists, and each anchor's first occurrence in its file starts within its
// entry's first location.
func (k Key) ValidateAgainst(files map[string]string) error {
	var errs []error
	for _, e := range k.Entries {
		for j, l := range e.Locations {
			content, ok := files[l.Path]
			if !ok {
				errs = append(errs, fmt.Errorf("%s: locations[%d]: %s does not exist in the fixture", e.ID, j, l.Path))
				continue
			}
			lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
			if l.StartLine < 1 || l.EndLine < l.StartLine || l.EndLine > len(lines) {
				errs = append(errs, fmt.Errorf("%s: locations[%d]: lines %d-%d are invalid or beyond the end of %s (%d lines)", e.ID, j, l.StartLine, l.EndLine, l.Path, len(lines)))
				continue
			}
			if j > 0 {
				continue
			}
			if i := strings.Index(content, e.Anchor); i < 0 {
				errs = append(errs, fmt.Errorf("%s: anchor %q does not occur in %s", e.ID, e.Anchor, l.Path))
			} else if at := strings.Count(content[:i], "\n") + 1; at < l.StartLine || at > l.EndLine {
				errs = append(errs, fmt.Errorf("%s: anchor %q: first occurrence in %s is at line %d, outside lines %d-%d", e.ID, e.Anchor, l.Path, at, l.StartLine, l.EndLine))
			}
		}
	}
	return errors.Join(errs...)
}
