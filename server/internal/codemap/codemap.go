// Package codemap is the first caller of the agent loop: it answers
// "what does this code do" with short sections and clickable snippets.
//
// Tools are read only by construction: search and read. No writer tool
// may register here; writers get their own prompt and route.
package codemap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pcoder/internal/agent"
)

// Ref is one clickable snippet: a file plus an exact line range.
type Ref struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Snippet   string `json:"snippet"`
}

// Section is one headed block of the answer.
type Section struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Refs    []Ref  `json:"refs"`
}

// Result is the whole structured answer.
type Result struct {
	Sections []Section `json:"sections"`
}

const systemPrompt = `You answer questions about the code in this repo. Ground every claim with the search_code and read_file tools before writing. Do not invent file paths or line numbers: refs must come from tool output.

Emit the final answer only as JSON matching the response schema: sections with title, summary, and refs holding path, startLine, endLine, and snippet. Keep snippets under 10 lines.

Two entry shapes get direct help. "Explain the current diff" means read git status and the diff, then walk each changed file. "Map this repo" means find entrypoints, key directories, and data flow, then summarize.

When the question is not about this repo's code, return one section titled "Not a codebase question" whose summary says codemaps answer questions about this repo and suggests two or three repo questions to try instead. Refs may be empty there.`

// schemaJSON is the json_schema for the final answer.
func schemaJSON() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sections": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":   map[string]any{"type": "string"},
						"summary": map[string]any{"type": "string"},
						"refs": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"path":      map[string]any{"type": "string"},
									"startLine": map[string]any{"type": "integer"},
									"endLine":   map[string]any{"type": "integer"},
									"snippet":   map[string]any{"type": "string"},
								},
								"required":             []string{"path", "startLine", "endLine", "snippet"},
								"additionalProperties": false,
							},
						},
					},
					"required":             []string{"title", "summary", "refs"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"sections"},
		"additionalProperties": false,
	}
}

// Executor runs one shell command in a container and returns trimmed output.
type Executor interface {
	ExecCommand(ctx context.Context, container, command string) (string, error)
}

// Tools builds the two read only tools bound to one container and repo dir.
func Tools(exec Executor, container, repoDir string) []agent.Tool {
	q := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
	}
	return []agent.Tool{
		{
			Name:        "search_code",
			Description: "Search repo text with grep. Returns file:line matches. Do not pass shell metacharacters; plain pattern only.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string", "description": "Fixed string or regex to search for"},
				},
				"required":             []string{"pattern"},
				"additionalProperties": false,
			},
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					Pattern string `json:"pattern"`
				}
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return "", fmt.Errorf("bad args: %w", err)
				}
				pattern := strings.TrimSpace(args.Pattern)
				if pattern == "" {
					return "", fmt.Errorf("pattern is required")
				}
				if len(pattern) > 200 {
					return "", fmt.Errorf("pattern over 200 chars")
				}
				if strings.ContainsAny(pattern, "\n\r\x00") {
					return "", fmt.Errorf("pattern must be one line")
				}
				cmd := fmt.Sprintf("grep -rn --exclude-dir=.git --exclude-dir=node_modules --exclude='*.lock' --exclude-dir=dist -I -m 50 -- %s %s | head -50",
					q(pattern), q(repoDir))
				out, err := exec.ExecCommand(ctx, container, cmd)
				if err != nil {
					if strings.Contains(out, "") && out == "" {
						return "(no matches)", nil
					}
					// grep exits 1 on no matches; ExecCommand turns that
					// into an error, so report it as empty, not failure.
					if strings.Contains(err.Error(), "exit 1") {
						return "(no matches)", nil
					}
					return "", err
				}
				if strings.TrimSpace(out) == "" {
					return "(no matches)", nil
				}
				if len(out) > 12*1024 {
					out = out[:12*1024]
				}
				return out, nil
			},
		},
		{
			Name:        "read_file",
			Description: "Read exact lines of one repo file. Paths stay inside the repo; ranges cap at 120 lines.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "description": "Repo-relative path"},
					"start": map[string]any{"type": "integer", "description": "First line, 1-based"},
					"end":   map[string]any{"type": "integer", "description": "Last line inclusive"},
				},
				"required":             []string{"path", "start", "end"},
				"additionalProperties": false,
			},
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					Path  string `json:"path"`
					Start int    `json:"start"`
					End   int    `json:"end"`
				}
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return "", fmt.Errorf("bad args: %w", err)
				}
				if !validRepoPath(args.Path) {
					return "", fmt.Errorf("invalid path")
				}
				if args.Start < 1 || args.End < args.Start || args.End-args.Start > 119 {
					return "", fmt.Errorf("range must span 1-120 lines with start >= 1")
				}
				cmd := fmt.Sprintf("sed -n '%d,%dp' %s", args.Start, args.End, q(repoDir+"/"+args.Path))
				out, err := exec.ExecCommand(ctx, container, cmd)
				if err != nil {
					return "", err
				}
				if strings.IndexByte(out, 0) >= 0 {
					return "", fmt.Errorf("binary file, not shown")
				}
				if len(out) > 100*1024 {
					out = out[:100*1024]
				}
				if strings.TrimSpace(out) == "" {
					return "(empty range)", nil
				}
				return out, nil
			},
		},
	}
}

