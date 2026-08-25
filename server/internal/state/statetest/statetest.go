// Package statetest provides test-only assertions over the on-disk
// state.json — the app's single source of truth. Assertions go through a
// GENERIC map (not the typed Document), so unexpected keys, dropped
// sections, and omitempty surprises cannot hide: the file must be exactly
// what the test says — nothing more, nothing less.
package statetest

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

// Read loads the raw state.json at path into a generic map. Every key that
// exists on disk is visible here, including ones the typed Document would
// silently drop. (Path-based, not store-based, so test files in the state
// package itself can use it without an import cycle.)
func Read(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse state file %s: %v\nraw:\n%s", path, err, raw)
	}
	return doc
}

// AssertEqual fails unless the on-disk state.json is EXACTLY want: same
// top-level sections, same records, same fields — nothing more, nothing
// less. want is normalized through JSON first, so callers may write plain
// Go maps with ints where the file would have numbers.
func AssertEqual(t *testing.T, path string, want map[string]any) {
	t.Helper()
	if err := Compare(path, want); err != nil {
		got := Read(t, path)
		wantNorm := normalize(t, want)
		t.Fatalf("%v\n--- got ---\n%s\n--- want ---\n%s", err, pretty(got), pretty(wantNorm))
	}
}

// Compare is AssertEqual without the testing.T: it returns a description of
// the first divergence (extra key, missing key, wrong value, ...). Exists
// so the helper itself can be tested.
func Compare(path string, want map[string]any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read state file: %w", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		return fmt.Errorf("parse state file: %w", err)
	}
	wantNorm, err := normalizeMap(want)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, wantNorm) {
		return fmt.Errorf("state.json does not match the expected state")
	}
	return nil
}

// AssertSection asserts one top-level section (e.g. "projects") equals want,
// leaving the rest of the document unchecked. Use for focused tests; prefer
// AssertEqual when the whole file is known.
func AssertSection(t *testing.T, path, section string, want any) {
	t.Helper()
	got := Read(t, path)
	gotSection, ok := got[section]
	if !ok {
		t.Fatalf("state.json has no %q section\ncurrent state:\n%s", section, pretty(got))
	}
	wantNorm := normalizeValue(t, want)
	if !reflect.DeepEqual(gotSection, wantNorm) {
		t.Fatalf("state.json %q section does not match\n--- got ---\n%s\n--- want ---\n%s",
			section, prettyValue(gotSection), prettyValue(wantNorm))
	}
}

// normalize round-trips want through JSON so ints, typed structs, and
// json.RawMessage compare equal to what decoding the file produces.
func normalizeMap(want map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(want)
	if err != nil {
		return nil, fmt.Errorf("normalize want: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("normalize want: %w", err)
	}
	return out, nil
}

func normalize(t *testing.T, want map[string]any) map[string]any {
	t.Helper()
	out, err := normalizeMap(want)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func normalizeValue(t *testing.T, want any) any {
	t.Helper()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("normalize want: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("normalize want: %v", err)
	}
	return out
}

func pretty(v any) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(raw)
}

func prettyValue(v any) string { return pretty(v) }
