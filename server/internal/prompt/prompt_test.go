package prompt

import (
	"fmt"
	"strings"
	"testing"
)

// Shared blocks are inherited verbatim, never copied: both guides carry
// the same product context and prose, and the scope gate carries the
// same role as the turn guide.
func TestPromptBlocksShared(t *testing.T) {
	reads := []string{"r1", "r2"}
	writes := []string{"w1"}
	guide := ButlerGuide(reads, writes)
	for _, want := range []string{Product(), Prose(), ButlerRole(), ButlerRefusal} {
		if !strings.Contains(guide, want) {
			t.Fatalf("butler guide omits shared block %q", cut(want, 60))
		}
	}
	codemap := CodemapGuide()
	for _, want := range []string{Product(), Prose()} {
		if !strings.Contains(codemap, want) {
			t.Fatalf("codemap guide omits shared block %q", cut(want, 60))
		}
	}
	if !strings.Contains(ButlerScopePrompt(), ButlerRole()) {
		t.Fatal("scope prompt drifted from the shared role block")
	}
}

// Retired phrasing stays retired: the old codemap tone copy ("laid back",
// "user friendly UX") and the typo ("succintly") must not creep back in
// a second copy of the same idea.
func TestPromptNoWeakCopies(t *testing.T) {
	all := ButlerGuide([]string{"r"}, []string{"w"}) + "\n" + ButlerScopePrompt() + "\n" + CodemapGuide()
	for _, banned := range []string{"laid back", "user friendly UX", "succintly", "Keep answers short."} {
		if strings.Contains(all, banned) {
			t.Fatalf("retired phrasing %q present: one copy of each idea only", banned)
		}
	}
}

// Budgets enforce the restraint rule: a block earns its place by changing
// behavior, and these caps force a conscious edit to grow them. Sizes use
// representative 10+23 tool names, matching the real registry shape.
func TestPromptBudgets(t *testing.T) {
	reads := make([]string, 10)
	writes := make([]string, 23)
	for i := range reads {
		reads[i] = fmt.Sprintf("read_tool_%d", i)
	}
	for i := range writes {
		writes[i] = fmt.Sprintf("write_tool_%d", i)
	}
	if n := len(ButlerGuide(reads, writes)); n > 3600 {
		t.Fatalf("butler guide = %d chars, over the 3600 budget", n)
	}
	if n := len(ButlerScopePrompt()); n > 900 {
		t.Fatalf("scope prompt = %d chars, over the 900 budget", n)
	}
	if n := len(ButlerWorkflows()); n > 700 {
		t.Fatalf("workflow examples = %d chars, over the 700 budget", n)
	}
	if n := len(CodemapGuide()); n > 1600 {
		t.Fatalf("codemap guide = %d chars, over the 1600 budget", n)
	}
}

// The scope schema pins one required boolean: the gate output is a
// verdict, never prose.
func TestPromptScopeSchema(t *testing.T) {
	s := ButlerScopeSchema()
	props, _ := s["properties"].(map[string]any)
	if _, ok := props["can_help"]; !ok {
		t.Fatalf("scope schema = %v, want can_help", s)
	}
	req, _ := s["required"].([]string)
	if len(req) != 1 || req[0] != "can_help" {
		t.Fatalf("scope schema required = %v, want exactly [can_help]", req)
	}
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
