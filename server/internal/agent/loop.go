package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v2"
)

const (
	// groundingNudge steers ungrounded answers (zero tool rounds) toward
	// one verification pass.
	groundingNudge = "Before answering, ground your answer: call at least one available tool to verify against the repo. If the question has nothing to do with this repo's code, answer directly instead."
	// closeOutPrompt turns maxSteps exhaustion into an answer: one
	// tools-free call over everything gathered so far.
	closeOutPrompt = "You have used all available tool rounds. Provide your final answer now based on everything gathered — no more tool calls."
)

// Run answers one prompt: gather free text via the tool loop, then shape
// it through the schema-enforcing format call.
func Run(ctx context.Context, cfg Config, sysPrompt, userPrompt string, history []map[string]any, tools []Tool, schemaName string, schema map[string]any, maxSteps int, onTrace func(TraceEvent), lin *Lineage, opts ...RunOption) (string, error) {
	if !cfg.Valid() {
		return "", fmt.Errorf("ai not configured")
	}
	if maxSteps <= 0 {
		maxSteps = 8
	}
	client := NewClient(cfg)
	if lin != nil {
		lin.Model = cfg.Model
		lin.SetTools(tools)
	}
	l := newLoop(client, cfg, tools, assembleMessages(sysPrompt, userPrompt, history), maxSteps, onTrace, lin)
	for _, o := range opts {
		o(l)
	}
	answer, err := l.gather(ctx)
	if err != nil {
		return "", err
	}
	return formatResult(ctx, client, cfg, schemaName, schema, answer, onTrace, lin)
}

// RunOption tweaks one loop.
type RunOption func(*loop)

// WithoutGroundingNudge disables the one-time repo-verification nudge.
func WithoutGroundingNudge() RunOption {
	return func(l *loop) { l.groundNudged = true }
}

// WithLeadIn inserts one developer message right after the system prompt,
// never stored in history.
func WithLeadIn(text string) RunOption {
	return func(l *loop) {
		if strings.TrimSpace(text) == "" || len(l.msgs) == 0 {
			return
		}
		l.msgs = append([]openai.ChatCompletionMessageParamUnion{l.msgs[0], devMsg(text)}, l.msgs[1:]...)
	}
}

// loop carries one gather phase: the message list plus the per-turn
// dedup/grounding state the step loop consults.
type loop struct {
	client  openai.Client
	cfg     Config
	tools   []Tool
	byName  map[string]Tool
	msgs    []openai.ChatCompletionMessageParamUnion
	maxStep int
	onTrace func(TraceEvent)
	lin     *Lineage
	// lastCall is the exact (tool, args) key of the previous call: a
	// consecutive identical call is blocked without executing (v0). Weak
	// models re-issue identical queries and each one costs an exec plus
	// full output appended to context, so the loop refuses to pay twice
	// in a row instead of nudging with prose.
	lastCall string
	// roundsDone counts executed tool rounds; groundNudged bounds the
	// grounding nudge to once per turn.
	roundsDone   int
	groundNudged bool
}

func newLoop(client openai.Client, cfg Config, tools []Tool, msgs []openai.ChatCompletionMessageParamUnion, maxSteps int, onTrace func(TraceEvent), lin *Lineage) *loop {
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}
	return &loop{client: client, cfg: cfg, tools: tools, byName: byName, msgs: msgs,
		maxStep: maxSteps, onTrace: onTrace, lin: lin}
}

func (l *loop) params() openai.ChatCompletionNewParams {
	return openai.ChatCompletionNewParams{Model: l.cfg.Model, Messages: l.msgs, Tools: sdkTools(l.tools)}
}

// gather runs the tool loop to a free-text final answer. The grounding
// and close-out nudges consume steps of the same budget, so weak models
// cannot loop forever: at most maxSteps tool rounds, one grounding
// nudge, one close-out.
func (l *loop) gather(ctx context.Context) (string, error) {
	for step := 0; step < l.maxStep; step++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		msg, err := l.request(ctx, step)
		if err != nil {
			return "", err
		}
		if len(msg.ToolCalls) == 0 {
			answer, nudge, err := l.settleText(step, strings.TrimSpace(msg.Content))
			if err != nil {
				return "", err
			}
			if !nudge {
				return answer, nil
			}
			continue
		}
		l.answerTools(ctx, step, msg)
	}
	return l.closeOut(ctx)
}