func validRepoPath(p string) bool {
	if p == "" || len(p) > 1024 || strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "" {
			return false
		}
	}
	return true
}

// ToolCall is the persisted summary of one tool invocation: name plus
// full args. Outputs stay out of events.log (bloat); they go to the
// project's live log instead.
type ToolCall struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
}

// Ask runs the loop and parses the final JSON into a Result, collecting
// the tool summary and every trace event for the caller to persist.
func Ask(ctx context.Context, cfg agent.Config, exec Executor, container, repoDir, prompt string, history []map[string]any, onTrace func(agent.TraceEvent)) (Result, []ToolCall, error) {
	var calls []ToolCall
	out, err := agent.Run(ctx, cfg, systemPrompt, prompt, history, Tools(exec, container, repoDir), "codemap", schemaJSON(), 8,
		func(ev agent.TraceEvent) {
			if ev.Kind == "tool_start" {
				calls = append(calls, ToolCall{Tool: ev.Tool, Args: ev.Args})
			}
			if onTrace != nil {
				onTrace(ev)
			}
		})
	if err != nil {
		return Result{}, nil, err
	}
	var res Result
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		// Lenient retry: some models wrap JSON in fences.
		trimmed := strings.TrimSpace(out)
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
		if err2 := json.Unmarshal([]byte(strings.TrimSpace(trimmed)), &res); err2 != nil {
			return Result{}, nil, fmt.Errorf("model did not return codemap JSON: %w (output: %s)", err, excerpt(out))
		}
	}
	if len(res.Sections) == 0 {
		return Result{}, nil, fmt.Errorf("model returned no sections (output: %s)", excerpt(out))
	}
	// Clamp junk: many sections or giant snippets bloat events.log.
	if len(res.Sections) > 12 {
		res.Sections = res.Sections[:12]
	}
	for i := range res.Sections {
		if len(res.Sections[i].Refs) > 10 {
			res.Sections[i].Refs = res.Sections[i].Refs[:10]
		}
		for j := range res.Sections[i].Refs {
			r := &res.Sections[i].Refs[j]
			if !validRepoPath(r.Path) {
				r.Path = "(invalid path)"
			}
			if r.StartLine < 1 {
				r.StartLine = 1
			}
			if r.EndLine < r.StartLine {
				r.EndLine = r.StartLine
			}
			if len(r.Snippet) > 4*1024 {
				r.Snippet = r.Snippet[:4*1024]
			}
		}
	}
	return res, calls, nil
}

// excerpt caps raw model output for error messages and log previews.
func excerpt(s string) string {
	s = strings.TrimSpace(s)
	const max = 500
	if len(s) > max {
		return s[:max] + "…"
	}
	if s == "" {
		return "(empty)"
	}
	return s
}
