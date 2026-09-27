// Codemap prompts, built from shared blocks: the same product context
// and prose as Butler, plus the repo-QA loop rules that are codemap's
// own. Anything here must earn its sentences — see the restraint rule in
// shared.go.
package prompt

import "strings"

// CodemapLoopRules is how codemaps ground answers in repo searches: heavy
// but judicious tool use, defended step by step, closed with findings.
func CodemapLoopRules() string {
	return `Loop rules: 1. Use tools heavily but judiciously: often enough for full context, never more than needed. 2. Loop as many rounds as needed to get sufficient context; don't be shy. 3. All tool paths are relative to the repo root (e.g. "src/main.jsx"), never absolute paths starting with "/". 4. Think aloud: every message that calls tools also states in one short sentence what you are checking and why, so each round steers the next. 5. Work efficiently: batch independent tool calls in one block, never re-read a file you already read this turn, and stop calling tools as soon as you can answer. 6. Close with findings, not actions: the final answer names what the code does and the exact files involved, never the steps you took to find it.`
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
