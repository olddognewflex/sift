package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/schema"
	"github.com/olddognewflex/sift/internal/testutil"
)

// Validate gates the output of harnesses without native schema support, so
// it must accept every valid agent output the fixtures produce.
func TestValidateAcceptsFixtureOutputs(t *testing.T) {
	check := func(name string, s []byte, err error, v any) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(s, data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	s, err := schema.For[contract.DiscoveryReport]("")
	check("discovery", s, err, testutil.Discovery())
	s, err = schema.For[contract.ReviewReport]("")
	check("architecture", s, err, testutil.ArchitectureReview())
	check("correctness", s, err, testutil.CorrectnessReview())
	s, err = schema.For[contract.ValidationDecision]("")
	for id, d := range testutil.Decisions {
		check("decision "+id, s, err, d)
	}
}
