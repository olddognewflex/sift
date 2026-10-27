package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinsLoad(t *testing.T) {
	defs, err := Load("", "discovery", "architecture", "correctness", "pruv")
	if err != nil {
		t.Fatal(err)
	}
	for name, d := range defs {
		if d.Version == "" || len(d.SHA256) != 64 || !strings.HasPrefix(d.Source, "builtin:") || d.Instructions == "" {
			t.Errorf("%s: %+v", name, d)
		}
		if strings.HasPrefix(d.Instructions, "---") {
			t.Errorf("%s: front matter leaked into instructions", name)
		}
	}
}

func TestOverrideDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pruv", "---\nname: pruv\nversion: 7\n---\nBe skeptical.\n")
	defs, err := Load(dir, "pruv")
	if err != nil {
		t.Fatal(err)
	}
	if d := defs["pruv"]; d.Version != "7" || d.Instructions != "Be skeptical." || !strings.HasPrefix(d.Source, dir) {
		t.Fatalf("got %+v", d)
	}
	if _, err := Load(dir, "discovery"); err == nil {
		t.Fatal("missing definition must fail")
	}
	write("bad", "---\nname: other\nversion: 1\n---\nx\n")
	if _, err := Load(dir, "bad"); err == nil {
		t.Fatal("name mismatch must fail")
	}
	if _, err := Load(dir, "../pruv"); err == nil {
		t.Fatal("path-like names must fail")
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no front matter": "hello",
		"unterminated":    "---\nname: x\n",
		"no version":      "---\nname: x\n---\nbody",
		"unknown key":     "---\nname: x\nversion: 1\nmodel: y\n---\nbody",
		"empty body":      "---\nname: x\nversion: 1\n---\n  \n",
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
