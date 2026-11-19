package storage

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
)

func verifyManifest(id, original string) *contract.Manifest {
	m := manifest(id)
	m.Verifies = &contract.VerifyTarget{
		Run: original, SourceRevision: "c", SnapshotDigest: "d",
		Findings: []string{"ARCH-001"}, FindingsSHA256: "s",
	}
	return m
}

func TestLatestAuditAndVerifiersOf(t *testing.T) {
	s := Store{Root: t.TempDir()}
	const (
		audit1  = "20261027T100000Z-aaaaaa"
		verify1 = "20261027T110000Z-bbbbbb"
		audit2  = "20261027T120000Z-cccccc"
		verify2 = "20261027T130000Z-dddddd"
		verify3 = "20261027T140000Z-eeeeee"
		broken  = "20261027T150000Z-ffffff"
	)
	if _, err := s.LatestAudit(); !errors.Is(err, ErrNoRuns) {
		t.Fatalf("empty store: %v", err)
	}
	for id, m := range map[string]*contract.Manifest{
		audit1:  manifest(audit1),
		verify1: verifyManifest(verify1, audit1),
		audit2:  manifest(audit2),
		verify2: verifyManifest(verify2, audit2),
		verify3: verifyManifest(verify3, audit1),
	} {
		r, err := s.Create(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.WriteJSON(ManifestFile, m); err != nil {
			t.Fatal(err)
		}
	}
	// The newest run has an unreadable manifest; scans skip it.
	r, err := s.Create(broken)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.WriteFile(ManifestFile, []byte("{not json")); err != nil {
		t.Fatal(err)
	}

	latest, err := s.LatestAudit()
	if err != nil || latest.ID != audit2 {
		t.Fatalf("LatestAudit = %v, %v; want %s", latest, err, audit2)
	}
	for _, tc := range []struct {
		id   string
		want []string
	}{
		{audit1, []string{verify1, verify3}},
		{audit2, []string{verify2}},
		{verify1, nil},
	} {
		got, err := s.VerifiersOf(tc.id)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("VerifiersOf(%s) = %v, %v; want %v", tc.id, got, err, tc.want)
		}
	}

	// Only verify runs left: there is no audit to default to.
	only := Store{Root: t.TempDir()}
	vr, _ := only.Create(verify1)
	if err := vr.WriteJSON(ManifestFile, verifyManifest(verify1, audit1)); err != nil {
		t.Fatal(err)
	}
	if _, err := only.LatestAudit(); !errors.Is(err, ErrNoRuns) {
		t.Fatalf("verify-only store: %v", err)
	}
}

func TestEffectiveStatusNotRunning(t *testing.T) {
	m := manifest("20261027T100000Z-aaaaaa")
	m.Status = contract.RunCompleted
	if st, note := EffectiveStatus(m); st != contract.RunCompleted || note != "" {
		t.Fatalf("completed run = %s %q", st, note)
	}
	m.Status = contract.RunRunning
	m.Host = "some-other-host.invalid"
	if st, _ := EffectiveStatus(m); st != contract.RunRunning {
		t.Fatalf("remote run = %s, want running", st)
	}
	host, _ := os.Hostname()
	m.Host, m.PID = host, os.Getpid()
	if st, _ := EffectiveStatus(m); st != contract.RunRunning {
		t.Fatalf("live local run = %s, want running", st)
	}
}
