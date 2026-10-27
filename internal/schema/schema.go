// Package schema generates JSON Schemas from Sift's Go contract types, so the
// schemas given to harnesses and published for integrations never drift from
// the types used to decode and validate artifacts.
//
// It supports exactly the shapes used by internal/contract: structs (with
// embedding), strings, booleans, integers, floats, slices, string-keyed maps,
// pointers, time.Time, json.RawMessage, and string enums that implement
// Enum() []string. Struct fields without `omitempty` are required, and
// objects do not allow additional properties.
package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type enumer interface{ Enum() []string }

var (
	timeType   = reflect.TypeFor[time.Time]()
	rawType    = reflect.TypeFor[json.RawMessage]()
	enumerType = reflect.TypeFor[enumer]()
)

// For returns the JSON Schema (draft-07) for T as a JSON document. Draft-07
// is used because Claude Code's --json-schema validator rejects 2020-12.
func For[T any](title string) ([]byte, error) {
	s, err := generate(reflect.TypeFor[T]())
	if err != nil {
		return nil, err
	}
	s["$schema"] = "http://json-schema.org/draft-07/schema#"
	if title != "" {
		s["title"] = title
	}
	return json.MarshalIndent(s, "", "  ")
}

func generate(t reflect.Type) (map[string]any, error) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == timeType:
		return map[string]any{"type": "string", "format": "date-time"}, nil
	case t == rawType:
		return map[string]any{}, nil
	case t.Implements(enumerType) && t.Kind() == reflect.String:
		values := reflect.Zero(t).Interface().(enumer).Enum()
		return map[string]any{"type": "string", "enum": values}, nil
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Slice:
		items, err := generate(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("schema: map key %s is not a string", t.Key())
		}
		values, err := generate(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": values}, nil
	case reflect.Struct:
		return object(t)
	}
	return nil, fmt.Errorf("schema: unsupported type %s", t)
}

func object(t reflect.Type) (map[string]any, error) {
	props := map[string]any{}
	required := []string{}
	if err := addFields(t, props, &required); err != nil {
		return nil, err
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}, nil
}

func addFields(t reflect.Type, props map[string]any, required *[]string) error {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || (!f.IsExported() && !f.Anonymous) {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			// Embedded structs are flattened, matching encoding/json.
			if err := addFields(f.Type, props, required); err != nil {
				return err
			}
			continue
		}
		if name == "" {
			name = f.Name
		}
		s, err := generate(f.Type)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
		}
		if d := f.Tag.Get("desc"); d != "" {
			s["description"] = d
		}
		props[name] = s
		if !strings.Contains(opts, "omitempty") {
			*required = append(*required, name)
		}
	}
	return nil
}
