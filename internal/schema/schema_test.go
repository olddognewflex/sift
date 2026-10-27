package schema_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/schema"
)

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRequiredAndEnums(t *testing.T) {
	data, err := schema.For[contract.FindingDraft]("draft")
	if err != nil {
		t.Fatal(err)
	}
	s := decode(t, data)
	if s["additionalProperties"] != false || s["title"] != "draft" {
		t.Fatalf("object constraints missing: %v", s)
	}
	var required []string
	for _, r := range s["required"].([]any) {
		required = append(required, r.(string))
	}
	if !slices.Contains(required, "claim") || slices.Contains(required, "alternatives") {
		t.Errorf("required = %v (omitempty fields must be optional)", required)
	}
	props := s["properties"].(map[string]any)
	enum := props["severity"].(map[string]any)["enum"].([]any)
	if len(enum) != len(contract.SeverityHigh.Enum()) {
		t.Errorf("severity enum = %v", enum)
	}
	if props["claim"].(map[string]any)["description"] == nil {
		t.Error("desc tag not emitted")
	}
	ev := props["evidence"].(map[string]any)["items"].(map[string]any)
	if ev["additionalProperties"] != false {
		t.Error("nested objects must disallow additional properties")
	}
}

func TestEmbeddedStructsAreFlattened(t *testing.T) {
	data, err := schema.For[contract.Finding]("")
	if err != nil {
		t.Fatal(err)
	}
	props := decode(t, data)["properties"].(map[string]any)
	for _, k := range []string{"id", "claim", "validation_state", "evidence_checks"} {
		if _, ok := props[k]; !ok {
			t.Errorf("missing property %q", k)
		}
	}
}

// published are the schemas written to schemas/ for integrations.
var published = map[string]func() ([]byte, error){
	"manifest.schema.json":             func() ([]byte, error) { return schema.For[contract.Manifest]("Sift audit manifest") },
	"discovery.schema.json":            func() ([]byte, error) { return schema.For[contract.Discovery]("Sift system discovery context") },
	"review-plan.schema.json":          func() ([]byte, error) { return schema.For[contract.ReviewPlan]("Sift review plan") },
	"review.schema.json":               func() ([]byte, error) { return schema.For[contract.Review]("Sift reviewer output") },
	"findings.schema.json":             func() ([]byte, error) { return schema.For[contract.FindingSet]("Sift findings") },
	"validation-decisions.schema.json": func() ([]byte, error) { return schema.For[contract.DecisionSet]("Sift validation decisions") },
	"agent-execution.schema.json":      func() ([]byte, error) { return schema.For[contract.ExecutionResult]("Sift agent execution result") },
	"report-metadata.schema.json":      func() ([]byte, error) { return schema.For[contract.ReportMeta]("Sift report metadata") },
}

// TestPublishedSchemasUpToDate keeps schemas/ in sync with the Go types.
// Regenerate with: make schemas
func TestPublishedSchemasUpToDate(t *testing.T) {
	dir := filepath.Join("..", "..", "schemas")
	update := os.Getenv("UPDATE_SCHEMAS") == "1"
	for name, gen := range published {
		want, err := gen()
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, '\n')
		path := filepath.Join(dir, name)
		if update {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is out of date; run `make schemas`", path)
		}
	}
}