// request issues one loop call and maps wire failures to turn errors.
// Lineage keeps one light row per call: step + model on the way in,
// assistant text + requested tool calls on the way out. Full payloads
// stay in server logs (Completion slog), not in the file.
func (l *loop) request(ctx context.Context, step int) (openai.ChatCompletionMessage, error) {
	params := l.params()
	l.lin.record(LineageLLMRequest, func(ev *LineageEvent) {
		ev.Step, ev.Model = step, l.cfg.Model
		ev.Content = capLine(strings.TrimSpace(tailText(l.msgs)), 2000)
	})
	res, resRaw, err := Completion(ctx, l.client, params, l.onTrace,
		"LLM Request", "LLM Response", fmt.Sprintf("step %d", step+1), stepAttempts)
	if err != nil {
		l.lin.record(LineageError, func(ev *LineageEvent) { ev.Step, ev.Err = step, err.Error() })
		return openai.ChatCompletionMessage{}, err
	}
	l.lin.record(LineageLLMResponse, func(ev *LineageEvent) {
		ev.Step, ev.Model = step, l.cfg.Model
		if len(res.Choices) == 0 {
			return
		}
		msg := res.Choices[0].Message
		ev.Content = capLine(strings.TrimSpace(msg.Content), 8000)
		for _, tc := range msg.ToolCalls {
			if tc.Type != "function" {
				continue
			}
			ev.Calls = append(ev.Calls, LineageCall{Tool: tc.Function.Name, Args: capLine(tc.Function.Arguments, 2000)})
		}
	})
	if len(res.Choices) == 0 {
		preview := previewOf(resRaw)
		l.trace(TraceEvent{Kind: "model_error", Err: "model returned no choices (response: " + preview + ")"})
		l.lin.record(LineageError, func(ev *LineageEvent) { ev.Step, ev.Err = step, "model returned no choices" })
		return openai.ChatCompletionMessage{}, fmt.Errorf("model returned no choices on step %d (response: %s)", step+1, preview)
	}
	return res.Choices[0].Message, nil
}

// settleText handles a tool-free reply: empty answers fail, ungrounded
// ones take the one-time grounding nudge, the rest are final.
func (l *loop) settleText(step int, text string) (answer string, nudge bool, err error) {
	if text == "" {
		where := fmt.Sprintf("step %d", step+1)
		l.trace(TraceEvent{Kind: "model_error", Err: fmt.Sprintf("model returned an empty answer on %s", where)})
		l.lin.record(LineageError, func(ev *LineageEvent) { ev.Step, ev.Err = step, "model returned an empty answer" })
		return "", false, fmt.Errorf("model returned an empty answer on %s", where)
	}
	if len(l.tools) > 0 && l.roundsDone == 0 && !l.groundNudged && step < l.maxStep-1 &&
		!strings.Contains(text, "Not a codebase question") {
		l.groundNudged = true
		l.msgs = append(l.msgs, assistantMsg(text), userMsg(groundingNudge))
		return "", true, nil
	}
	l.lin.record(LineageFinalAnswer, func(ev *LineageEvent) { ev.Step, ev.Content = step, capLine(text, 16000) })
	return text, false, nil
}

// answerTools echoes the assistant turn (keeping the call ids the tool
// replies reference) and executes each call, appending fresh replies.
func (l *loop) answerTools(ctx context.Context, step int, msg openai.ChatCompletionMessage) {
	echo := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		if tc.Type != "function" {
			continue
		}
		echo = append(echo, toolCallOf(tc.ID, tc.Function.Name, tc.Function.Arguments))
	}
	text := msg.Content
	if text == "" {
		text = "(calling tools)"
	}
	l.trace(TraceEvent{Kind: "round", Round: step, Text: text})
	l.roundsDone++
	l.msgs = append(l.msgs, assistantCallsMsg(text, echo))
	for _, tc := range msg.ToolCalls {
		if tc.Type != "function" {
			continue
		}
		l.msgs = append(l.msgs, l.execTool(ctx, time.Now(), step, tc.Function.Name, tc.Function.Arguments, tc.ID))
	}
}

