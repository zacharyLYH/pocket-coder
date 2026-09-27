package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

var todoStatuses = map[string]bool{
	"pending": true, "in_progress": true, "completed": true, "cancelled": true,
}

var todoPriorities = map[string]bool{"high": true, "medium": true, "low": true}

// TodoTool lets a model replace the current checklist during a turn. The
// result is returned to the model, so the next round can act on the latest
// status without another state channel.
func TodoTool(lin *Lineage) Tool {
	return Tool{
		Name:        "todo",
		Description: "Create or update the checklist for a multi-step task. Replace the full list; keep exactly one in_progress item while work remains.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type": "array", "maxItems": 20,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"content":  map[string]any{"type": "string"},
							"status":   map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "cancelled"}},
							"priority": map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
						},
						"required":             []string{"content", "status", "priority"},
						"additionalProperties": false,
					},
				},
			},
			"required": []string{"todos"}, "additionalProperties": false,
		},
		Run: func(_ context.Context, argsJSON string) (string, error) {
			var args struct {
				Todos []Todo `json:"todos"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return "", fmt.Errorf("bad todo args: %w", err)
			}
			if len(args.Todos) > 20 {
				return "", fmt.Errorf("todo list cannot exceed 20 items")
			}
			inProgress := 0
			for i := range args.Todos {
				t := &args.Todos[i]
				if strings.TrimSpace(t.Content) == "" || len([]rune(t.Content)) > 500 {
					return "", fmt.Errorf("todo %d content must be 1-500 characters", i+1)
				}
				if !todoStatuses[t.Status] || !todoPriorities[t.Priority] {
					return "", fmt.Errorf("todo %d has an invalid status or priority", i+1)
				}
				if t.Status == "in_progress" {
					inProgress++
				}
			}
			if inProgress > 1 {
				return "", fmt.Errorf("todo list may have only one in_progress item")
			}
			if lin != nil {
				lin.Todos = args.Todos
			}
			out, _ := json.Marshal(args.Todos)
			return string(out), nil
		},
	}
}
