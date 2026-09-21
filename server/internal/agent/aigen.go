// One-shot structured generation: a single tools-free json_schema
// completion for callers that need raw JSON (commit messages, PR
// descriptions). maxAttempts=2 covers transport-level retries only; the
// schema call itself is a single attempt with no raw-text fallback (same
// contract as the extractor format call in format.go).
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/shared"
)

func CompleteJSON(ctx context.Context, cfg Config, system, user, schemaName string, schema map[string]any) (json.RawMessage, error) {
	if !cfg.Valid() {
		return nil, fmt.Errorf("ai not configured")
	}
	client := NewClient(cfg)
	params := openai.ChatCompletionNewParams{
		Model: cfg.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			devMsg(system),
			userMsg(user),
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
		"AI generation request", "AI generation response", "aigen call", 2)
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.Choices) == 0 {
		return nil, fmt.Errorf("generation returned no choices")
	}
	raw := []byte(res.Choices[0].Message.Content)
	if len(raw) == 0 {
		return nil, fmt.Errorf("generation returned empty answer")
	}
	return json.RawMessage(raw), nil
}
