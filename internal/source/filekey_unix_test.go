//go:build unix

package source

import (
	"os"
	"path/filepath"
	"testing"
)

// Equal-size distinct files must land in separate buckets so grouping
// never falls back to an all-pairs scan on Unix.
func TestSameFileGroupsBucketsByInode(t *testing.T) {
	dir := t.TempDir()
	var files []os.FileInfo
	for _, name := range []string{"a", "b", "c"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := fileKey(fi); !ok {
			t.Fatalf("fileKey(%s): no identity", name)
		}
		files = append(files, fi)
	}
	link := filepath.Join(dir, "a-link")
	if err := os.Link(filepath.Join(dir, "a"), link); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, fi)

	groups := sameFileGroups(files)
	if len(groups) != 1 || len(groups[0]) != 2 || groups[0][0] != 0 || groups[0][1] != 3 {
		t.Fatalf("groups = %v, want [[0 3]]", groups)
	}
}
