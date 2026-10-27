package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
