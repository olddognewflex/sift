package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
)

// Validate checks data against a schema produced by For. It supports exactly
// the keywords For emits (type, properties, required, additionalProperties,
// items, enum) and is meant for harnesses that cannot enforce a schema
// themselves. It returns every violation found, joined.
func Validate(schemaDoc, data []byte) error {
	var s map[string]any
	if err := json.Unmarshal(schemaDoc, &s); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("not JSON: trailing data after the value")
	}
	var errs []string
	check(s, v, "$", &errs)
	if len(errs) > 0 {
		return fmt.Errorf("does not match schema: %s", strings.Join(errs, "; "))
	}
	return nil
}

func check(s map[string]any, v any, path string, errs *[]string) {
	bad := func(format string, args ...any) { *errs = append(*errs, path+": "+fmt.Sprintf(format, args...)) }
	if enum, ok := s["enum"].([]any); ok && !slices.Contains(enum, v) {
		bad("%v is not one of %v", v, enum)
		return
	}
	typ, _ := s["type"].(string)
	switch typ {
	case "":
		return // any value (json.RawMessage fields)
	case "string":
		if _, ok := v.(string); !ok {
			bad("want string, got %s", kind(v))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			bad("want boolean, got %s", kind(v))
		}
	case "number", "integer":
		n, ok := v.(json.Number)
		if !ok {
			bad("want %s, got %s", typ, kind(v))
			return
		}
		f, err := n.Float64()
		if err != nil || (typ == "integer" && f != math.Trunc(f)) {
			bad("want %s, got %s", typ, n)
		}
	case "array":
		items, ok := v.([]any)
		if !ok {
			bad("want array, got %s", kind(v))
			return
		}
		is, _ := s["items"].(map[string]any)
		for i, it := range items {
			check(is, it, fmt.Sprintf("%s[%d]", path, i), errs)
		}
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			bad("want object, got %s", kind(v))
			return
		}
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, ok := obj[r.(string)]; !ok {
					bad("missing required field %q", r)
				}
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if ps, ok := props[k].(map[string]any); ok {
				check(ps, obj[k], path+"."+k, errs)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap {
					bad("unexpected field %q", k)
				}
			case map[string]any:
				check(ap, obj[k], path+"."+k, errs)
			}
		}
	default:
		bad("schema type %q is not supported", typ)
	}
}

func kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}
