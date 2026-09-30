package agent

import (
	"context"
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
		lin.Model = cfg.Model
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
		lin.record(LineageLLMRequest, func(ev *LineageEvent) {
			ev.Model = cfg.Model
			ev.Content = capLine(strings.TrimSpace(userPrompt), 2000)
		})
	}
	res, _, err := Completion(ctx, client, params, nil,
		"LLM Structured request", "LLM Structured response", "structured call", 1)
	if err != nil {
		if lin != nil {
			lin.record(LineageError, func(ev *LineageEvent) { ev.Err = err.Error() })
		}
		return "", err
	}
	if lin != nil {
		lin.record(LineageLLMResponse, func(ev *LineageEvent) {
			ev.Model = cfg.Model
			if res != nil && len(res.Choices) > 0 {
				ev.Content = capLine(strings.TrimSpace(res.Choices[0].Message.Content), 2000)
			}
		})
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
