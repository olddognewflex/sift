// Package source inspects the target Git repository and exports the audited
// content into an isolated, read-only snapshot.
//
// Sift never runs agents against the live repository. It runs them against a
// snapshot under the run directory, so the audited content is fixed for the
// whole run, evidence line numbers refer to a recorded revision, and the
// target repository cannot be modified through the agents' working
// directory. Git is invoked with GIT_OPTIONAL_LOCKS=0 so that inspection does
// not rewrite the repository's index.
package source

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/olddognewflex/sift/internal/contract"
)

// ErrDirty is returned when the repository has uncommitted changes and the
// dirty policy is refuse.
var ErrDirty = errors.New("working tree has uncommitted changes")

// State describes a repository at inspection time.
type State struct {
	Root      string
	Commit    string
	Branch    string
	Porcelain []string
}

func (s State) Dirty() bool { return len(s.Porcelain) > 0 }

// Snapshot describes exported audit content.
type Snapshot struct {
	Files   int
	Digest  string
	Skipped []string
	// Diff is `git diff --binary HEAD` when uncommitted content was included.
	Diff []byte
}

// Inspect resolves the repository containing path and records its state.
func Inspect(ctx context.Context, path string) (State, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return State{}, err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return State{}, fmt.Errorf("%s is not a directory", abs)
	}
	root, err := git(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return State{}, fmt.Errorf("%s is not inside a Git repository (Sift needs Git to record the audited revision): %w", abs, err)
	}
	st := State{Root: strings.TrimSpace(string(root))}
	commit, err := git(ctx, st.Root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return State{}, fmt.Errorf("repository %s has no commits to audit: %w", st.Root, err)
	}
	st.Commit = strings.TrimSpace(string(commit))
	if b, err := git(ctx, st.Root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		st.Branch = strings.TrimSpace(string(b))
	}
	status, err := git(ctx, st.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return State{}, err
	}
	st.Porcelain = parsePorcelainZ(string(status))
	return st, nil
}

// parsePorcelainZ splits `git status --porcelain=v1 -z` output into one
// entry per path. With -z, a rename or copy (R or C in either status column)
// is followed by its original path as a separate field; that field is folded
// into the entry as "XY ORIG -> PATH", the form git uses without -z.
func parsePorcelainZ(out string) []string {
	var entries []string
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		e := fields[i]
		if e == "" {
			continue
		}
		if len(e) > 3 && strings.ContainsAny(e[:2], "RC") && i+1 < len(fields) {
			i++
			e = e[:3] + fields[i] + " -> " + e[3:]
		}
		entries = append(entries, e)
	}
	return entries
}

