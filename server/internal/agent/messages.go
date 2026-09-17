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
