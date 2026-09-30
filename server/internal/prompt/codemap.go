// Codemap prompts, built from shared blocks: the same product context
// and prose as Butler, plus the repo-QA loop rules that are codemap's
// own. Anything here must earn its sentences — see the restraint rule in
// shared.go.
package prompt

import "strings"

// CodemapLoopRules is how codemaps ground answers in repo searches: heavy
// but judicious tool use, defended step by step, closed with findings.
func CodemapLoopRules() string {
	return `Loop rules: 1. Use tools heavily but judiciously: often enough for full context, never more than needed. 2. Loop as many rounds as needed to get sufficient context; don't be shy. 3. For work with 3 or more steps, create and maintain a todo checklist with the todo tool; update it as work completes, and keep exactly one item in_progress. 4. Tool paths are ABSOLUTE under the repo root given at turn start — copy it verbatim. 5. Think aloud: every message that calls tools also states in one short sentence what you are checking and why, so each round steers the next. 6. Work efficiently: batch independent tool calls in one block, never re-read a file you already read this turn, and stop calling tools as soon as you can answer. 7. Close with findings, not actions: the final answer names what the code does and the exact files involved, never the steps you took to find it. Final refs stay repo-relative.`
}

// CodemapGuide assembles the repo-QA system prompt.
func CodemapGuide() string {
	return strings.Join([]string{
		Product(),
		`CodeMaps answers questions about this repo: read the question carefully, use tool calls to gather sufficient context, then answer.`,
		Prose(),
		CodemapLoopRules(),
	}, " ")
}

// CodemapScopePrompt is the codemap gate, the mirror of Butler's: true
// when the query is about this repo's code, false for ops asks (lifecycle,
// models, keys) that belong to Butler instead.
func CodemapScopePrompt() string {
	return Product() + ` Decide whether a user query is a question about this repo's code that CodeMaps can answer. Answer only JSON matching the response schema. about_code is true for questions about code, architecture, behavior, or files of this repo. It is false for operational requests (start, stop, models, keys, shortcuts) and anything not about this repo.`
}

// CodemapScopeSchema pins the classifier output: one required boolean.
func CodemapScopeSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"about_code": map[string]any{"type": "boolean"}},
		"required":             []string{"about_code"},
		"additionalProperties": false,
	}
}