// maxMsgToolChars bounds one tool result entering model context. Outputs
// above this are cut with a marker telling the model to narrow down.
// Persistence caps (lineage, N.json) are separate and stay as-is: this
// bounds what the NEXT request pays for, which is the recursive-growth
// vector MaxSteps alone cannot cover.
const maxMsgToolChars = 8 * 1024

// capMsg cuts s rune-aware for model context, marking the cut so the
// model narrows its next call instead of re-requesting the same thing.
func capMsg(s string) string {
	if r := []rune(s); len(r) > maxMsgToolChars {
		return string(r[:maxMsgToolChars]) + "\n…(output truncated — narrow the range or pattern instead of repeating this call)"
	}
	return s
}

// execTool runs one call, tracing both sides. Unknown tools and failures
// feed back as tool text (never Go errors) so the model can recover.
// A consecutive identical call is blocked without executing: the reply is
// a short shape error, so repeats cost ~100 chars of context instead of
// another full output appended forever.
func (l *loop) execTool(ctx context.Context, toolStart time.Time, step int, name, args, id string) openai.ChatCompletionMessageParamUnion {
	key := name + "\x00" + args
	if key == l.lastCall {
		msg := "error: identical to the previous tool call (" + name + ") — not executed. Do not repeat it: change the tool or arguments, or answer with what you have."
		l.trace(TraceEvent{Kind: "tool_start", Round: step, Tool: name, Args: args})
		l.trace(TraceEvent{Kind: "tool_done", Round: step, Tool: name, Args: args, Err: "blocked consecutive repeat"})
		l.lin.record(LineageToolStart, func(ev *LineageEvent) {
			ev.Step, ev.Tool, ev.Args = step, name, capLine(args, 2000)
		})
		l.lin.record(LineageToolDone, func(ev *LineageEvent) {
			ev.Step, ev.Tool, ev.Args = step, name, capLine(args, 2000)
			ev.Err = "blocked consecutive repeat"
		})
		return toolReply(id, msg)
	}
	l.lastCall = key
	tool, ok := l.byName[name]
	if !ok {
		return toolReply(id, "unknown tool "+name)
	}
	l.trace(TraceEvent{Kind: "tool_start", Round: step, Tool: name, Args: args})
	l.lin.record(LineageToolStart, func(ev *LineageEvent) {
		ev.Step, ev.Tool, ev.Args = step, name, capLine(args, 2000)
	})
	out, rerr := tool.Run(ctx, args)
	if rerr != nil {
		out = "error: " + rerr.Error()
	}
	ev := TraceEvent{Kind: "tool_done", Round: step, Tool: name, Args: args, Result: out}
	if rerr != nil {
		ev.Err = rerr.Error()
	}
	l.trace(ev)
	l.lin.record(LineageToolDone, func(ev *LineageEvent) {
		ev.Step, ev.Tool, ev.Args = step, name, capLine(args, 2000)
		ev.Output, ev.DurationMs = capLine(out, 8000), time.Since(toolStart).Milliseconds()
		if rerr != nil {
			ev.Err = capLine(rerr.Error(), 4000)
		}
	})
	// The reply is what the next request pays context for: cap it here.
	// Lineage and N.json keep their own (higher) caps above.
	return toolReply(id, capMsg(out))
}

