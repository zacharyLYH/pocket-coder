package statetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The helper's own contract: Compare must catch every way a state file can
// drift from expectations — an extra key anywhere, a missing key, a changed
// value, an extra/missing array element — and must pass on an exact match.
func TestCompareCatchesEveryDrift(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	write := func(t *testing.T, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exact := `{"user":{"email":"me@example.com"},"projects":{"abc":{"name":"x","repo":""}}}`
	want := map[string]any{
		"user":     map[string]any{"email": "me@example.com"},
		"projects": map[string]any{"abc": map[string]any{"name": "x", "repo": ""}},
	}

	t.Run("exact match passes", func(t *testing.T) {
		write(t, exact)
		if err := Compare(path, want); err != nil {
			t.Fatalf("exact state rejected: %v", err)
		}
	})

	cases := map[string]string{
		"extra top-level key":     `{"user":{"email":"me@example.com"},"projects":{"abc":{"name":"x","repo":""}},"smtp":{"host":"h"}}`,
		"missing top-level key":   `{"user":{"email":"me@example.com"}}`,
		"extra project":           `{"user":{"email":"me@example.com"},"projects":{"abc":{"name":"x","repo":""},"zzz":{"name":"y","repo":""}}}`,
		"missing project":         `{"user":{"email":"me@example.com"},"projects":{}}`,
		"extra field on record":   `{"user":{"email":"me@example.com"},"projects":{"abc":{"name":"x","repo":"","branch":"main"}}}`,
		"missing field on record": `{"user":{"email":"me@example.com"},"projects":{"abc":{"name":"x"}}}`,
		"wrong value":             `{"user":{"email":"me@example.com"},"projects":{"abc":{"name":"y","repo":""}}}`,
		"wrong type":              `{"user":{"email":"me@example.com"},"projects":[]}`,
		"extra array element":     `{"user":{"email":"me@example.com"},"sshKeys":[{"fingerprint":"a","publicKey":"k","email":"e"}],"projects":{"abc":{"name":"x","repo":""}}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			write(t, content)
			if err := Compare(path, want); err == nil {
				t.Fatalf("Compare accepted drifted state:\n%s", content)
			}
		})
	}

	t.Run("invalid json is an error, not a match", func(t *testing.T) {
		write(t, "{not json")
		err := Compare(path, want)
		if err == nil || !strings.Contains(err.Error(), "parse") {
			t.Fatalf("err = %v, want parse error", err)
		}
	})

	t.Run("missing file is an error", func(t *testing.T) {
		if err := Compare(filepath.Join(dir, "gone.json"), want); err == nil {
			t.Fatal("missing file accepted")
		}
	})
}
