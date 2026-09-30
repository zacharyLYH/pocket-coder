package agent

import (
	"encoding/json"
	"strings"

	"github.com/openai/openai-go/v2"
)

// Message constructors: the SDK union types bury one string under four
// nesting levels, so every call site would repeat the same shape.
func devMsg(text string) openai.ChatCompletionMessageParamUnion {
	return openai.ChatCompletionMessageParamUnion{
		OfDeveloper: &openai.ChatCompletionDeveloperMessageParam{
			Content: openai.ChatCompletionDeveloperMessageParamContentUnion{OfString: openai.String(text)},
		},
	}
}

func userMsg(text string) openai.ChatCompletionMessageParamUnion {
	return openai.ChatCompletionMessageParamUnion{
		OfUser: &openai.ChatCompletionUserMessageParam{
			Content: openai.ChatCompletionUserMessageParamContentUnion{OfString: openai.String(text)},
		},
	}
}

func assistantMsg(text string) openai.ChatCompletionMessageParamUnion {
	return openai.ChatCompletionMessageParamUnion{
		OfAssistant: &openai.ChatCompletionAssistantMessageParam{
			Content: openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(text)},
		},
	}
}

func assistantCallsMsg(text string, calls []openai.ChatCompletionMessageToolCallUnionParam) openai.ChatCompletionMessageParamUnion {
	return openai.ChatCompletionMessageParamUnion{
		OfAssistant: &openai.ChatCompletionAssistantMessageParam{
			Content:   openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(text)},
			ToolCalls: calls,
		},
	}
}

func toolCallOf(id, name, args string) openai.ChatCompletionMessageToolCallUnionParam {
	return openai.ChatCompletionMessageToolCallUnionParam{
		OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
			ID: id,
			Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
				Name:      name,
				Arguments: args,
			},
		},
	}
}

func jsonOf(v any) ([]byte, error) {
	return json.Marshal(v)
}

// tailText best-efforts the visible text of the newest outgoing message —
// the new information driving this LLM call (user prompt on step 0, last
// tool reply later). The SDK buries text under role-specific unions, so
// decode generically and take a string "content" when present.
func tailText(msgs []openai.ChatCompletionMessageParamUnion) string {
	if len(msgs) == 0 {
		return ""
	}
	raw, err := jsonOf(msgs[len(msgs)-1])
	if err != nil {
		return ""
	}
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	if s, ok := v["content"].(string); ok {
		return s
	}
	if v["content"] != nil {
		c, _ := jsonOf(v["content"])
		return string(c)
	}
	return ""
}

// previewOf trims a wire payload for error text: enough to tell
// provider-empty apart from our bugs, never the whole body.
func previewOf(raw []byte) string {
	preview := strings.TrimSpace(string(raw))
	const max = 500
	if len(preview) > max {
		preview = preview[:max] + "…"
	}
	return preview
}
