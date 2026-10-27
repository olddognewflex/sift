// Package agent loads agent definitions: Markdown instructions with a small
// YAML front matter block naming the agent and its version.
package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/olddognewflex/sift"
)

// Definition is a loaded agent definition.
type Definition struct {
	Name         string
	Version      string
	Description  string
	Instructions string // Markdown body, used as the agent's system prompt
	SHA256       string // digest of the whole file, recorded for reproducibility
	Source       string // "builtin:<file>" or an absolute path
	Raw          []byte // the file as loaded
}

type frontMatter struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Load returns the named definitions from dir, or from the built-in set when
// dir is empty. Every name must resolve.
func Load(dir string, names ...string) (map[string]Definition, error) {
	var fsys fs.FS = sift.Agents
	prefix, source := "agents/", "builtin:agents/"
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		fsys, prefix, source = os.DirFS(abs), "", abs+string(filepath.Separator)
	}
	defs := make(map[string]Definition, len(names))
	for _, name := range names {
		if !namePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid agent name %q", name)
		}
		data, err := fs.ReadFile(fsys, prefix+name+".md")
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", name, err)
		}
		def, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("agent %q (%s%s.md): %w", name, source, name, err)
		}
		if def.Name != name {
			return nil, fmt.Errorf("agent file %s.md declares name %q", name, def.Name)
		}
		def.Source = source + name + ".md"
		defs[name] = def
	}
	return defs, nil
}

// Parse parses a definition file.
func Parse(data []byte) (Definition, error) {
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return Definition{}, fmt.Errorf("missing front matter")
	}
	head, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return Definition{}, fmt.Errorf("unterminated front matter")
	}
	var fm frontMatter
	dec := yaml.NewDecoder(bytes.NewReader(head))
	dec.KnownFields(true)
	if err := dec.Decode(&fm); err != nil {
		return Definition{}, fmt.Errorf("front matter: %w", err)
	}
	if fm.Name == "" || fm.Version == "" {
		return Definition{}, fmt.Errorf("front matter needs name and version")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return Definition{}, fmt.Errorf("empty instructions")
	}
	sum := sha256.Sum256(data)
	return Definition{
		Name:         fm.Name,
		Version:      fm.Version,
		Description:  fm.Description,
		Instructions: string(body),
		SHA256:       hex.EncodeToString(sum[:]),
		Raw:          data,
	}, nil
}
