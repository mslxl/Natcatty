package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// expectedABI is the ghostty_type_json manifest captured from a library
// built at pinnedCommit. The Go bindings mirror these layouts and enums and
// refuse to load anything else; a build that reports a different manifest
// is a mismatched artifact, not a runtime surprise.
//
// The fixture is host-independent for 64-bit targets: the tooling compares
// only the ABI fields the bindings use, not abi.target/abi.os, which name
// the machine that built the probe.
//
//go:embed testdata/abi-befcdfd2.json
var expectedABI []byte

// diffABI compares the parts of the type manifest the Go bindings depend
// on. Additions are allowed (a newer library may describe more types); any
// missing type, changed size/offset/enum value or version change fails.
func diffABI(wantBytes, gotBytes []byte) error {
	var want, got map[string]any
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		return fmt.Errorf("expected ABI fixture: %w", err)
	}
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		return fmt.Errorf("library reported invalid ghostty_type_json: %w", err)
	}
	if len(got) == 0 {
		return fmt.Errorf("library reported an empty ghostty_type_json manifest")
	}

	for _, path := range []string{"schema", "library_version", "abi.pointer_size", "abi.usize_size", "abi.max_alignment", "abi.endian"} {
		w, ok := lookup(want, path)
		if !ok {
			continue
		}
		g, ok := lookup(got, path)
		if !ok {
			return fmt.Errorf("ABI mismatch: library manifest has no %s; the Go bindings require it", path)
		}
		if !scalarEqual(w, g) {
			switch path {
			case "library_version":
				return fmt.Errorf("ABI mismatch: library version is %v, Go bindings expect %v (pinned commit %s)", formatScalar(g), formatScalar(w), pinnedCommit[:12])
			case "schema":
				return fmt.Errorf("ABI mismatch: type manifest schema is %v, Go bindings expect %v", formatScalar(g), formatScalar(w))
			default:
				return fmt.Errorf("ABI mismatch: %s is %v, Go bindings expect %v", path, formatScalar(g), formatScalar(w))
			}
		}
	}

	wantTypes, ok := want["types"].(map[string]any)
	if !ok {
		return fmt.Errorf("expected ABI fixture has no types object")
	}
	gotTypes, ok := got["types"].(map[string]any)
	if !ok {
		return fmt.Errorf("ABI mismatch: library manifest has no types object; the Go bindings require it")
	}
	return diffValue("types", wantTypes, gotTypes)
}

func lookup(m map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = m
	for _, part := range parts {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func diffValue(path string, want, got any) error {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("ABI mismatch: %s is %T, want an object", path, got)
		}
		for key, wantValue := range w {
			gotValue, ok := g[key]
			if !ok {
				return fmt.Errorf("ABI mismatch: %s.%s is missing; the Go bindings require it", path, key)
			}
			if err := diffValue(path+"."+key, wantValue, gotValue); err != nil {
				return err
			}
		}
		return nil
	case []any:
		g, ok := got.([]any)
		if !ok {
			return fmt.Errorf("ABI mismatch: %s is %T, want an array", path, got)
		}
		if len(w) != len(g) {
			return fmt.Errorf("ABI mismatch: %s has %d values, Go bindings expect %d", path, len(g), len(w))
		}
		for i := range w {
			if err := diffValue(fmt.Sprintf("%s[%d]", path, i), w[i], g[i]); err != nil {
				return err
			}
		}
		return nil
	default:
		if !scalarEqual(want, got) {
			return fmt.Errorf("ABI mismatch: %s is %v, Go bindings expect %v", path, formatScalar(got), formatScalar(want))
		}
		return nil
	}
}

func scalarEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return reflect.DeepEqual(a, b)
}

func formatScalar(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%v", v)
}
