package agent

import (
	"encoding/json"
	"time"
)

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

type LineageEvent struct {
	TS         string `json:"ts"`
	Kind       string `json:"kind"`
	Step       int    `json:"step,omitempty"`
	Round      int    `json:"round,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Args       string `json:"args,omitempty"`
	Content    string `json:"content,omitempty"`
	Output     string `json:"output,omitempty"`
	Err        string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Payload    any    `json:"payload,omitempty"`
}

type Lineage struct {
	InitialRequest any            `json:"initialRequest,omitempty"`
	TurnID         string         `json:"turnId,omitempty"`
	ThreadID       string         `json:"threadId,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	Time           time.Time      `json:"time,omitempty"`
	Events         []LineageEvent `json:"events"`
	ExtractorInput any            `json:"extractorInput,omitempty"`
	Error          string         `json:"error,omitempty"`
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

func payloadOf(raw []byte) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func extractorSnapshot(l *Lineage, answer string) map[string]any {
	out := map[string]any{"answer": capLine(answer, 16000)}
	if l == nil {
		return out
	}
	out["events"] = make([]map[string]any, 0, len(l.Events))
	for _, ev := range l.Events {
		item := map[string]any{"ts": ev.TS, "kind": ev.Kind}
		if ev.Step != 0 {
			item["step"] = ev.Step
		}
		if ev.Round != 0 {
			item["round"] = ev.Round
		}
		if ev.Tool != "" {
			item["tool"] = ev.Tool
		}
		if ev.Args != "" {
			item["args"] = capLine(ev.Args, 2000)
		}
		if ev.Output != "" {
			item["output"] = capLine(ev.Output, 6000)
		}
		if ev.Content != "" {
			item["content"] = capLine(ev.Content, 8000)
		}
		if ev.Err != "" {
			item["error"] = capLine(ev.Err, 2000)
		}
		if ev.Payload != nil && (ev.Kind == "llm_response" || ev.Kind == "format_response") {
			raw, _ := json.Marshal(ev.Payload)
			item["payload"] = payloadOf([]byte(capLine(string(raw), 12000)))
		}
		out["events"] = append(out["events"].([]map[string]any), item)
	}
	return out
}