// Fingerprint summarizes the repository's HEAD, status, uncommitted diff, and
// the content of untracked files (which the diff omits). Comparing
// fingerprints before and after an audit detects modification.
func Fingerprint(ctx context.Context, root string) (string, error) {
	h := sha256.New()
	for _, args := range [][]string{
		{"rev-parse", "HEAD"},
		{"status", "--porcelain=v1", "-z", "--untracked-files=all"},
		{"diff", "--binary", "HEAD"},
	} {
		out, err := git(ctx, root, args...)
		if err != nil {
			return "", err
		}
		h.Write(out)
		h.Write([]byte{0})
	}
	untracked, err := git(ctx, root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return "", err
	}
	for _, rel := range strings.Split(string(untracked), "\x00") {
		if rel == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			data = []byte("unreadable: " + err.Error())
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(h, "%s\x00%x\n", rel, sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Export writes the content selected by policy into dest, which must not
// exist yet. Files are written read-only.
func Export(ctx context.Context, st State, policy contract.DirtyPolicy, dest string) (Snapshot, error) {
	if st.Dirty() && policy == contract.DirtyRefuse {
		return Snapshot{}, DirtyError(st)
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		return Snapshot{}, fmt.Errorf("create snapshot directory: %w", err)
	}
	w := &writer{dest: dest, sums: map[string]string{}, dirs: map[string]bool{}}
	var err error
	if st.Dirty() && policy == contract.DirtyInclude {
		err = w.exportWorkingTree(ctx, st.Root)
	} else {
		err = w.exportCommit(ctx, st.Root, st.Commit)
	}
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Files: len(w.sums), Digest: w.digest(), Skipped: w.skipped}
	if st.Dirty() && policy == contract.DirtyInclude {
		if snap.Diff, err = git(ctx, st.Root, "diff", "--binary", "HEAD"); err != nil {
			return Snapshot{}, err
		}
	}
	return snap, nil
}

// DirtyError explains a refused dirty working tree and how to proceed.
func DirtyError(st State) error {
	return fmt.Errorf("%w (%d path(s), e.g. %q); commit or stash them, or rerun with --dirty=head (audit %s only) or --dirty=include (audit the working tree as it is)",
		ErrDirty, len(st.Porcelain), st.Porcelain[0], short(st.Commit))
}

// Revision returns the source revision label recorded on findings: the
// commit, plus the snapshot digest when uncommitted content is included.
func Revision(st State, policy contract.DirtyPolicy, snap Snapshot) string {
	if st.Dirty() && policy == contract.DirtyInclude {
		return st.Commit + "+worktree." + snap.Digest[:12]
	}
	return st.Commit
}

type writer struct {
	dest    string
	sums    map[string]string
	dirs    map[string]bool // directories created, by exact spelling
	skipped []string
}

const collision = "collides with an earlier path in the snapshot, e.g. on a case-insensitive filesystem"

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (w *writer) skip(path, why string) { w.skipped = append(w.skipped, path+" ("+why+")") }

func (w *writer) write(rel string, data []byte) error {
	if !contract.ValidRelPath(rel) {
		w.skip(rel, "unsafe path")
		return nil
	}
	// Git lists each path once, so anything already on disk at this path or
	// at one of its parent directories, other than a directory this writer
	// created under exactly that spelling, means the filesystem folded two
	// distinct paths together (README.md and readme.md, or Sub/ and sub/, on
	// case-insensitive APFS). Keep the first path: writing the later one
	// would fail on the read-only file, replace its content, or store it
	// under the other path's spelling.
	parents := strings.Split(rel, "/")
	parents = parents[:len(parents)-1]
	for i := range parents {
		dir := strings.Join(parents[:i+1], "/")
		if w.dirs[dir] {
			continue
		}
		if taken, err := exists(filepath.Join(w.dest, filepath.FromSlash(dir))); err != nil {
			return err
		} else if taken {
			w.skip(rel, collision)
			return nil
		}
		break // the rest of the chain does not exist yet either
	}
	full := filepath.Join(w.dest, filepath.FromSlash(rel))
	if taken, err := exists(full); err != nil {
		return err
	} else if taken {
		w.skip(rel, collision)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	for i := range parents {
		w.dirs[strings.Join(parents[:i+1], "/")] = true
	}
	if err := os.WriteFile(full, data, 0o444); err != nil {
		return fmt.Errorf("snapshot %s: %w", rel, err)
	}
	sum := sha256.Sum256(data)
	w.sums[rel] = hex.EncodeToString(sum[:])
	return nil
}

func (w *writer) digest() string {
	paths := make([]string, 0, len(w.sums))
	for p := range w.sums {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s\x00%s\n", p, w.sums[p])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// exportCommit writes the blobs of commit, read directly from the object
// database. (git archive is avoided because export-ignore and export-subst
// attributes would alter the audited content.)
func (w *writer) exportCommit(ctx context.Context, root, commit string) error {
	tree, err := git(ctx, root, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return err
	}
	type blob struct{ oid, path string }
	var blobs []blob
	for _, e := range strings.Split(string(tree), "\x00") {
		if e == "" {
			continue
		}
		meta, path, ok := strings.Cut(e, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return fmt.Errorf("unexpected ls-tree entry %q", e)
		}
		switch {
		case f[0] == "120000":
			w.skip(path, "symlink")
		case f[1] == "commit":
			w.skip(path, "submodule")
		case f[1] == "blob":
			blobs = append(blobs, blob{f[2], path})
		}
	}
	if len(blobs) == 0 {
		return nil
	}
	cmd := gitCmd(ctx, root, "cat-file", "--batch")
	var req bytes.Buffer
	for _, b := range blobs {
		req.WriteString(b.oid + "\n")
	}
	cmd.Stdin = &req
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	r := bufio.NewReader(out)
	for _, b := range blobs {
		data, err := readBatchObject(r)
		if err == nil {
			err = w.write(b.path, data)
		}
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("export %s: %w", b.path, err)
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git cat-file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func readBatchObject(r *bufio.Reader) ([]byte, error) {
	header, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	f := strings.Fields(header)
	if len(f) != 3 || f[1] != "blob" {
		return nil, fmt.Errorf("unexpected cat-file header %q", strings.TrimSpace(header))
	}
	size, err := strconv.Atoi(f[2])
	if err != nil {
		return nil, err
	}
	data := make([]byte, size+1) // content plus trailing newline
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	return data[:size], nil
}

const ambiguous = "resolves to the same file as another listed path, e.g. on a case-insensitive filesystem"

// exportWorkingTree copies tracked and untracked, non-ignored files as they
// exist on disk.
func (w *writer) exportWorkingTree(ctx context.Context, root string) error {
	cached, err := git(ctx, root, "ls-files", "-z", "--cached", "--deduplicate")
	if err != nil {
		return err
	}
	others, err := git(ctx, root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	type listed struct {
		rel     string
		tracked bool
	}
	var list []listed
	for _, rel := range strings.Split(string(cached), "\x00") {
		list = append(list, listed{rel, true})
	}
	for _, rel := range strings.Split(string(others), "\x00") {
		list = append(list, listed{rel, false})
	}
	type entry struct {
		rel, full, skip string
		tracked         bool
	}
	var entries []entry
	var regular []os.FileInfo
	var at []int // index in entries of each regular file
	for _, l := range list {
		if l.rel == "" {
			continue
		}
		e := entry{rel: l.rel, full: filepath.Join(root, filepath.FromSlash(l.rel)), tracked: l.tracked}
		fi, err := os.Lstat(e.full)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue // deleted in the working tree
		case err != nil:
			return err
		case fi.Mode()&os.ModeSymlink != 0:
			e.skip = "symlink"
		case fi.IsDir():
			e.skip = "submodule"
		case !fi.Mode().IsRegular():
			e.skip = "not a regular file"
		default:
			regular = append(regular, fi)
			at = append(at, len(entries))
		}
		entries = append(entries, e)
	}
	// Git lists A.txt and a.txt separately, but on a case-insensitive
	// filesystem both names open one file holding whichever blob was checked
	// out last, so its content cannot be attributed to either path. With
	// core.ignorecase unset, Git also lists the on-disk spelling of a folded
	// path as untracked; that alias is dropped and the tracked path kept.
	// Hard links are caught too and treated the same way on purpose: a
	// path-based check would miss aliases such as NFC/NFD spellings on APFS,
	// and Git never checks out hard links.
	for _, g := range sameFileGroups(regular) {
		var tracked []*entry
		for _, i := range g {
			if e := &entries[at[i]]; e.tracked {
				tracked = append(tracked, e)
			}
		}
		for _, i := range g {
			switch e := &entries[at[i]]; {
			case len(tracked) != 1:
				e.skip = ambiguous
			case e != tracked[0]:
				e.skip = "untracked alias of tracked path " + tracked[0].rel
			}
		}
	}
	for _, e := range entries {
		if e.skip != "" {
			w.skip(e.rel, e.skip)
			continue
		}
		data, err := os.ReadFile(e.full)
		if err != nil {
			return err
		}
		if err := w.write(e.rel, data); err != nil {
			return err
		}
	}
	return nil
}

// sameFileGroups returns, as indexes into files, each set of two or more
// entries that are the same on-disk file. Files are bucketed by device and
// inode where the platform exposes them, so the pairwise os.SameFile scan
// only runs within a bucket of real aliases. Elsewhere they are bucketed by
// size, since only files of equal size can be the same file.
func sameFileGroups(files []os.FileInfo) [][]int {
	buckets := map[any][]int{}
	for i, fi := range files {
		k, ok := fileKey(fi)
		if !ok {
			k = fi.Size()
		}
		buckets[k] = append(buckets[k], i)
	}
	var groups [][]int
	for _, b := range buckets {
		grouped := map[int]bool{}
		for x, i := range b {
			if grouped[i] {
				continue
			}
			g := []int{i}
			for _, j := range b[x+1:] {
				if !grouped[j] && os.SameFile(files[i], files[j]) {
					g = append(g, j)
					grouped[j] = true
				}
			}
			if len(g) > 1 {
				groups = append(groups, g)
			}
		}
	}
	return groups
}

func gitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=off"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	return cmd
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := gitCmd(ctx, dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
