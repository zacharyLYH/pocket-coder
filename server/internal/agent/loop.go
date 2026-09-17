package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v2"
)

// Model-facing prompts for the gather phase. Tier-2's extractor prompt
// lives separately in format.go; the two evolve independently.
const (
	// groundingNudge steers ungrounded answers (zero tool rounds) toward
	// one verification pass instead of prose-begging every turn.
	groundingNudge = "Before answering, ground your answer: call at least one available tool to verify against the repo. If the question has nothing to do with this repo's code, answer directly instead."
	// closeOutPrompt turns maxSteps exhaustion into an answer: one
	// tools-free call over everything gathered so far.
	closeOutPrompt = "You have used all available tool rounds. Provide your final answer now based on everything gathered — no more tool calls."
)

// Run answers one prompt: gather free text via the tool loop, then shape
// it once through the schema-enforcing format call. The loop never
// carries a schema (providers drop tool calls or null out choices when
// json_schema rides with tools); the format call never carries tools.
func Run(ctx context.Context, cfg Config, sysPrompt, userPrompt string, history []map[string]any, tools []Tool, schemaName string, schema map[string]any, maxSteps int, onTrace func(TraceEvent), lin *Lineage) (string, error) {
	if !cfg.Valid() {
		return "", fmt.Errorf("ai not configured")
	}
	if maxSteps <= 0 {
		maxSteps = 8
	}
	client := NewClient(cfg)
	if lin != nil {
		lin.InitialRequest = map[string]any{"userPrompt": userPrompt, "model": cfg.Model}
	}
	answer, err := newLoop(client, cfg, tools, assembleMessages(sysPrompt, userPrompt, history), maxSteps, onTrace, lin).gather(ctx)
	if err != nil {
		return "", err
	}
	return formatResult(ctx, client, cfg, schemaName, schema, answer, onTrace, lin)
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
	// seenCalls counts exact (tool, args) repeats: weak models re-issue
	// identical failing queries, so the repeat note steers them off.
	seenCalls map[string]int
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
		maxStep: maxSteps, onTrace: onTrace, lin: lin, seenCalls: map[string]int{}}
}

func (l *loop) params() openai.ChatCompletionNewParams {
	return openai.ChatCompletionNewParams{Model: l.cfg.Model, Messages: l.msgs, Tools: sdkTools(l.tools)}
}

// gather runs the tool loop to a free-text final answer. Nudges (grounding,
// close-out) consume steps of the same budget, so weak models cannot loop
// forever: at most maxSteps tool rounds, one grounding nudge, one close-out.
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
// The raw payload surfaces in the error so provider-empty (choices:null)
// reads apart from our bugs.
func (l *loop) request(ctx context.Context, step int) (openai.ChatCompletionMessage, error) {
	params := l.params()
	requestRaw, _ := jsonOf(params)
	l.lin.record("llm_request", func(ev *LineageEvent) { ev.Step, ev.Payload = step, payloadOf(requestRaw) })
	res, resRaw, err := attemptCompletion(ctx, l.client, params, l.onTrace,
		"LLM Request", "LLM Response", fmt.Sprintf("step %d", step+1), stepAttempts)
	if err != nil {
		l.lin.record("error", func(ev *LineageEvent) { ev.Step, ev.Err = step, err.Error() })
		return openai.ChatCompletionMessage{}, err
	}
	l.lin.record("llm_response", func(ev *LineageEvent) { ev.Step, ev.Payload = step, payloadOf(resRaw) })
	if len(res.Choices) == 0 {
		preview := previewOf(resRaw)
		l.trace(TraceEvent{Kind: "model_error", Err: "model returned no choices (response: " + preview + ")"})
		l.lin.record("error", func(ev *LineageEvent) { ev.Step, ev.Err = step, "model returned no choices" })
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
		l.lin.record("error", func(ev *LineageEvent) { ev.Step, ev.Err = step, "model returned an empty answer" })
		return "", false, fmt.Errorf("model returned an empty answer on %s", where)
	}
	if len(l.tools) > 0 && l.roundsDone == 0 && !l.groundNudged && step < l.maxStep-1 &&
		!strings.Contains(text, "Not a codebase question") {
		l.groundNudged = true
		l.msgs = append(l.msgs, assistantMsg(text), userMsg(groundingNudge))
		return "", true, nil
	}
	l.lin.record("final_answer", func(ev *LineageEvent) { ev.Step, ev.Content = step, capLine(text, 16000) })
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

// execTool runs one call, tracing both sides. Unknown tools and failures
// feed back as tool text (never Go errors) so the model can recover.
func (l *loop) execTool(ctx context.Context, toolStart time.Time, step int, name, args, id string) openai.ChatCompletionMessageParamUnion {
	tool, ok := l.byName[name]
	if !ok {
		return toolReply(id, "unknown tool "+name)
	}
	l.trace(TraceEvent{Kind: "tool_start", Round: step, Tool: name, Args: args})
	l.lin.record("tool_start", func(ev *LineageEvent) {
		ev.Step, ev.Round, ev.Tool, ev.Args = step, step, name, args
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
	l.lin.record("tool_done", func(ev *LineageEvent) {
		ev.Step, ev.Round, ev.Tool, ev.Args = step, step, name, args
		ev.Output, ev.DurationMs = capLine(out, 8000), time.Since(toolStart).Milliseconds()
		if rerr != nil {
			ev.Err = capLine(rerr.Error(), 4000)
		}
	})
	key := name + "\x00" + args
	if l.seenCalls[key] > 0 && rerr == nil {
		out += fmt.Sprintf("\n(note: this exact call already ran %d time(s) with the same result — try a different pattern, path, or tool instead of repeating it)", l.seenCalls[key])
	}
	l.seenCalls[key]++
	return toolReply(id, out)
}

// closeOut spends one tools-free call to convert grounding into an
// answer when the model never volunteered one within budget.
func (l *loop) closeOut(ctx context.Context) (string, error) {
	l.msgs = append(l.msgs, userMsg(closeOutPrompt))
	params := openai.ChatCompletionNewParams{Model: l.cfg.Model, Messages: l.msgs}
	finalRaw, _ := jsonOf(params)
	l.lin.record("llm_request", func(ev *LineageEvent) { ev.Step, ev.Payload = l.maxStep, payloadOf(finalRaw) })
	res, _, err := attemptCompletion(ctx, l.client, params, l.onTrace,
		"LLM Final-answer request", "LLM Final-answer response", "final-answer call", stepAttempts)
	if err != nil {
		l.trace(TraceEvent{Kind: "model_error", Err: err.Error()})
		l.lin.record("error", func(ev *LineageEvent) { ev.Err = err.Error() })
		return "", fmt.Errorf("model kept calling tools after %d steps", l.maxStep)
	}
	if res == nil || len(res.Choices) == 0 {
		l.trace(TraceEvent{Kind: "model_error", Err: fmt.Sprintf("final-answer call returned no choices after %d tool steps", l.maxStep)})
		l.lin.record("error", func(ev *LineageEvent) { ev.Err = "final-answer call returned no choices" })
		return "", fmt.Errorf("model kept calling tools after %d steps", l.maxStep)
	}
	text := strings.TrimSpace(res.Choices[0].Message.Content)
	if text == "" {
		l.lin.record("error", func(ev *LineageEvent) { ev.Err = "final-answer call returned empty answer" })
		return "", fmt.Errorf("model kept calling tools after %d steps", l.maxStep)
	}
	l.lin.record("final_answer", func(ev *LineageEvent) { ev.Content = capLine(text, 16000) })
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
// globally-unique call ids. Rounds with no named calls replay as nothing.
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
