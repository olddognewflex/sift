package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/testutil"
)

var ctx = context.Background()

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInspectClean(t *testing.T) {
	repo := testutil.GitRepo(t, map[string]string{"main.go": "package main\n"})
	st, err := Inspect(ctx, filepath.Join(repo))
	if err != nil {
		t.Fatal(err)
	}
	if st.Dirty() || len(st.Commit) != 40 || st.Branch == "" {
		t.Fatalf("state = %+v", st)
	}
}

// With -z, git writes a rename as "R  new\0old\0". The original path belongs
// to the rename entry; it is not an entry of its own.
func TestInspectPairsRenamesWithOriginalPath(t *testing.T) {
	repo := testutil.GitRepo(t, map[string]string{"a.go": "package a\n\nconst A = 1\n", "c.go": "package c\n", "d.go": "package d\n\nconst D = 4\n"})
	testutil.Git(t, repo, "mv", "a.go", "b.go") // staged rename: "R "
	testutil.WriteFiles(t, repo, map[string]string{"c.go": "package c\n\n// changed\n"})
	if err := os.Rename(filepath.Join(repo, "d.go"), filepath.Join(repo, "e.go")); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repo, "add", "-N", "e.go") // worktree rename: " R"
	st, err := Inspect(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"R  a.go -> b.go", " M c.go", " R d.go -> e.go"}
	if strings.Join(st.Porcelain, "|") != strings.Join(want, "|") {
		t.Fatalf("porcelain = %q, want %q", st.Porcelain, want)
	}
}

func TestInspectErrors(t *testing.T) {
	if _, err := Inspect(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not inside a Git repository") {
		t.Fatalf("non-git dir: %v", err)
	}
	empty := t.TempDir()
	testutil.Git(t, empty, "init", "-q")
	if _, err := Inspect(ctx, empty); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Fatalf("empty repo: %v", err)
	}
	if _, err := Inspect(ctx, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing dir must fail")
	}
}

func dirtyRepo(t *testing.T) (string, State) {
	t.Helper()
	repo := testutil.GitRepo(t, map[string]string{"a.go": "committed\n", "b.go": "keep\n", "gone.go": "x\n"})
	testutil.WriteFiles(t, repo, map[string]string{"a.go": "modified\n", "new.go": "untracked\n"})
	if err := os.Remove(filepath.Join(repo, "gone.go")); err != nil {
		t.Fatal(err)
	}
	st, err := Inspect(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Dirty() {
		t.Fatal("expected dirty")
	}
	return repo, st
}

func TestExportRefusesDirty(t *testing.T) {
	_, st := dirtyRepo(t)
	dest := filepath.Join(t.TempDir(), "snap")
	_, err := Export(ctx, st, contract.DirtyRefuse, dest)
	if !errors.Is(err, ErrDirty) || !strings.Contains(err.Error(), "--dirty=include") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("snapshot created despite refusal")
	}
}

func TestExportHeadIgnoresWorkingTree(t *testing.T) {
	repo, st := dirtyRepo(t)
	before, _ := Fingerprint(ctx, repo)
	dest := filepath.Join(t.TempDir(), "snap")
	snap, err := Export(ctx, st, contract.DirtyHead, dest)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dest, "a.go")) != "committed\n" || read(t, filepath.Join(dest, "gone.go")) != "x\n" {
		t.Error("head snapshot must contain committed content")
	}
	if _, err := os.Stat(filepath.Join(dest, "new.go")); err == nil {
		t.Error("untracked file leaked into head snapshot")
	}
	if snap.Files != 3 || snap.Diff != nil || Revision(st, contract.DirtyHead, snap) != st.Commit {
		t.Errorf("snap = %+v", snap)
	}
	fi, _ := os.Stat(filepath.Join(dest, "a.go"))
	if fi.Mode().Perm()&0o222 != 0 {
		t.Errorf("snapshot file is writable: %v", fi.Mode())
	}
	after, _ := Fingerprint(ctx, repo)
	if before != after {
		t.Error("export modified the repository")
	}
}

func TestExportIncludeWorkingTree(t *testing.T) {
	_, st := dirtyRepo(t)
	dest := filepath.Join(t.TempDir(), "snap")
	snap, err := Export(ctx, st, contract.DirtyInclude, dest)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dest, "a.go")) != "modified\n" || read(t, filepath.Join(dest, "new.go")) != "untracked\n" {
		t.Error("include snapshot must contain working-tree content")
	}
	if _, err := os.Stat(filepath.Join(dest, "gone.go")); err == nil {
		t.Error("deleted file present in include snapshot")
	}
	if !strings.Contains(string(snap.Diff), "+modified") {
		t.Errorf("diff = %q", snap.Diff)
	}
	rev := Revision(st, contract.DirtyInclude, snap)
	if !strings.HasPrefix(rev, st.Commit+"+worktree.") {
		t.Errorf("revision = %q", rev)
	}
}

