package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v2"
)

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
	msgs := []openai.ChatCompletionMessageParamUnion{{
		OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
			Content: openai.ChatCompletionDeveloperMessageParamContentUnion{OfString: openai.String(sysPrompt)},
		},
	}}
	callSeq := 0
	for _, h := range history {
		role, _ := h["role"].(string)
		content, _ := h["content"].(string)
		// Server-rebuilt tool turns carry an internal toolSteps array
		// (never sent to the model verbatim): expand into a real
		// assistant(tool_calls) + N tool-result messages with fresh,
		// globally-unique call ids.
		if role == "assistant" {
			if steps := parseToolSteps(h["toolSteps"]); len(steps) > 0 {
				text := content
				if strings.TrimSpace(text) == "" {
					text = "(calling tools)"
				}
				type kept struct {
					id   string
					tool string
					args string
					out  string
				}
				var ks []kept
				for _, st := range steps {
					if strings.TrimSpace(st.Tool) == "" {
						continue
					}
					id := fmt.Sprintf("call_%d", callSeq)
					callSeq++
					out := st.Output
					if strings.TrimSpace(st.Err) != "" {
						out = "error: " + st.Err
					}
					ks = append(ks, kept{id: id, tool: st.Tool, args: st.Args, out: out})
				}
				if len(ks) == 0 {
					continue
				}
				calls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(ks))
				for _, k := range ks {
					calls = append(calls, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID: k.id,
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      k.tool,
								Arguments: k.args,
							},
						},
					})
				}
				msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content:   openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(text)},
						ToolCalls: calls,
					},
				})
				for _, k := range ks {
					msgs = append(msgs, toolReply(k.id, k.out))
				}
				continue
			}
		}
		if content == "" {
			continue
		}
		if role == "assistant" {
			msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Content: openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(content)},
				},
			})
		} else {
			msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Content: openai.ChatCompletionUserMessageParamContentUnion{OfString: openai.String(content)},
				},
			})
		}
	}
	msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
		OfUser: &openai.ChatCompletionUserMessageParam{
			Content: openai.ChatCompletionUserMessageParamContentUnion{OfString: openai.String(userPrompt)},
		},
	})

	byName := map[string]Tool{}
	for _, t := range tools {
		byName[t.Name] = t
	}
	// seenCalls counts exact (tool, args) repeats within the turn. Live
	// runs show weak models re-issuing identical failing queries; the
	// repeat note steers them off the loop. Model-only: persisted tool
	// outputs (trace events) keep the raw result.
	seenCalls := map[string]int{}
	// roundsDone counts executed tool rounds; groundNudged bounds the
	// structural grounding nudge to once per turn.
	roundsDone := 0
	groundNudged := false
	var params openai.ChatCompletionNewParams
	params.Model = cfg.Model
	params.Tools = sdkTools(tools)
	// NOTE: response_format is deliberately NOT set on the tool loop.
	// Providers (verified on free reasoning gateways) return choices:null
	// or drop tool calls when json_schema rides along with tools. The
	// schema is enforced once, on a separate formatting call after the
	// loop produces its final text (see below).

	// Sanity record of the exact initial request: full system prompt,
	// tool definitions, and history as the model will see them. It goes
	// to the lineage graph only — attemptCompletion already logs every
	// wire call, and a second full-payload log line here doubled log
	// volume for no extra information.
	{
		params.Messages = msgs
		initRaw, _ := json.MarshalIndent(params, "", "  ")
		if lin != nil {
			lin.InitialRequest = payloadOf(initRaw)
		}
	}

	for step := 0; step < maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		params.Messages = msgs
		requestRaw, _ := json.Marshal(params)
		lin.record("llm_request", func(ev *LineageEvent) { ev.Step, ev.Payload = step, payloadOf(requestRaw) })

		res, resRaw, err := attemptCompletion(ctx, client, params, onTrace,
			"LLM Request", "LLM Response", fmt.Sprintf("step %d", step+1), stepAttempts)
		if err != nil {
			lin.record("error", func(ev *LineageEvent) {
				ev.Step, ev.Err = step, err.Error()
			})
			return "", err
		}
		lin.record("llm_response", func(ev *LineageEvent) {
			ev.Step, ev.Payload = step, payloadOf(resRaw)
		})

		if len(res.Choices) == 0 {
			// Providers (notably free gateways) can answer HTTP 200 with
			// choices:null and no SDK error. Surface the raw payload so
			// the caller can tell provider-empty apart from our bugs.
			preview := strings.TrimSpace(string(resRaw))
			const max = 500
			if len(preview) > max {
				preview = preview[:max] + "…"
			}
			if onTrace != nil {
				onTrace(TraceEvent{Kind: "model_error", Err: "model returned no choices (response: " + preview + ")"})
			}
			lin.record("error", func(ev *LineageEvent) {
				ev.Step, ev.Err = step, "model returned no choices"
			})
			return "", fmt.Errorf("model returned no choices on step %d (response: %s)", step+1, preview)
		}
		msg := res.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			out := strings.TrimSpace(msg.Content)
			stepWhere := fmt.Sprintf("step %d", step+1)
			if out == "" {
				if onTrace != nil {
					onTrace(TraceEvent{Kind: "model_error", Err: fmt.Sprintf("model returned an empty answer on %s", stepWhere)})
				}
				lin.record("error", func(ev *LineageEvent) {
					ev.Step, ev.Err = step, "model returned an empty answer"
				})
				return "", fmt.Errorf("model returned an empty answer on %s", stepWhere)
			}
			// Structural grounding rule: a final answer with zero tool
			// rounds is ungrounded by construction. Nudge once (with an
			// explicit repo-irrelevant escape) instead of prose-begging
			// every turn. Bounded: one nudge, then accept. Answers that
			// already declare the question repo-irrelevant skip the nudge.
			if len(tools) > 0 && roundsDone == 0 && !groundNudged && step < maxSteps-1 &&
				!strings.Contains(out, "Not a codebase question") {
				groundNudged = true
				msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(out)},
					},
				}, openai.ChatCompletionMessageParamUnion{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Content: openai.ChatCompletionUserMessageParamContentUnion{OfString: openai.String(
							"Before answering, ground your answer: call at least one available tool to verify against the repo. If the question has nothing to do with this repo's code, answer directly instead.")},
					},
				})
				continue
			}
			lin.record("final_answer", func(ev *LineageEvent) {
				ev.Step, ev.Content = step, capLine(out, 16000)
			})
			formatted, ferr := formatResult(ctx, client, cfg, schemaName, schema, out, onTrace, lin)
			if ferr != nil {
				if lin != nil {
					lin.Error = ferr.Error()
				}
				return "", ferr
			}
			return formatted, nil
		}
		// Echo the assistant turn with its tool calls so the next
		// request keeps the call ids the tool replies reference.
		echo := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			if tc.Type != "function" {
				continue
			}
			echo = append(echo, openai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID: tc.ID,
					Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				},
			})
		}
		assistantText := msg.Content
		if assistantText == "" {
			assistantText = "(calling tools)"
		}
		if onTrace != nil {
			onTrace(TraceEvent{Kind: "round", Round: step, Text: assistantText})
		}
		roundsDone++
		msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
			OfAssistant: &openai.ChatCompletionAssistantMessageParam{
				Content:   openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(assistantText)},
				ToolCalls: echo,
			},
		})
		for _, tc := range msg.ToolCalls {
			if tc.Type != "function" {
				continue
			}
			tool, ok := byName[tc.Function.Name]
			if !ok {
				msgs = append(msgs, toolReply(tc.ID, "unknown tool "+tc.Function.Name))
				continue
			}
			if onTrace != nil {
				onTrace(TraceEvent{Kind: "tool_start", Round: step, Tool: tool.Name, Args: tc.Function.Arguments})
			}
			lin.record("tool_start", func(ev *LineageEvent) {
				ev.Step, ev.Round, ev.Tool, ev.Args = step, step, tool.Name, tc.Function.Arguments
			})
			toolStart := time.Now()
			out, rerr := tool.Run(ctx, tc.Function.Arguments)
			if rerr != nil {
				out = "error: " + rerr.Error()
			}
			if onTrace != nil {
				ev := TraceEvent{Kind: "tool_done", Round: step, Tool: tool.Name, Args: tc.Function.Arguments, Result: out}
				if rerr != nil {
					ev.Err = rerr.Error()
				}
				onTrace(ev)
			}
			lin.record("tool_done", func(ev *LineageEvent) {
				ev.Step, ev.Round, ev.Tool, ev.Args = step, step, tool.Name, tc.Function.Arguments
				ev.Output, ev.DurationMs = capLine(out, 8000), time.Since(toolStart).Milliseconds()
				if rerr != nil {
					ev.Err = capLine(rerr.Error(), 4000)
				}
			})
			fedOut := out
			callKey := tool.Name + "\x00" + tc.Function.Arguments
			if seenCalls[callKey] > 0 && rerr == nil {
				fedOut += fmt.Sprintf("\n(note: this exact call already ran %d time(s) with the same result — try a different pattern, path, or tool instead of repeating it)", seenCalls[callKey])
			}
			seenCalls[callKey]++
			msgs = append(msgs, toolReply(tc.ID, fedOut))
		}
	}
	// The model never volunteered a final answer within maxSteps. Nudge
	// it once, tools-free, so rounds of grounding still become an answer
	// instead of a failed turn.
	msgs = append(msgs, openai.ChatCompletionMessageParamUnion{
		OfUser: &openai.ChatCompletionUserMessageParam{
			Content: openai.ChatCompletionUserMessageParamContentUnion{OfString: openai.String(
				"You have used all available tool rounds. Provide your final answer now based on everything gathered — no more tool calls.")},
		},
	})
	finalParams := openai.ChatCompletionNewParams{Model: cfg.Model, Messages: msgs}
	finalRaw, _ := json.Marshal(finalParams)
	lin.record("llm_request", func(ev *LineageEvent) { ev.Step, ev.Payload = maxSteps, payloadOf(finalRaw) })
	fres, _, ferr := attemptCompletion(ctx, client, finalParams, onTrace,
		"LLM Final-answer request", "LLM Final-answer response", "final-answer call", stepAttempts)
	if ferr != nil {
		if onTrace != nil {
			onTrace(TraceEvent{Kind: "model_error", Err: ferr.Error()})
		}
		lin.record("error", func(ev *LineageEvent) {
			ev.Err = ferr.Error()
		})
		return "", fmt.Errorf("model kept calling tools after %d steps", maxSteps)
	}
	if fres == nil || len(fres.Choices) == 0 {
		if onTrace != nil {
			onTrace(TraceEvent{Kind: "model_error", Err: fmt.Sprintf("final-answer call returned no choices after %d tool steps", maxSteps)})
		}
		lin.record("error", func(ev *LineageEvent) {
			ev.Err = "final-answer call returned no choices"
		})
		return "", fmt.Errorf("model kept calling tools after %d steps", maxSteps)
	}
	out := strings.TrimSpace(fres.Choices[0].Message.Content)
	if out == "" {
		lin.record("error", func(ev *LineageEvent) {
			ev.Err = "final-answer call returned empty answer"
		})
		return "", fmt.Errorf("model kept calling tools after %d steps", maxSteps)
	}
	lin.record("final_answer", func(ev *LineageEvent) {
		ev.Content = capLine(out, 16000)
	})
	formatted, ferr := formatResult(ctx, client, cfg, schemaName, schema, out, onTrace, lin)
	if ferr != nil {
		if lin != nil {
			lin.Error = ferr.Error()
		}
		return "", ferr
	}
	return formatted, nil
}
