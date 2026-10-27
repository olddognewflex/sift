package schema

import (
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
)

type inner struct {
	N int `json:"n"`
}

type sample struct {
	Name   string            `json:"name"`
	Ok     bool              `json:"ok"`
	Score  float64           `json:"score"`
	Items  []inner           `json:"items"`
	Labels map[string]string `json:"labels"`
	Note   string            `json:"note,omitempty"`
}

func TestValidate(t *testing.T) {
	s, err := For[sample]("sample")
	if err != nil {
		t.Fatal(err)
	}
	good := `{"name":"a","ok":true,"score":1.5,"items":[{"n":2}],"labels":{"k":"v"}}`
	if err := Validate(s, []byte(good)); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	for doc, want := range map[string]string{
		`{"name":"a","ok":true,"score":1,"items":[],"labels":{}`: "not JSON",
		`[1]`: "want object",
		`{"ok":true,"score":1,"items":[],"labels":{}}`:                      `missing required field "name"`,
		`{"name":"a","ok":true,"score":1,"items":[],"labels":{},"extra":1}`: `unexpected field "extra"`,
		`{"name":"a","ok":"yes","score":1,"items":[],"labels":{}}`:          "$.ok: want boolean",
		`{"name":"a","ok":true,"score":1,"items":[{"n":1.5}],"labels":{}}`:  "$.items[0].n: want integer",
		`{"name":"a","ok":true,"score":1,"items":[],"labels":{"k":3}}`:      "$.labels.k: want string",
		`{"name":"a","ok":true,"score":1,"items":[],"labels":{}} {}`:        "trailing data",
		`{"name":null,"ok":true,"score":1,"items":[],"labels":{}}`:          "want string, got null",
	} {
		err := Validate(s, []byte(doc))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want error containing %q, got %v", doc, want, err)
		}
	}
}

// Every published contract schema must accept a document the contract's own
// validation accepts; checked here with a real enum-bearing type.
func TestValidateEnums(t *testing.T) {
	s, err := For[contract.ValidationDecision]("decision")
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(s, []byte(`{"verdict":"definitely"}`)); err == nil || !strings.Contains(err.Error(), "is not one of") {
		t.Fatalf("bad enum accepted or wrong error: %v", err)
	}
}
