package harness

import (
	"encoding/json"
	"testing"

	"pcoder/internal/state"
	"pcoder/internal/state/statetest"
)

// newState opens a fresh state file in a temp dir and returns a plugin
// store over it.
func newState(t *testing.T) (*state.Store, *Store) {
	t.Helper()
	st, err := state.Open(t.TempDir(), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	return st, New(st)
}

func TestSaveListGetRoundTrip(t *testing.T) {
	st, l := newState(t)

	id, err := l.Save(Harness{Name: "My Agent!", Command: "my-agent", Install: "npm i -g my-agent"})
	if err != nil || id != "my-agent" {
		t.Fatalf("got (%q, %v), want my-agent", id, err)
	}

	h, err := l.Get(id)
	if err != nil || h.Name != "My Agent!" || h.Command != "my-agent" || h.ID != "my-agent" {
		t.Fatalf("get: %+v err=%v", h, err)
	}

	got, err := l.List()
	if err != nil || len(got) != 1 || got[0].ID != "my-agent" {
		t.Fatalf("list: %+v err=%v", got, err)
	}

	// on disk: exactly this plugin — id synced to the key, install present,
	// no config keys (omitempty), nothing else in the document
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
		"harnesses": map[string]any{
			"my-agent": map[string]any{
				"id":      "my-agent",
				"name":    "My Agent!",
				"command": "my-agent",
				"install": "npm i -g my-agent",
			},
		},
	})
}

// Config v2: the CLI's own portable config rides on the plugin and survives
// the state round trip byte-for-byte.
func TestConfigRoundTrip(t *testing.T) {
	st, l := newState(t)
	raw := []byte(`{"model":"anthropic/claude-x","key":"sk-test"}`)

	if _, err := l.Save(Harness{
		Name: "OpenCode", Command: "opencode",
		ConfigPath: "/root/.config/opencode/opencode.json", Config: raw,
	}); err != nil {
		t.Fatal(err)
	}

	h, err := l.Get("opencode")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if h.ConfigPath != "/root/.config/opencode/opencode.json" {
		t.Fatalf("configPath = %q", h.ConfigPath)
	}
	var cfg map[string]any
	if err := json.Unmarshal(h.Config, &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	if cfg["model"] != "anthropic/claude-x" {
		t.Fatalf("config = %s", h.Config)
	}

	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
		"harnesses": map[string]any{
			"opencode": map[string]any{
				"id":         "opencode",
				"name":       "OpenCode",
				"command":    "opencode",
				"configPath": "/root/.config/opencode/opencode.json",
				"config":     map[string]any{"model": "anthropic/claude-x", "key": "sk-test"},
			},
		},
	})
}

func TestSaveRejectsBadInputAndDuplicates(t *testing.T) {
	st, l := newState(t)

	// duplicate slug is refused
	if _, err := l.Save(Harness{Name: "My Agent", Command: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Save(Harness{Name: "my agent", Command: "x"}); err == nil {
		t.Fatal("duplicate slug should be refused")
	}

	// missing command is refused
	if _, err := l.Save(Harness{Name: "No Cmd"}); err == nil {
		t.Fatal("missing command should be refused")
	}

	// unusable name is refused without writing anything
	if _, err := l.Save(Harness{Name: "---", Command: "x"}); err == nil {
		t.Log("all-punctuation name accepted?") // Slug returns "" → must error
	} else if _, getErr := l.Get("---"); getErr == nil {
		t.Fatal("unusable name was persisted")
	}

	// every rejection left the file at the single accepted plugin
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
		"harnesses": map[string]any{
			"my-agent": map[string]any{"id": "my-agent", "name": "My Agent", "command": "a"},
		},
	})
}

func TestGetMissing(t *testing.T) {
	_, l := newState(t)
	if _, err := l.Get("ghost"); err == nil {
		t.Fatal("expected error for missing plugin")
	}
}

