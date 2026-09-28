package agent

import (
	"context"
	"encoding/json"
	"strings"
)

// Turn-level shared machinery: the five-stage pipeline (scope, context,
// tools, run, post) both butler and codemap ride, the one Round collector
// that folds trace events into a transcript, and the one history builder
// both callers previously hand-copied.

// Step is one tool call in a transcript round.
type Step struct {
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Output string `json:"output,omitempty"`
	Err    string `json:"error,omitempty"`
}

// Round is one model round in a turn transcript: the model's visible
// thought plus the tool calls it made.
type Round struct {
	Thought string `json:"thought,omitempty"`
	Steps   []Step `json:"steps"`
}

// RoundCollector folds trace events into rounds. This is the single
// state machine that butler and codemap each previously hand-rolled.
type RoundCollector struct {
	rounds  []Round
	pending []Step
}

// Collect consumes one trace event. Unknown kinds are ignored.
func (c *RoundCollector) Collect(ev TraceEvent) {
	switch ev.Kind {
	case "round":
		c.rounds = append(c.rounds, Round{Thought: ev.Text})
	case "tool_start":
		c.pending = append(c.pending, Step{Tool: ev.Tool, Args: ev.Args})
	case "tool_done":
		var st Step
		if len(c.pending) > 0 {
			st = c.pending[0]
			c.pending = c.pending[1:]
		} else {
			// Unknown-tool path or out-of-order event: fall
			// back to the done event's own identity.
			st = Step{Tool: ev.Tool, Args: ev.Args}
		}
		if ev.Err != "" {
			st.Err = ev.Err
		} else {
			st.Output = ev.Result
		}
		if len(c.rounds) == 0 {
			c.rounds = append(c.rounds, Round{})
		}
		cur := &c.rounds[len(c.rounds)-1]
		cur.Steps = append(cur.Steps, st)
	}
}

// Rounds returns the transcript so far.
func (c *RoundCollector) Rounds() []Round { return c.rounds }

// TraceFunc wraps a collector into an onTrace handler: collector first,
// then the caller's own observer (streaming, obs logging).
func TraceFunc(c *RoundCollector, onTrace func(TraceEvent)) func(TraceEvent) {
	return func(ev TraceEvent) {
		c.Collect(ev)
		if onTrace != nil {
			onTrace(ev)
		}
	}
}

// TurnView is the caller-agnostic view of one persisted turn that
// BuildHistory replays. Callers map their concrete turn types onto this.
type TurnView struct {
	Prompt string
	Answer string
	Steps  []Step
	Failed bool
}

// HistoryOpts bound a history rebuild.
type HistoryOpts struct {
	MaxTurns int // keep the newest N turns (0 = unlimited)
	MaxChars int // drop oldest until estimated chars fit (0 = unlimited)
}

// BuildHistory rebuilds the LLM conversation from persisted turns: each
// turn becomes user(prompt) plus one assistant entry carrying the tool
// steps and the answer text. Failed turns contribute their prompt only.
// Context is bounded newest-first. This is the one implementation that
// butlerHistory and threadHistory previously hand-copied.
func BuildHistory(turns []TurnView, userPrompt string, o HistoryOpts) []map[string]any {
	if o.MaxTurns > 0 && len(turns) > o.MaxTurns {
		turns = turns[len(turns)-o.MaxTurns:]
	}
	if o.MaxChars > 0 {
		size := func(t TurnView) int {
			n := len(t.Prompt) + len(t.Answer)
			for _, s := range t.Steps {
				n += len(s.Output) + len(s.Err)
			}
			return n
		}
		total := 0
		for _, t := range turns {
			total += size(t)
		}
		start := 0
		for total > o.MaxChars && start < len(turns) {
			total -= size(turns[start])
			start++
		}
		turns = turns[start:]
	}
	var out []map[string]any
	for _, t := range turns {
		if strings.TrimSpace(t.Prompt) == "" && strings.TrimSpace(t.Answer) == "" && len(t.Steps) == 0 {
			continue
		}
		// A just-reserved placeholder replays as userPrompt, not history.
		if t.Answer == "" && !t.Failed && t.Prompt == userPrompt {
			continue
		}
		if strings.TrimSpace(t.Prompt) != "" {
			out = append(out, map[string]any{"role": "user", "content": t.Prompt})
		}
		if t.Failed {
			continue
		}
		var toolSteps []any
		for _, s := range t.Steps {
			if strings.TrimSpace(s.Tool) == "" {
				continue
			}
			if strings.TrimSpace(s.Output) == "" && strings.TrimSpace(s.Err) == "" {
				continue
			}
			toolSteps = append(toolSteps, map[string]any{
				"tool": s.Tool, "args": s.Args,
				"output": s.Output, "error": s.Err,
			})
		}
		if len(toolSteps) > 0 {
			out = append(out, map[string]any{
				"role": "assistant", "content": t.Answer,
				"toolSteps": toolSteps,
			})
		} else if strings.TrimSpace(t.Answer) != "" {
			out = append(out, map[string]any{"role": "assistant", "content": t.Answer})
		}
	}
	return out
}

