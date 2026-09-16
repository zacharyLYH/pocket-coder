package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/shared"
)

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
		lin.ExtractorInput = snapshot
	}
	fparams := openai.ChatCompletionNewParams{
		Model: cfg.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
				Content: openai.ChatCompletionDeveloperMessageParamContentUnion{OfString: openai.String(
					"You are the structuredExtractor tier. Produce only JSON matching the response schema. Use the sanitized tier-1 evidence below; prune chatter and do not invent refs, paths, or line numbers. Ref paths must be repo-relative, such as server/cmd/server/main.go, never /server/... or another absolute path. Preserve valid refs from tool evidence.")},
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
	res, responseRaw, err := attemptCompletion(ctx, client, fparams, onTrace,
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
