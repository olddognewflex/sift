package storage

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olddognewflex/sift/internal/contract"
)

func TestRunIDs(t *testing.T) {
	now := time.Date(2026, 10, 27, 15, 30, 12, 0, time.UTC)
	id, err := NewRunID(now)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^20261027T153012Z-[0-9a-f]{6}$`).MatchString(id) {
		t.Fatalf("id = %q", id)
	}
	other, _ := NewRunID(now)
	if other == id {
		t.Fatal("run IDs must be unique")
	}
}

func manifest(id string) *contract.Manifest {
	return &contract.Manifest{
		Header: contract.NewHeader(contract.KindManifest, id), Status: contract.RunRunning,
		Repository: contract.Repository{Path: "/r", Commit: "c", SourceRevision: "c", DirtyPolicy: contract.DirtyRefuse},
	}
}

func TestStoreLifecycle(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "runs")}
	if _, err := s.Latest(); !errors.Is(err, ErrNoRuns) {
		t.Fatalf("empty store: %v", err)
	}
	ids := []string{"20261027T100000Z-aaaaaa", "20261027T110000Z-bbbbbb"}
	for _, id := range ids {
		r, err := s.Create(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.WriteJSON(ManifestFile, manifest(id)); err != nil {
			t.Fatal(err)
		}
	}
	// A directory without a manifest is not a run.
	if err := os.Mkdir(filepath.Join(s.Root, "20261027T120000Z-cccccc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ids[0]); err == nil {
		t.Fatal("creating an existing run must fail")
	}
	latest, err := s.Latest()
	if err != nil || latest.ID != ids[1] {
		t.Fatalf("latest = %v, %v", latest, err)
	}
	r, err := s.Resolve(ids[0])
	if err != nil || r.ID != ids[0] {
		t.Fatalf("resolve = %v, %v", r, err)
	}
	m, err := r.Manifest()
	if err != nil || m.RunID != ids[0] {
		t.Fatalf("manifest = %v, %v", m, err)
	}
	for _, bad := range []string{"../etc", "nope", "20261027T100000Z-zzzzzz"} {
		if _, err := s.Open(bad); err == nil {
			t.Errorf("Open(%q) should fail", bad)
		}
	}
}

func TestWriteJSONRefusesInvalid(t *testing.T) {
	s := Store{Root: t.TempDir()}
	r, _ := s.Create("20261027T100000Z-aaaaaa")
	m := manifest(r.ID)
	m.Status = "exploded"
	if err := r.WriteJSON(ManifestFile, m); err == nil {
		t.Fatal("invalid artifact was written")
	}
	if r.Exists(ManifestFile) {
		t.Fatal("file exists after refused write")
	}
}

func TestReadJSONErrors(t *testing.T) {
	s := Store{Root: t.TempDir()}
	r, _ := s.Create("20261027T100000Z-aaaaaa")
	var d contract.Discovery
	err := r.ReadJSON(DiscoveryFile, &d)
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing artifact: %v", err)
	}
	if err := r.WriteFile(DiscoveryFile, []byte(`{"kind":"discovery","schema_version":1,"run_id":"x","extra":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.ReadJSON(DiscoveryFile, &d); err == nil || !strings.Contains(err.Error(), DiscoveryFile) {
		t.Fatalf("invalid artifact: %v", err)
	}
}

func TestWriteAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a", "b.json")
	for _, content := range []string{"one", "two"} {
		if err := WriteAtomic(p, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(p)
	entries, _ := os.ReadDir(filepath.Dir(p))
	if string(got) != "two" || len(entries) != 1 {
		t.Fatalf("content %q, entries %v", got, entries)
	}
}
