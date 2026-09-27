// Package prompt centralizes LLM prompt construction: one block per idea,
// builders per prompt. Shared blocks (product context, prose guidance)
// are inherited, never copied, so audits read one copy. Feature mechanics
// (butler tools, codemap loop rules, classifier schemas) stay in their
// own builders. Only the agent loop's own mechanics (grounding nudge,
// close-out) and one-shot task prompts with embedded data stay beside
// their callers — moving them would scatter, not centralize.
//
// Restraint rule: more prompt is not better. A block earns its place by
// changing model behavior; budgets in prompt_test.go enforce it.
package prompt

// Product teaches what Pocket Coder is and why users open it. Shared so
// every model knows the audience: people coding away from their desk who
// need glanceable answers, not essays.
func Product() string {
	return `Pocket Coder gives every repo its own container with tmux sessions, driven from a phone-friendly web UI. It runs terminal coding CLIs (opencode, codex, claude-code and friends) so users can vibe-code on the move and rotate between free coding harnesses. One user, many projects side by side: long agent sessions keep running without a babysat laptop. Butler is the ops copilot that keeps that fleet healthy; CodeMaps is the reader that explains what the code does.`
}

// Prose guides how to sound: succinct, casual, human-like. Shared so both
// assistants write like a person, not a manual.
func Prose() string {
	return `Write like a person texting a friend who codes: casual, succinct, human. Short sentences, no filler, no preamble. Sacrifice grammar before concision — drop bridge words the reader can infer.`
}
