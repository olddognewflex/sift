package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

// hintWords must not appear in fixture source, where they would tell a
// reviewer what was planted. BUG, FIXME and XXX are matched in capitals
// only, so ordinary code (debug, bugReport, Bugsnag) passes.
var hintWords = regexp.MustCompile(`\b(?:(?i:planted|decoys?|answer[ _-]?keys?)|BUG|FIXME|XXX)\b`)

// Fixture is a benchmark fixture: a source tree and its answer key. The
// driver materializes Files into a git repository at test time.
type Fixture struct {
	Name string
	Key  Key
	// Files maps repository-relative slash paths to content.
	Files map[string]string
	// SourceDigest covers every file path and content; KeyDigest covers
	// the key.yaml bytes. Both are "sha256:<hex>".
	SourceDigest string
	KeyDigest    string
}

// LoadFixtures loads every fixture under dir, one per subdirectory
// <name>/ holding key.yaml and repo/. A non-empty filter selects fixtures
// by name; naming a fixture that does not exist is an error. Fixtures are
// returned sorted by name.
func LoadFixtures(dir string, filter []string) ([]Fixture, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	for _, f := range filter {
		if !slices.Contains(names, f) {
			return nil, fmt.Errorf("fixture %q not found in %s (have %v)", f, dir, names)
		}
	}
	var out []Fixture
	for _, name := range names { // ReadDir sorts by name
		if len(filter) > 0 && !slices.Contains(filter, name) {
			continue
		}
		f, err := loadFixture(filepath.Join(dir, name), name)
		if err != nil {
			return nil, fmt.Errorf("fixture %s: %w", name, err)
		}
		out = append(out, f)
	}
	return out, nil
}

func loadFixture(dir, name string) (Fixture, error) {
	keyPath := filepath.Join(dir, "key.yaml")
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return Fixture{}, err
	}
	key, err := parseKey(data, keyPath)
	if err != nil {
		return Fixture{}, err
	}
	if key.Fixture != name {
		return Fixture{}, fmt.Errorf("key names fixture %q, but its directory is %q", key.Fixture, name)
	}
	files, err := readTree(filepath.Join(dir, "repo"))
	if err != nil {
		return Fixture{}, err
	}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		if w := hintWords.FindString(files[p]); w != "" {
			return Fixture{}, fmt.Errorf("repo/%s: contains %q; fixture source must not hint at what is planted", p, w)
		}
	}
	if err := key.ValidateAgainst(files); err != nil {
		return Fixture{}, err
	}
	keySum := sha256.Sum256(data)
	return Fixture{
		Name: name, Key: key, Files: files,
		SourceDigest: sourceDigest(files),
		KeyDigest:    "sha256:" + hex.EncodeToString(keySum[:]),
	}, nil
}

// readTree reads every regular file under root, keyed by slash path
// relative to root. Symlinks and other special files are rejected so the
// fixture is exactly what is committed.
func readTree(root string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file (symlinks and special files are not allowed)", filepath.ToSlash(rel))
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err == nil && len(files) == 0 {
		err = fmt.Errorf("%s: no files", root)
	}
	return files, err
}

// sourceDigest hashes the sorted (path, content) pairs, each length
// prefixed so no two trees share an encoding.
func sourceDigest(files map[string]string) string {
	h := sha256.New()
	for _, p := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(h, "%d:%s\n%d:", len(p), p, len(files[p]))
		h.Write([]byte(files[p]))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