func TestEnsureBuiltinsIdempotent(t *testing.T) {
	st, l := newState(t)

	first, err := l.EnsureBuiltins()
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(first) != 8 {
		t.Fatalf("seeded %d, want 8: %v", len(first), first)
	}

	second, err := l.EnsureBuiltins()
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second seed wrote %v, want none", second)
	}

	got, err := l.List()
	if err != nil {
		t.Fatalf("list seeded: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("loaded %d, want 8: %+v", len(got), got)
	}

	// the seeded document is exactly the eight builtins, field for field
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
		"harnesses": map[string]any{
			"claude":   map[string]any{"id": "claude", "name": "Claude", "command": "claude", "install": "npm i -g @anthropic-ai/claude-code"},
			"cline":    map[string]any{"id": "cline", "name": "Cline", "command": "cline", "install": "npm i -g cline"},
			"codex":    map[string]any{"id": "codex", "name": "Codex", "command": "codex", "install": "npm i -g @openai/codex"},
			"freebuff": map[string]any{"id": "freebuff", "name": "Freebuff", "command": "freebuff", "install": "npm i -g freebuff && freebuff --version || true"},
			"kiro":     map[string]any{"id": "kiro", "name": "Kiro", "command": "kiro-cli", "install": "curl -fsSL https://cli.kiro.dev/install | bash"},
			"opencode": map[string]any{"id": "opencode", "name": "OpenCode", "command": "opencode", "install": "npm i -g opencode-ai"},
			"pi":       map[string]any{"id": "pi", "name": "Pi", "command": "pi", "install": "npm install -g --ignore-scripts @earendil-works/pi-coding-agent"},
			"terminal": map[string]any{"id": "terminal", "name": "Terminal", "command": "bash"},
		},
	})
}

func TestEnsureBuiltinsDoesNotOverwriteUserEdits(t *testing.T) {
	st, l := newState(t)
	if _, err := l.Save(Harness{Name: "Terminal", Command: "zsh"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.EnsureBuiltins(); err != nil {
		t.Fatal(err)
	}
	h, err := l.Get("terminal")
	if err != nil {
		t.Fatal(err)
	}
	if h.Command != "zsh" {
		t.Fatalf("seed overwrote user edit: %+v", h)
	}
	// the user edit is what's on disk — seeding added the other seven around it
	statetest.AssertSection(t, st.Path(), "harnesses", map[string]any{
		"terminal": map[string]any{"id": "terminal", "name": "Terminal", "command": "zsh"},
		"opencode": map[string]any{"id": "opencode", "name": "OpenCode", "command": "opencode", "install": "npm i -g opencode-ai"},
		"codex":    map[string]any{"id": "codex", "name": "Codex", "command": "codex", "install": "npm i -g @openai/codex"},
		"claude":   map[string]any{"id": "claude", "name": "Claude", "command": "claude", "install": "npm i -g @anthropic-ai/claude-code"},
		"freebuff": map[string]any{"id": "freebuff", "name": "Freebuff", "command": "freebuff", "install": "npm i -g freebuff && freebuff --version || true"},
		"cline":    map[string]any{"id": "cline", "name": "Cline", "command": "cline", "install": "npm i -g cline"},
		"pi":       map[string]any{"id": "pi", "name": "Pi", "command": "pi", "install": "npm install -g --ignore-scripts @earendil-works/pi-coding-agent"},
		"kiro":     map[string]any{"id": "kiro", "name": "Kiro", "command": "kiro-cli", "install": "curl -fsSL https://cli.kiro.dev/install | bash"},
	})
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"My Agent":      "my-agent",
		"OpenCode":      "opencode",
		"Cline 2":       "cline-2",
		"  trim -- me ": "trim-me",
		"---":           "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBinary(t *testing.T) {
	cases := map[string]string{
		"opencode":        "opencode",
		"vi hello.txt":    "vi",
		"cline --model x": "cline",
	}
	for cmd, want := range cases {
		if got := Binary(Harness{Command: cmd}); got != want {
			t.Errorf("Binary(%q) = %q, want %q", cmd, got, want)
		}
	}
}