func TestExportDigestIsContentAddressed(t *testing.T) {
	repo := testutil.GitRepo(t, map[string]string{"a.go": "a\n", "dir/b.go": "b\n"})
	st, _ := Inspect(ctx, repo)
	s1, err := Export(ctx, st, contract.DirtyRefuse, filepath.Join(t.TempDir(), "1"))
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := Export(ctx, st, contract.DirtyRefuse, filepath.Join(t.TempDir(), "2"))
	if s1.Digest != s2.Digest || len(s1.Digest) != 64 {
		t.Fatalf("digests %q %q", s1.Digest, s2.Digest)
	}
}

func TestExportSkipsSymlinksAndIgnoresExportAttributes(t *testing.T) {
	repo := testutil.GitRepo(t, map[string]string{
		"keep.go":        "$Format:%H$\n",
		"hidden.go":      "secret but audited\n",
		".gitattributes": "keep.go export-subst\nhidden.go export-ignore\n",
	})
	if err := os.Symlink("/etc/passwd", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repo, "add", "link")
	testutil.Git(t, repo, "commit", "-q", "-m", "link")
	st, _ := Inspect(ctx, repo)
	dest := filepath.Join(t.TempDir(), "snap")
	snap, err := Export(ctx, st, contract.DirtyRefuse, dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "link")); err == nil {
		t.Error("symlink exported")
	}
	if len(snap.Skipped) != 1 || !strings.Contains(snap.Skipped[0], "symlink") {
		t.Errorf("skipped = %v", snap.Skipped)
	}
	if read(t, filepath.Join(dest, "keep.go")) != "$Format:%H$\n" {
		t.Error("export-subst altered audited content")
	}
	if read(t, filepath.Join(dest, "hidden.go")) != "secret but audited\n" {
		t.Error("export-ignore dropped audited content")
	}
}

