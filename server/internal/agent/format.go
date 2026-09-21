package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/shared"
)

// extractorPrompt describes the tier's job and its success state: a flow
// map in execution order, shaped like the example. The evidence fields
// are named because only their semantics are ours to define; everything
// enforceable lives in the response schema, not here.
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
// follow-up call. The tool loop must stay schema-free (providers null out
// choices or skip tool calls when schema rides with tools); this call
// carries no tools so the schema applies cleanly. Structured output is
// never faked: exactly one schema-enforced attempt, no retry, no
// schema-free conversion. Failure aborts the turn.
func formatResult(ctx context.Context, client openai.Client, cfg Config, schemaName string, schema map[string]any, out string, onTrace func(TraceEvent), lin *Lineage) (string, error) {
	if schema == nil {
		return out, nil
	}
	snapshot := extractorSnapshot(lin, out)
	if lin != nil {
		lin.PrunedTier1Data = snapshot
	}
	fparams := openai.ChatCompletionNewParams{
		Model: cfg.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
				Content: openai.ChatCompletionDeveloperMessageParamContentUnion{OfString: openai.String(extractorPrompt)},
			},
		}, {
			OfUser: &openai.ChatCompletionUserMessageParam{
				Content: openai.ChatCompletionUserMessageParamContentUnion{OfString: openai.String(mustJSON(snapshot))},
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
	// One attempt only. Models without structured-output support fail
	// deterministically. There is no raw-text fallback.
	freqRaw, _ := json.Marshal(fparams)
	lin.record("format_request", func(ev *LineageEvent) {
		ev.Payload = payloadOf(freqRaw)
	})
	res, responseRaw, err := Completion(ctx, client, fparams, onTrace,
		"LLM Format request", "LLM Format response", "format call", 1)
	if err != nil {
		if onTrace != nil {
			onTrace(TraceEvent{Kind: "model_error", Err: "structuredExtractor failed: " + err.Error()})
		}
		lin.record("format_request", func(ev *LineageEvent) {
			ev.Err = err.Error()
		})
		return "", err
	}
	if res == nil || len(res.Choices) == 0 {
		if onTrace != nil {
			onTrace(TraceEvent{Kind: "model_error", Err: "structuredExtractor returned no choices"})
		}
		lin.record("format_response", func(ev *LineageEvent) {
			ev.Err = "no choices"
		})
		return "", fmt.Errorf("structuredExtractor returned no choices")
	}
	formatted := strings.TrimSpace(res.Choices[0].Message.Content)
	if formatted == "" {
		if onTrace != nil {
			onTrace(TraceEvent{Kind: "model_error", Err: "structuredExtractor returned empty answer"})
		}
		lin.record("format_response", func(ev *LineageEvent) {
			ev.Err = "empty answer"
		})
		return "", fmt.Errorf("structuredExtractor returned empty answer")
	}
	lin.record("format_response", func(ev *LineageEvent) {
		ev.Content = capLine(formatted, 16000)
		ev.Payload = payloadOf(responseRaw)
	})
	return formatted, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
