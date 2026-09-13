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
	"log/slog"
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

// TraceEvent is one debuggable moment of a run: a tool starting with
// its full args, a tool finishing with a result preview, or a model
// failure. Callers plog these so runs are inspectable after the fact.
type TraceEvent struct {
	Kind   string // "tool_start" | "tool_done" | "model_error"
	Tool   string
	Args   string
	Result string
	Err    string
}

// Run executes the turn cycle: send messages, run tool calls, append
// results, repeat until the model answers without tools or maxSteps hits.
// onTrace receives every tool start/done plus model failures; it may be
// nil when the caller does not care.
func Run(ctx context.Context, cfg Config, sysPrompt, userPrompt string, history []map[string]any, tools []Tool, schemaName string, schema map[string]any, maxSteps int, onTrace func(TraceEvent)) (string, error) {
	if !cfg.Valid() {
		return "", fmt.Errorf("ai not configured")
	}
	if maxSteps <= 0 {
		maxSteps = 8
	}
	client := NewClient(cfg)
	msgs := []openai.ChatCompletionMessageParamUnion{{
		OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
			Content: openai.ChatCompletionDeveloperMessageParamContentUnion{OfString: openai.String(sysPrompt)},
		},
	}}
	for _, h := range history {
		role, _ := h["role"].(string)
		content, _ := h["content"].(string)
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
	var params openai.ChatCompletionNewParams
	params.Model = cfg.Model
	params.Tools = sdkTools(tools)
	if schema != nil {
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   schemaName,
					Strict: openai.Bool(false),
					Schema: schema,
				},
			},
		}
	}

	for step := 0; step < maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		params.Messages = msgs
		
		reqRaw, _ := json.Marshal(params)
		slog.Info("LLM Request", "payload", string(reqRaw))

		stepCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		res, err := client.Chat.Completions.New(stepCtx, params)
		cancel()
		if err != nil {
			if onTrace != nil {
				onTrace(TraceEvent{Kind: "model_error", Err: err.Error()})
			}
			return "", err
		}
		
		resRaw, _ := json.Marshal(res)
		slog.Info("LLM Response", "payload", string(resRaw))

		if len(res.Choices) == 0 {
			return "", fmt.Errorf("model returned no choices")
		}
		msg := res.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			out := strings.TrimSpace(msg.Content)
			if out == "" {
				return "", fmt.Errorf("model returned an empty answer")
			}
			return out, nil
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
				onTrace(TraceEvent{Kind: "tool_start", Tool: tool.Name, Args: tc.Function.Arguments})
			}
			out, rerr := tool.Run(ctx, tc.Function.Arguments)
			if rerr != nil {
				out = "error: " + rerr.Error()
			}
			if onTrace != nil {
				ev := TraceEvent{Kind: "tool_done", Tool: tool.Name, Args: tc.Function.Arguments, Result: out}
				if rerr != nil {
					ev.Err = rerr.Error()
				}
				onTrace(ev)
			}
			msgs = append(msgs, toolReply(tc.ID, out))
		}
	}
	return "", fmt.Errorf("model kept calling tools after %d steps", maxSteps)
}

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