// ScopeGate is the optional first stage: one structured call that may
// refuse the turn before the loop runs. Refusal is a pinned string.
type ScopeGate struct {
	Prompt     string
	Schema     map[string]any
	SchemaName string
	// Refused parses the structured verdict; false = refuse.
	Refused func(verdict string) bool
	Refusal string
}

// TurnSpec is stage 3+4 as data: what the loop runs with.
type TurnSpec struct {
	System     func() string // guide; may append orientation per turn
	UserMsg    string        // reserved for future lead-ins; usually ""
	Tools      []Tool
	MaxSteps   int
	Schema     map[string]any // nil = free-text final answer
	SchemaName string
	// Opts are extra Run options (grounding, lead-ins).
	Opts []RunOption
}

// TurnResult is what a pipeline run hands to the post stage.
type TurnResult struct {
	Answer  string
	Rounds  []Round
	Lineage *Lineage
	Err     error
	Refused bool // scope gate refused; Answer holds the pinned refusal
}

// Pipeline is the whole turn. Fields may be zero: nil Scope skips the
// gate, nil Post skips caller post-processing.
type Pipeline struct {
	Scope   *ScopeGate
	Context func(userPrompt string) []map[string]any
	Run     TurnSpec
	Post    func(res TurnResult)
	// OnTrace observes live events (streaming, obs logging). The
	// transcript collector runs before this regardless.
	OnTrace func(TraceEvent)
}

// Execute runs the pipeline for one user prompt.
func (p Pipeline) Execute(ctx context.Context, cfg Config, lin *Lineage, userPrompt string) TurnResult {
	if p.Scope != nil {
		verdict, err := Structured(ctx, cfg, p.Scope.Prompt, userPrompt, p.Scope.SchemaName, p.Scope.Schema, lin)
		if err == nil && p.Scope.Refused(verdict) {
			res := TurnResult{Answer: p.Scope.Refusal, Refused: true, Lineage: lin}
			if p.Post != nil {
				p.Post(res)
			}
			return res
		}
		// Gate errors fall through to the loop: the gate only refutes.
	}
	var hist []map[string]any
	if p.Context != nil {
		hist = p.Context(userPrompt)
	}
	sys := p.Run.System()
	c := &RoundCollector{}
	answer, err := Run(ctx, cfg, sys, userPrompt, hist, p.Run.Tools, p.Run.SchemaName, p.Run.Schema,
		p.Run.MaxSteps, TraceFunc(c, p.OnTrace), lin, p.Run.Opts...)
	res := TurnResult{Answer: answer, Rounds: c.Rounds(), Lineage: lin, Err: err}
	if p.Post != nil {
		p.Post(res)
	}
	return res
}

// JSONArg is a helper for pipeline stages that need to decode raw args.
func JSONArg(raw string, v any) error { return json.Unmarshal([]byte(raw), v) }
