package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/testutil"
)

// makeFixture writes <root>/<dir>/key.yaml (with the fixture name set to
// name) and <root>/<dir>/repo/<files>.
func makeFixture(t *testing.T, root, dir, name string, files map[string]string) {
	t.Helper()
	key := strings.Replace(validKeyYAML, "fixture: ledger", "fixture: "+name, 1)
	all := map[string]string{"key.yaml": key}
	for p, c := range files {
		all["repo/"+p] = c
	}
	testutil.WriteFiles(t, filepath.Join(root, dir), all)
}

func TestLoadFixturesSelftest(t *testing.T) {
	// testdata also holds the benchmark fixtures dir, so select by name.
	fs, err := LoadFixtures("testdata", []string{"selftest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Name != "selftest" {
		t.Fatalf("got %+v", fs)
	}
	f := fs[0]
	kinds := map[Kind]int{}
	for _, e := range f.Key.Entries {
		kinds[e.Kind]++
	}
	if kinds[KindDefect] < 1 || kinds[KindDecoy] < 1 {
		t.Errorf("selftest needs a defect and a decoy: %v", kinds)
	}
	if _, ok := f.Files["counter/counter.go"]; !ok {
		t.Errorf("files: %v", f.Files)
	}
	if !strings.HasPrefix(f.SourceDigest, "sha256:") || len(f.SourceDigest) != len("sha256:")+64 || !strings.HasPrefix(f.KeyDigest, "sha256:") {
		t.Errorf("digests %q %q", f.SourceDigest, f.KeyDigest)
	}
	again, err := LoadFixtures("testdata", []string{"selftest"})
	if err != nil {
		t.Fatal(err)
	}
	if again[0].SourceDigest != f.SourceDigest || again[0].KeyDigest != f.KeyDigest {
		t.Error("digests are not stable across loads")
	}
}

func TestLoadFixturesDigests(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	makeFixture(t, a, "ledger", "ledger", validFiles)
	makeFixture(t, b, "ledger", "ledger", validFiles)
	fa, err := LoadFixtures(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := LoadFixtures(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fa[0].SourceDigest != fb[0].SourceDigest || fa[0].KeyDigest != fb[0].KeyDigest {
		t.Fatal("identical fixtures should have identical digests")
	}
	testutil.WriteFiles(t, filepath.Join(b, "ledger", "repo"), map[string]string{"README": "x\n"})
	fb, err = LoadFixtures(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fa[0].SourceDigest == fb[0].SourceDigest {
		t.Error("source digest should change when a file is added")
	}
	if fa[0].KeyDigest != fb[0].KeyDigest {
		t.Error("key digest should not depend on the source")
	}
}

func TestLoadFixturesFilterAndOrder(t *testing.T) {
	root := t.TempDir()
	makeFixture(t, root, "zeta", "zeta", validFiles)
	makeFixture(t, root, "alpha", "alpha", validFiles)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("not a fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := LoadFixtures(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Name != "alpha" || all[1].Name != "zeta" {
		t.Fatalf("want alpha, zeta; got %d fixtures", len(all))
	}
	one, err := LoadFixtures(root, []string{"zeta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].Name != "zeta" {
		t.Fatalf("filter: got %+v", one)
	}
	if _, err := LoadFixtures(root, []string{"zeta", "nope"}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown filter name: got %v", err)
	}
}

func TestLoadFixturesRejects(t *testing.T) {
	t.Run("name mismatch", func(t *testing.T) {
		root := t.TempDir()
		makeFixture(t, root, "other", "ledger", validFiles)
		if _, err := LoadFixtures(root, nil); err == nil || !strings.Contains(err.Error(), "other") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		makeFixture(t, root, "ledger", "ledger", validFiles)
		if err := os.Symlink("transfer.go", filepath.Join(root, "ledger", "repo", "ledger", "link.go")); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFixtures(root, nil); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("key does not fit source", func(t *testing.T) {
		root := t.TempDir()
		makeFixture(t, root, "ledger", "ledger", map[string]string{"ledger/transfer.go": "package ledger\n"})
		if _, err := LoadFixtures(root, nil); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("hint word in source", func(t *testing.T) {
		for _, tc := range []struct{ content, word string }{
			{"// FIXME: handle overflow\n", "FIXME"},
			{"// the Decoy branch\n", "Decoy"},
			{"see the answer key\n", "answer key"},
			{"x := 1 // XXX\n", "XXX"},
		} {
			root := t.TempDir()
			files := map[string]string{"ledger/notes.go": tc.content}
			for p, c := range validFiles {
				files[p] = c
			}
			makeFixture(t, root, "ledger", "ledger", files)
			_, err := LoadFixtures(root, nil)
			if err == nil || !strings.Contains(err.Error(), "ledger/notes.go") || !strings.Contains(err.Error(), tc.word) {
				t.Errorf("%q: want error naming the file and %q, got %v", tc.content, tc.word, err)
			}
		}
	})
	t.Run("ordinary words pass", func(t *testing.T) {
		root := t.TempDir()
		files := map[string]string{"ledger/notes.go": "func debug(bugReport string, xxxl int) {}\n// Bugsnag client\n"}
		for p, c := range validFiles {
			files[p] = c
		}
		makeFixture(t, root, "ledger", "ledger", files)
		if _, err := LoadFixtures(root, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing key", func(t *testing.T) {
		root := t.TempDir()
		testutil.WriteFiles(t, filepath.Join(root, "ledger"), map[string]string{"repo/a.go": "package a\n"})
		if _, err := LoadFixtures(root, nil); err == nil {
			t.Fatal("want error")
		}
	})
}
