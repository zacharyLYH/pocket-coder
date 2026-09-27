package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/shared"
)

// Structured asks one tools-free question with a json_schema response.
// One attempt only; callers decide what a failure means.
func Structured(ctx context.Context, cfg Config, sysPrompt, userPrompt, schemaName string, schema map[string]any, lin *Lineage) (string, error) {
	if !cfg.Valid() {
		return "", fmt.Errorf("ai not configured")
	}
	client := NewClient(cfg)
	if lin != nil {
		lin.InitialRequest = map[string]any{"userPrompt": userPrompt, "model": cfg.Model}
	}
	params := openai.ChatCompletionNewParams{
		Model: cfg.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			devMsg(sysPrompt),
			userMsg(userPrompt),
		},
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
	if lin != nil {
		raw, _ := json.Marshal(params)
		lin.record("llm_request", func(ev *LineageEvent) { ev.Payload = payloadOf(raw) })
	}
	res, resRaw, err := Completion(ctx, client, params, nil,
		"LLM Structured request", "LLM Structured response", "structured call", 1)
	if err != nil {
		if lin != nil {
			lin.record("error", func(ev *LineageEvent) { ev.Err = err.Error() })
		}
		return "", err
	}
	if lin != nil {
		lin.record("llm_response", func(ev *LineageEvent) { ev.Payload = payloadOf(resRaw) })
	}
	if res == nil || len(res.Choices) == 0 {
		return "", fmt.Errorf("structured call returned no choices")
	}
	out := strings.TrimSpace(res.Choices[0].Message.Content)
	if out == "" {
		return "", fmt.Errorf("structured call returned empty answer")
	}
	return out, nil
}
