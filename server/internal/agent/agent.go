// Package agent is the reusable OpenAI-compatible loop future features
// share. Codemap is the first caller; later callers add a system prompt
// and tools without touching the loop.
//
// Only stdlib plus the official openai-go module. No frameworks.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/shared"
)

// Config is the single global model credential. One key only.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Valid reports whether codemap features may run at all.
func (c Config) Valid() bool {
	return c.BaseURL != "" && c.APIKey != "" && c.Model != ""
}

// NormalizeBaseURL trims whitespace and trailing slashes and drops a
// trailing /chat/completions, since users paste all three shapes.
func NormalizeBaseURL(raw string) string {
	u := strings.TrimSpace(raw)
	for strings.HasSuffix(u, "/") {
		u = strings.TrimSuffix(u, "/")
	}
	u = strings.TrimSuffix(u, "/chat/completions")
	return u
}

// Tool is one function the model may call. Run closes over whatever it
// needs (project container, repo dir) and receives the raw JSON args.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Run         func(ctx context.Context, argsJSON string) (string, error)
}

// NewClient builds an OpenAI client against any compatible base URL.
func NewClient(cfg Config) openai.Client {
	return openai.NewClient(
		option.WithBaseURL(NormalizeBaseURL(cfg.BaseURL)),
		option.WithAPIKey(cfg.APIKey),
	)
}

func sdkTools(tools []Tool) []openai.ChatCompletionToolUnionParam {
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema := t.Schema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, openai.ChatCompletionToolUnionParam{
			OfFunction: &openai.ChatCompletionFunctionToolParam{
				Function: shared.FunctionDefinitionParam{
					Name:        t.Name,
					Description: openai.String(t.Description),
					Parameters:  shared.FunctionParameters(schema),
					Strict:      openai.Bool(false),
				},
			},
		})
	}
	return out
}

// TestConnection verifies the endpoint accepts both tools and json_schema.
// Plain chat passing while this fails means a thin proxy or a model too
// weak for agent work, which must surface at Test time, not first codemap.
func TestConnection(ctx context.Context, cfg Config) error {
	cfg.BaseURL = NormalizeBaseURL(cfg.BaseURL)
	if !cfg.Valid() {
		return fmt.Errorf("base URL, API key, and model are all required")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client := NewClient(cfg)
	_, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: cfg.Model,
		Messages: []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Content: openai.ChatCompletionUserMessageParamContentUnion{OfString: openai.String("reply with {\"ok\":true}")},
			},
		}},
		Tools: []openai.ChatCompletionToolUnionParam{{
			OfFunction: &openai.ChatCompletionFunctionToolParam{
				Function: shared.FunctionDefinitionParam{
					Name:        "ping",
					Description: openai.String("test probe, call it"),
					Parameters:  shared.FunctionParameters(map[string]any{"type": "object", "properties": map[string]any{}}),
				},
			},
		}},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   "probe",
					Strict: openai.Bool(false),
					Schema: map[string]any{
						"type":                 "object",
						"properties":           map[string]any{"ok": map[string]any{"type": "boolean"}},
						"required":             []string{"ok"},
						"additionalProperties": false,
					},
				},
			},
		},
		MaxCompletionTokens: openai.Int(200),
	})
	if err != nil {
		if isToolError(err) {
			return fmt.Errorf("endpoint does not support tools: %w", err)
		}
		if isSchemaError(err) {
			return fmt.Errorf("endpoint does not support json_schema output: %w", err)
		}
		return err
	}
	return nil
}

func isToolError(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "tool") && (strings.Contains(s, "support") || strings.Contains(s, "unknown") || strings.Contains(s, "invalid"))
}

func isSchemaError(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "response_format") || strings.Contains(s, "json_schema")
}

// toolReply wraps model-visible tool text. Empty results still need a
// message (providers reject tool calls with no reply).
func toolReply(id, content string) openai.ChatCompletionMessageParamUnion {
	if content == "" {
		content = "(empty)"
	}
	return openai.ChatCompletionMessageParamUnion{
		OfTool: &openai.ChatCompletionToolMessageParam{
			Content:    openai.ChatCompletionToolMessageParamContentUnion{OfString: openai.String(content)},
			ToolCallID: id,
		},
	}
}

// replayStep mirrors codemap.ToolStep without importing it (agent stays
// dependency-free of callers).
type replayStep struct {
	Tool   string
	Args   string
	Output string
	Err    string
}

// parseToolSteps decodes the internal toolSteps array on rebuilt assistant
// history entries.
func parseToolSteps(v any) []replayStep {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return nil
	}
	out := make([]replayStep, 0, len(arr))
	for _, m := range arr {
		tool, _ := m["tool"].(string)
		if strings.TrimSpace(tool) == "" {
			continue
		}
		args, _ := m["args"].(string)
		output, _ := m["output"].(string)
		errStr, _ := m["error"].(string)
		out = append(out, replayStep{Tool: tool, Args: args, Output: output, Err: errStr})
	}
	return out
}
