package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/shared"
)

// extractorPrompt carries only what the response schema cannot enforce.
const extractorPrompt = "You are the structuredExtractor tier. Produce only JSON matching the response schema. " +
	"The evidence has two fields: answer holds the findings — section titles and summaries come from THIS; " +
	"events is the tool log — use it only to preserve valid refs, never turn tool calls, reads, or searches into sections. " +
	"Write the answer as a flow map: 2-8 sections in execution order, entrypoint first and downstream next, each shaped like the example. " +
	"Title names the step's finding, never an action or a bare filename. Summary is one or two sentences naming the exact functions " +
	"involved and the handoff between them (calls, emits, writes to). Every section carries the exact lines backing it as refs; " +
	"omit a section no lines back. Prune chatter and do not invent refs, paths, or line numbers. " +
	"Ref paths must be repo-relative, such as server/cmd/server/main.go, never /server/... or another absolute path. " +
	"Preserve valid refs from tool evidence. " +
	`Example shape: {"sections":[{"title":"Login submits credentials","summary":"handleLogin() validates input and calls SessionService.create().","refs":[{"path":"src/auth.js","startLine":10,"endLine":14,"function":"handleLogin"}]},{"title":"Session is created","summary":"SessionService.create() writes the row and emits session.created.","refs":[{"path":"src/session.js","startLine":40,"endLine":52,"function":"create"}]}]}`

// formatResult enforces the json_schema exactly once, on a tools-free
// follow-up call (providers null out choices or skip tool calls when a
// schema rides with tools). Structured output is never faked: one
// schema-enforced attempt, no retry, no fallback. Failure aborts the turn.
func formatResult(ctx context.Context, client openai.Client, cfg Config, schemaName string, schema map[string]any, out string, onTrace func(TraceEvent), lin *Lineage) (string, error) {
	if schema == nil {
		return out, nil
	}
	// Evidence for the extractor rides the wire only — it is not stored
	// in lineage. The file keeps one light format_request/response pair.
	evidence := map[string]any{"answer": capLine(out, 16000)}
	if lin != nil {
		evs := make([]map[string]any, 0, len(lin.Events))
		for _, ev := range lin.Events {
			item := map[string]any{"ts": ev.TS, "kind": ev.Kind}
			if ev.Step != 0 {
				item["step"] = ev.Step
			}
			if ev.Tool != "" {
				item["tool"] = ev.Tool
			}
			if ev.Args != "" {
				item["args"] = capLine(ev.Args, 2000)
			}
			if ev.Output != "" {
				item["output"] = capLine(ev.Output, 6000)
			}
			if ev.Content != "" {
				item["content"] = capLine(ev.Content, 8000)
			}
			if len(ev.Calls) > 0 {
				item["calls"] = ev.Calls
			}
			if ev.Err != "" {
				item["error"] = capLine(ev.Err, 2000)
			}
			evs = append(evs, item)
		}
		evidence["events"] = evs
	}
	snapRaw, _ := jsonOf(evidence)
	fparams := openai.ChatCompletionNewParams{
		Model: cfg.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
				Content: openai.ChatCompletionDeveloperMessageParamContentUnion{OfString: openai.String(extractorPrompt)},
			},
		}, {
			OfUser: &openai.ChatCompletionUserMessageParam{
				Content: openai.ChatCompletionUserMessageParamContentUnion{OfString: openai.String(string(snapRaw))},
			},
		}},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   schemaName,
					Strict: openai.Bool(false),
					Schema: schema,
				},
			},
		},
	}
	// One attempt only; no raw-text fallback.
	lin.record(LineageFormatRequest, func(ev *LineageEvent) {
		ev.Model = cfg.Model
	})
	res, _, err := Completion(ctx, client, fparams, onTrace,
		"LLM Format request", "LLM Format response", "format call", 1)
	if err != nil {
		if onTrace != nil {
			onTrace(TraceEvent{Kind: "model_error", Err: "structuredExtractor failed: " + err.Error()})
		}
		lin.record(LineageFormatRequest, func(ev *LineageEvent) {
			ev.Err = err.Error()
		})
		return "", err
	}
	if res == nil || len(res.Choices) == 0 {
		if onTrace != nil {
			onTrace(TraceEvent{Kind: "model_error", Err: "structuredExtractor returned no choices"})
		}
		lin.record(LineageFormatResp, func(ev *LineageEvent) {
			ev.Err = "no choices"
		})
		return "", fmt.Errorf("structuredExtractor returned no choices")
	}
	formatted := strings.TrimSpace(res.Choices[0].Message.Content)
	if formatted == "" {
		if onTrace != nil {
			onTrace(TraceEvent{Kind: "model_error", Err: "structuredExtractor returned empty answer"})
		}
		lin.record(LineageFormatResp, func(ev *LineageEvent) {
			ev.Err = "empty answer"
		})
		return "", fmt.Errorf("structuredExtractor returned empty answer")
	}
	lin.record(LineageFormatResp, func(ev *LineageEvent) {
		ev.Model = cfg.Model
		ev.Content = capLine(formatted, 16000)
	})
	return formatted, nil
}
