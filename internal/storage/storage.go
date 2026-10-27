// Package storage persists audit runs on the local filesystem.
//
// Each run lives in its own directory under the runs root. Every write is
// atomic (temp file + rename), so a run interrupted at any point leaves only
// complete artifacts behind.
package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/olddognewflex/sift/internal/contract"
)

// Well-known run-relative paths.
const (
	ManifestFile    = "manifest.json"
	SourceDir       = "source"
	SourceDiffFile  = "source.diff"
	DiscoveryFile   = "artifacts/discovery.json"
	ReviewPlanFile  = "artifacts/review-plan.json"
	CandidatesFile  = "artifacts/candidates.json"
	DecisionsFile   = "artifacts/decisions.json"
	FindingsFile    = "artifacts/findings.json"
	ReportDir       = "report"
	ReportMetaFile  = "report/report.json"
	AgentsDir       = "agents"
	ExecutionsDir   = "executions"
	ReviewFilePattn = "artifacts/review-%s.json"
)

// ErrNoRuns is returned by Latest when the runs directory has no runs.
var ErrNoRuns = errors.New("no audit runs found")

var runIDPattern = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{6}$`)

// Store is a runs root directory.
type Store struct{ Root string }

// Run is one audit run directory.
type Run struct {
	ID  string
	Dir string
}

// NewRunID returns a sortable, unique run ID such as 20261027T153012Z-a1b2c3.
func NewRunID(now time.Time) (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b), nil
}

// Create makes a new run directory. It fails if the run already exists.
func (s Store) Create(id string) (*Run, error) {
	if !runIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid run id %q", id)
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return nil, fmt.Errorf("create runs directory: %w", err)
	}
	dir := filepath.Join(s.Root, id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create run directory: %w", err)
	}
	return &Run{ID: id, Dir: dir}, nil
}

// Open returns an existing run.
func (s Store) Open(id string) (*Run, error) {
	if !runIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid run id %q", id)
	}
	dir := filepath.Join(s.Root, id)
	if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err != nil {
		return nil, fmt.Errorf("run %s not found in %s", id, s.Root)
	}
	return &Run{ID: id, Dir: dir}, nil
}

// List returns run IDs in chronological order.
func (s Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && runIDPattern.MatchString(e.Name()) {
			if _, err := os.Stat(filepath.Join(s.Root, e.Name(), ManifestFile)); err == nil {
				ids = append(ids, e.Name())
			}
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// Latest returns the most recent run.
func (s Store) Latest() (*Run, error) {
	ids, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w in %s", ErrNoRuns, s.Root)
	}
	return s.Open(ids[len(ids)-1])
}

// Resolve opens id, or the latest run when id is empty.
func (s Store) Resolve(id string) (*Run, error) {
	if id == "" {
		return s.Latest()
	}
	return s.Open(id)
}

// Path returns the absolute path of a run-relative path.
func (r *Run) Path(rel string) string { return filepath.Join(r.Dir, filepath.FromSlash(rel)) }

// WriteFile atomically writes data to a run-relative path.
func (r *Run) WriteFile(rel string, data []byte) error {
	return WriteAtomic(r.Path(rel), data)
}

// WriteJSON validates v and atomically writes it as indented JSON.
func (r *Run) WriteJSON(rel string, v contract.Validator) error {
	if err := v.Validate(); err != nil {
		return fmt.Errorf("refusing to write invalid %s: %w", rel, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", rel, err)
	}
	return r.WriteFile(rel, append(data, '\n'))
}

// ReadJSON strictly decodes and validates a run-relative artifact.
func (r *Run) ReadJSON(rel string, v contract.Validator) error {
	data, err := os.ReadFile(r.Path(rel))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("artifact %s is missing from run %s: %w", rel, r.ID, err)
		}
		return err
	}
	if err := contract.Decode(data, v); err != nil {
		return fmt.Errorf("artifact %s in run %s: %w", rel, r.ID, err)
	}
	return nil
}

// Exists reports whether a run-relative path exists.
func (r *Run) Exists(rel string) bool {
	_, err := os.Stat(r.Path(rel))
	return err == nil
}

// Manifest reads the run manifest.
func (r *Run) Manifest() (*contract.Manifest, error) {
	var m contract.Manifest
	if err := r.ReadJSON(ManifestFile, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// WriteAtomic writes data to path via a temporary file and rename.
func WriteAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
