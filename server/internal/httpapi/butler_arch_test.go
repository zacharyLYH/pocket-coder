package httpapi

import (
	"context"
	"strings"
	"testing"
)

// Sections read whole or by key; unknown keys list what exists instead
// of failing, so the model recovers in one round.
func TestButlerArchitectureTool(t *testing.T) {
	d, _, _, _ := newSessionDeps(t)
	run := butlerToolByName(t, d, butlerToolArchitecture, nil)

	whole, err := run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("architecture: %v", err)
	}
	if !strings.Contains(whole, "## State") || !strings.Contains(whole, "## Butler") {
		t.Fatalf("whole doc = %q, want all sections", cut(whole, 120))
	}
	if strings.Contains(whole, "## Transcripts") || strings.Contains(whole, "## Writes") {
		t.Fatal("Writes and Transcripts merged into Butler: update the mirror too")
	}

	one, err := run(context.Background(), `{"section":"projects"}`)
	if err != nil {
		t.Fatalf("architecture section: %v", err)
	}
	if !strings.Contains(one, "pcoder-owner-repo") || strings.Contains(one, "## Sessions") {
		t.Fatalf("projects section = %q, want just that section", cut(one, 120))
	}

	unknown, err := run(context.Background(), `{"section":"nope"}`)
	if err != nil {
		t.Fatalf("architecture unknown: %v", err)
	}
	if !strings.Contains(unknown, "projects") || !strings.Contains(unknown, "state") {
		t.Fatalf("unknown section = %q, want the known list", unknown)
	}
}