func TestFingerprintSeesUntrackedContent(t *testing.T) {
	repo := testutil.GitRepo(t, map[string]string{"a.go": "a\n"})
	testutil.WriteFiles(t, repo, map[string]string{"new.go": "one\n"})
	before, err := Fingerprint(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFiles(t, repo, map[string]string{"new.go": "two\n"})
	if after, _ := Fingerprint(ctx, repo); after == before {
		t.Fatal("changing an untracked file's content must change the fingerprint")
	}
}

// commitBlobs commits path->content through the index only, so the commit can
// hold paths that the local filesystem cannot represent side by side.
func commitBlobs(t *testing.T, repo string, files [][2]string) {
	t.Helper()
	for _, f := range files {
		blob := filepath.Join(t.TempDir(), "blob")
		if err := os.WriteFile(blob, []byte(f[1]), 0o644); err != nil {
			t.Fatal(err)
		}
		oid := strings.TrimSpace(testutil.Git(t, repo, "hash-object", "-w", blob))
		testutil.Git(t, repo, "update-index", "--add", "--cacheinfo", "100644,"+oid+","+f[0])
	}
	testutil.Git(t, repo, "commit", "-q", "-m", "colliding paths")
	testutil.Git(t, repo, "reset", "-q", "--hard")
}

func TestExportSkipsFilesystemPathCollisions(t *testing.T) {
	repo := testutil.GitRepo(t, map[string]string{"main.go": "package main\n"})
	// ls-tree sorts by byte, so the upper-case path of each pair comes first.
	commitBlobs(t, repo, [][2]string{
		{"A.txt", "upper\n"}, {"a.txt", "lower\n"},
		{"Dir", "file\n"}, {"dir/x.txt", "nested\n"},
		{"Sub/a.txt", "first\n"}, {"sub/a.txt", "second\n"}, {"sub/b.txt", "own dir spelling\n"},
	})
	st, err := Inspect(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "snap")
	snap, err := Export(ctx, st, contract.DirtyHead, dest)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if read(t, filepath.Join(dest, "A.txt")) != "upper\n" || read(t, filepath.Join(dest, "Dir")) != "file\n" {
		t.Error("first path of a collision must keep its own content")
	}
	upper, err := os.Stat(filepath.Join(dest, "A.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lower, err := os.Stat(filepath.Join(dest, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dest, "Sub/a.txt")) != "first\n" {
		t.Error("first path of a directory collision must keep its own content")
	}
	if os.SameFile(upper, lower) { // case-insensitive filesystem
		want := []string{"a.txt (" + collision + ")", "dir/x.txt (" + collision + ")", "sub/a.txt (" + collision + ")", "sub/b.txt (" + collision + ")"}
		if strings.Join(snap.Skipped, "|") != strings.Join(want, "|") || snap.Files != 4 {
			t.Errorf("skipped = %v, files = %d", snap.Skipped, snap.Files)
		}
		return
	}
	if read(t, filepath.Join(dest, "a.txt")) != "lower\n" || read(t, filepath.Join(dest, "dir/x.txt")) != "nested\n" {
		t.Error("case-sensitive filesystem must export every path")
	}
	if len(snap.Skipped) != 0 || snap.Files != 8 {
		t.Errorf("skipped = %v, files = %d", snap.Skipped, snap.Files)
	}
}

func TestExportIncludeSkipsPathsSharingOneFile(t *testing.T) {
	// With core.ignorecase unset, Git also lists Sub/b.txt, the on-disk
	// spelling of tracked sub/b.txt, as untracked.
	for _, ignorecase := range []string{"true", "false"} {
		t.Run("ignorecase="+ignorecase, func(t *testing.T) {
			repo := testutil.GitRepo(t, map[string]string{"main.go": "package main\n"})
			testutil.Git(t, repo, "config", "core.ignorecase", ignorecase)
			commitBlobs(t, repo, [][2]string{
				{"A.txt", "upper\n"}, {"a.txt", "lower\n"},
				{"Sub/a.txt", "first\n"}, {"sub/a.txt", "second\n"}, {"sub/b.txt", "own\n"},
			})
			testutil.WriteFiles(t, repo, map[string]string{"new.go": "untracked\n"}) // dirty on any filesystem
			st, err := Inspect(ctx, repo)
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "snap")
			snap, err := Export(ctx, st, contract.DirtyInclude, dest)
			if err != nil {
				t.Fatalf("export: %v", err)
			}
			if read(t, filepath.Join(dest, "new.go")) != "untracked\n" {
				t.Error("unrelated files must still be exported")
			}
			upper, _ := os.Lstat(filepath.Join(repo, "A.txt"))
			lower, _ := os.Lstat(filepath.Join(repo, "a.txt"))
			if upper != nil && lower != nil && os.SameFile(upper, lower) { // case-insensitive filesystem
				var want []string
				for _, p := range []string{"A.txt", "Sub/a.txt", "a.txt", "sub/a.txt"} {
					want = append(want, p+" ("+ambiguous+")")
				}
				if ignorecase == "false" {
					want = append(want, "Sub/b.txt (untracked alias of tracked path sub/b.txt)")
				}
				got := append([]string(nil), snap.Skipped...)
				sort.Strings(got)
				sort.Strings(want)
				if strings.Join(got, "|") != strings.Join(want, "|") || snap.Files != 3 {
					t.Errorf("skipped = %v, files = %d", snap.Skipped, snap.Files)
				}
				for _, p := range []string{"A.txt", "sub/a.txt"} {
					if ok, _ := exists(filepath.Join(dest, p)); ok {
						t.Errorf("content of ambiguous path %s must not be exported", p)
					}
				}
				if read(t, filepath.Join(dest, "sub/b.txt")) != "own\n" {
					t.Error("a tracked path with only untracked aliases keeps its content")
				}
				return
			}
			if read(t, filepath.Join(dest, "A.txt")) != "upper\n" || read(t, filepath.Join(dest, "a.txt")) != "lower\n" ||
				read(t, filepath.Join(dest, "Sub/a.txt")) != "first\n" || read(t, filepath.Join(dest, "sub/a.txt")) != "second\n" {
				t.Error("case-sensitive filesystem must export each path with its own content")
			}
			if len(snap.Skipped) != 0 || snap.Files != 7 {
				t.Errorf("skipped = %v, files = %d", snap.Skipped, snap.Files)
			}
		})
	}
}

// Hard links reach the same-file check on any filesystem.
func TestExportIncludeSkipsHardLinkedPaths(t *testing.T) {
	repo := testutil.GitRepo(t, map[string]string{"main.go": "package main\n"})
	testutil.WriteFiles(t, repo, map[string]string{"one.txt": "shared\n"})
	if err := os.Link(filepath.Join(repo, "one.txt"), filepath.Join(repo, "two.txt")); err != nil {
		t.Skipf("hard links unsupported: %v", err)
	}
	st, err := Inspect(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := Export(ctx, st, contract.DirtyInclude, filepath.Join(t.TempDir(), "snap"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"one.txt (" + ambiguous + ")", "two.txt (" + ambiguous + ")"}
	if strings.Join(snap.Skipped, "|") != strings.Join(want, "|") || snap.Files != 1 {
		t.Errorf("skipped = %v, files = %d", snap.Skipped, snap.Files)
	}
}