// closeOut spends one tools-free call to convert grounding into an
// answer when the model never volunteered one within budget.
func (l *loop) closeOut(ctx context.Context) (string, error) {
	l.msgs = append(l.msgs, userMsg(closeOutPrompt))
	params := openai.ChatCompletionNewParams{Model: l.cfg.Model, Messages: l.msgs}
	l.lin.record(LineageLLMRequest, func(ev *LineageEvent) {
		ev.Step, ev.Model = l.maxStep, l.cfg.Model
		ev.Content = capLine(strings.TrimSpace(tailText(l.msgs)), 2000)
	})
	res, _, err := Completion(ctx, l.client, params, l.onTrace,
		"LLM Final-answer request", "LLM Final-answer response", "final-answer call", stepAttempts)
	if err != nil {
		l.trace(TraceEvent{Kind: "model_error", Err: err.Error()})
		l.lin.record(LineageError, func(ev *LineageEvent) { ev.Err = err.Error() })
		return "", fmt.Errorf("model kept calling tools after %d steps", l.maxStep)
	}
	if res == nil || len(res.Choices) == 0 {
		l.trace(TraceEvent{Kind: "model_error", Err: fmt.Sprintf("final-answer call returned no choices after %d tool steps", l.maxStep)})
		l.lin.record(LineageError, func(ev *LineageEvent) { ev.Err = "final-answer call returned no choices" })
		return "", fmt.Errorf("model kept calling tools after %d steps", l.maxStep)
	}
	text := strings.TrimSpace(res.Choices[0].Message.Content)
	if text == "" {
		l.lin.record(LineageError, func(ev *LineageEvent) { ev.Err = "final-answer call returned empty answer" })
		return "", fmt.Errorf("model kept calling tools after %d steps", l.maxStep)
	}
	l.lin.record(LineageFinalAnswer, func(ev *LineageEvent) { ev.Content = capLine(text, 16000) })
	return text, nil
}

func (l *loop) trace(ev TraceEvent) {
	if l.onTrace != nil {
		l.onTrace(ev)
	}
}

// assembleMessages lays out the request: system prompt, rebuilt history,
// new prompt. Server-rebuilt tool turns carry an internal toolSteps array
// (never sent verbatim): each expands into an assistant(tool_calls) turn
// plus N tool-result messages with fresh, globally-unique call ids.
func assembleMessages(sysPrompt, userPrompt string, history []map[string]any) []openai.ChatCompletionMessageParamUnion {
	msgs := []openai.ChatCompletionMessageParamUnion{devMsg(sysPrompt)}
	seq := 0
	for _, h := range history {
		role, _ := h["role"].(string)
		content, _ := h["content"].(string)
		if role == "assistant" {
			if steps := parseToolSteps(h["toolSteps"]); len(steps) > 0 {
				if replay := replayCalls(content, steps, &seq); replay != nil {
					msgs = append(msgs, replay...)
				}
				continue
			}
		}
		if content == "" {
			continue
		}
		if role == "assistant" {
			msgs = append(msgs, assistantMsg(content))
		} else {
			msgs = append(msgs, userMsg(content))
		}
	}
	return append(msgs, userMsg(userPrompt))
}

// replayCalls expands one rebuilt assistant tool round into a real
// assistant(tool_calls) turn plus N tool-result messages with fresh,
// globally-unique call ids.
func replayCalls(content string, steps []replayStep, seq *int) []openai.ChatCompletionMessageParamUnion {
	text := content
	if strings.TrimSpace(text) == "" {
		text = "(calling tools)"
	}
	type kept struct{ id, tool, args, out string }
	var ks []kept
	for _, st := range steps {
		if strings.TrimSpace(st.Tool) == "" {
			continue
		}
		id := fmt.Sprintf("call_%d", *seq)
		*seq++
		out := st.Output
		if strings.TrimSpace(st.Err) != "" {
			out = "error: " + st.Err
		}
		ks = append(ks, kept{id: id, tool: st.Tool, args: st.Args, out: out})
	}
	if len(ks) == 0 {
		return nil
	}
	calls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(ks))
	for _, k := range ks {
		calls = append(calls, toolCallOf(k.id, k.tool, k.args))
	}
	msgs := []openai.ChatCompletionMessageParamUnion{assistantCallsMsg(text, calls)}
	for _, k := range ks {
		msgs = append(msgs, toolReply(k.id, k.out))
	}
	return msgs
}
