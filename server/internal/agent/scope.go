package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/shared"
)

// Structured asks one tools-free question with a json_schema response and
// returns the raw JSON text. One attempt only: callers decide what a
// failure means (butler fails open into the tool loop, whose guide still
// governs). Strict stays false like everywhere else: many user-brought
// endpoints 400 on strict schemas.
func Structured(ctx context.Context, cfg Config, sysPrompt, userPrompt, schemaName string, schema map[string]any) (string, error) {
	if !cfg.Valid() {
		return "", fmt.Errorf("ai not configured")
	}
	client := NewClient(cfg)
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
	res, _, err := Completion(ctx, client, params, nil,
		"LLM Structured request", "LLM Structured response", "structured call", 1)
	if err != nil {
		return "", err
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
