package agent

import (
	"time"
)

// LineageTool is one entry in the tools-available section at the top of
// the lineage file: name + description only, no schemas. Schemas live in
// code; lineage just needs to say what the model could call.
type LineageTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// LineageCall is one tool call the model requested in a response.
type LineageCall struct {
	Tool string `json:"tool"`
	Args string `json:"args,omitempty"`
}

// Lineage event kinds. Use these for lin.record() — raw strings in call
// sites drift (llm-request vs llm_request), and the shape test pins the
// wire values, so a typo would pass review and fail the suite.
const (
	LineageLLMRequest    = "llm_request"
	LineageLLMResponse   = "llm_response"
	LineageToolStart     = "tool_start"
	LineageToolDone      = "tool_done"
	LineageFinalAnswer   = "final_answer"
	LineageFormatRequest = "format_request"
	LineageFormatResp    = "format_response"
	LineageError         = "error"
)

type LineageEvent struct {
	TS         string        `json:"ts"`
	Kind       string        `json:"kind"`
	Step       int           `json:"step,omitempty"`
	Model      string        `json:"model,omitempty"`
	Tool       string        `json:"tool,omitempty"`
	Args       string        `json:"args,omitempty"`
	Content    string        `json:"content,omitempty"`
	Output     string        `json:"output,omitempty"`
	Calls      []LineageCall `json:"calls,omitempty"`
	Err        string        `json:"error,omitempty"`
	DurationMs int64         `json:"durationMs,omitempty"`
}

// TraceEvent is the compact live log event. Lineage stores the larger record.
type TraceEvent struct {
	Kind   string
	Round  int
	Text   string
	Tool   string
	Args   string
	Result string
	Err    string
}

// Todo is the small, replace-in-place checklist shared by agent features.
// Status and priority mirror OpenCode's todo contract.
type Todo struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

type Lineage struct {
	TurnID   string         `json:"turnId,omitempty"`
	ThreadID string         `json:"threadId,omitempty"`
	Prompt   string         `json:"prompt,omitempty"`
	Model    string         `json:"model,omitempty"`
	Tools    []LineageTool  `json:"tools,omitempty"`
	Time     time.Time      `json:"time,omitempty"`
	Events   []LineageEvent `json:"events"`
	Todos    []Todo         `json:"todos,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func (l *Lineage) record(kind string, fill func(*LineageEvent)) {
	if l == nil {
		return
	}
	ev := LineageEvent{TS: time.Now().UTC().Format(time.RFC3339Nano), Kind: kind}
	if fill != nil {
		fill(&ev)
	}
	l.Events = append(l.Events, ev)
}

func capLine(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// SetTools records the tools-available section once per turn: name +
// description only, no schemas. Callers set it at turn start; later
// events just reference tools by name.
func (l *Lineage) SetTools(tools []Tool) {
	if l == nil || len(l.Tools) > 0 {
		return
	}
	out := make([]LineageTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, LineageTool{Name: t.Name, Description: t.Description})
	}
	l.Tools = out
}
