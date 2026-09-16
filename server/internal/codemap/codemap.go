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
	"sort"
	"strings"

	"pcoder/internal/agent"
)

// Ref is one clickable snippet: a file plus an exact line range, with
// the owning function when the snippet sits inside one.
type Ref struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Snippet   string `json:"snippet"`
	Function  string `json:"function,omitempty"`
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

const systemPrompt = `
This is CodeMaps, a feature in Pocket-Coder that provides users a curated and highly user friendly UX while coding AI native on the move. The job of CodeMaps is to answer questions about this repo. 

This is your goal:
Read the user's question carefully, use tool calls to gather sufficient context, and answer the user's question succintly while sacrificing some grammar for concision.


These are your rules:
1. Use a casual laid back tone when replying to prompts, even if it sacrifices concision
2. Not enforcing best grammar will help cut out bridge words that users can infer easily
3. Use tools heavily, but use tools extremely judiciously. Use them often to get all the context you need, but not more than you really need. 
4. Loop as many rounds as you need to get sufficient context, don't be shy.

`

// schemaJSON is the json_schema for the final answer. Output shape and
// style live here — in the structured contract, not in prompt prose:
// summaries name functions and handoffs, refs carry their function and
// run in flow order. snippet is intentionally NOT required: the model
// names the block (path + lines) and the server attaches the exact lines,
// so snippets can neither be paraphrased nor hallucinated.
func schemaJSON() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sections": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":   map[string]any{"type": "string", "description": "Short step label, e.g. the flow or area name"},
						"summary": map[string]any{"type": "string", "description": "One or two sentences: name the exact functions involved and the handoff between them (calls, emits, writes to)"},
						"refs": map[string]any{
							"type":        "array",
							"description": "Code refs in flow order: entrypoint first, downstream next",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"path":      map[string]any{"type": "string"},
									"startLine": map[string]any{"type": "integer"},
									"endLine":   map[string]any{"type": "integer"},
									"snippet":   map[string]any{"type": "string", "description": "Optional; the server overwrites it with the exact lines anyway"},
									"function":  map[string]any{"type": "string", "description": "Exact function or symbol the snippet belongs to, e.g. handleSubscribe; empty when not inside one"},
								},
								"required":             []string{"path", "startLine", "endLine"},
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

func objectSchema(properties map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func decodeArgs(raw string, dst any) error {
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return fmt.Errorf("bad args: %w", err)
	}
	return nil
}

func capToolOutput(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// Tools builds the read-only tools bound to one container and repo dir.
func Tools(exec Executor, container, repoDir string) []agent.Tool {
	return []agent.Tool{
		{
			Name:        "search_code",
			Description: "Search repo text with grep. Returns file:line matches. Do not pass shell metacharacters; plain pattern only.",
			Schema: objectSchema(map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Fixed string or regex to search for"},
			}, "pattern"),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					Pattern string `json:"pattern"`
				}
				if err := decodeArgs(argsJSON, &args); err != nil {
					return "", err
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
					shQuote(pattern), shQuote(repoDir))
				out, err := exec.ExecCommand(ctx, container, cmd)
				if err != nil {
					if out == "" || strings.Contains(err.Error(), "exit 1") {
						return "(no matches)", nil
					}
					return "", err
				}
				if strings.TrimSpace(out) == "" {
					return "(no matches)", nil
				}
				return capToolOutput(out, 12*1024), nil
			},
		},
		{
			Name:        "read_file",
			Description: "Read exact lines of one repo file. Paths stay inside the repo; ranges cap at 120 lines.",
			Schema: objectSchema(map[string]any{
				"path":  map[string]any{"type": "string", "description": "Repo-relative path"},
				"start": map[string]any{"type": "integer", "description": "First line, 1-based"},
				"end":   map[string]any{"type": "integer", "description": "Last line inclusive"},
			}, "path", "start", "end"),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					Path  string `json:"path"`
					Start int    `json:"start"`
					End   int    `json:"end"`
				}
				if err := decodeArgs(argsJSON, &args); err != nil {
					return "", err
				}
				if !validRepoPath(args.Path) {
					return "", fmt.Errorf("invalid path")
				}
				if args.Start < 1 || args.End < args.Start || args.End-args.Start > 119 {
					return "", fmt.Errorf("range must span 1-120 lines with start >= 1")
				}
				cmd := fmt.Sprintf("sed -n '%d,%dp' %s", args.Start, args.End, shQuote(repoDir+"/"+args.Path))
				out, err := exec.ExecCommand(ctx, container, cmd)
				if err != nil {
					return "", err
				}
				if strings.IndexByte(out, 0) >= 0 {
					return "", fmt.Errorf("binary file, not shown")
				}
				out = capToolOutput(out, 100*1024)
				if strings.TrimSpace(out) == "" {
					return "(empty range)", nil
				}
				return out, nil
			},
		},
		{
			Name:        "git_status",
			Description: "Show working-tree status (branch, staged/unstaged files). Use for 'what changed' or 'explain the diff' questions.",
			Schema:      objectSchema(map[string]any{}),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				cmd := fmt.Sprintf("git -C %s status --short --branch 2>&1 | head -50", shQuote(repoDir))
				out, err := exec.ExecCommand(ctx, container, cmd)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(out) == "" {
					return "(clean tree)", nil
				}
				return capToolOutput(out, 12*1024), nil
			},
		},
		{
			Name:        "git_diff",
			Description: "Show the working-tree diff (unstaged plus staged). Use after git_status to explain the current diff. Optional path limits to one file.",
			Schema: objectSchema(map[string]any{
				"path": map[string]any{"type": "string", "description": "Optional repo-relative path to limit the diff"},
			}),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					Path string `json:"path"`
				}
				if strings.TrimSpace(argsJSON) != "" && strings.TrimSpace(argsJSON) != "{}" {
					if err := decodeArgs(argsJSON, &args); err != nil {
						return "", err
					}
				}
				target := ""
				if strings.TrimSpace(args.Path) != "" {
					if !validRepoPath(strings.TrimSpace(args.Path)) {
						return "", fmt.Errorf("invalid path")
					}
					target = " -- " + shQuote(strings.TrimSpace(args.Path))
				}
				cmd := fmt.Sprintf("git -C %s diff HEAD --stat 2>&1 | head -30; echo '---'; git -C %s diff HEAD%s 2>&1 | head -400", shQuote(repoDir), shQuote(repoDir), target)
				out, err := exec.ExecCommand(ctx, container, cmd)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(out) == "" || strings.TrimSpace(out) == "---" {
					return "(no diff)", nil
				}
				return capToolOutput(out, 24*1024), nil
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

type ToolStep struct {
	Tool   string `json:"tool"`
	Args   string `json:"args,omitempty"`
	Output string `json:"output,omitempty"`
	Err    string `json:"error,omitempty"`
}

type ToolRound struct {
	Thought string     `json:"thought,omitempty"`
	Steps   []ToolStep `json:"steps"`
}

func repoOrientation(exec Executor, ctx context.Context, container, repoDir string) string {
	out, err := exec.ExecCommand(ctx, container, "ls -1 "+shQuote(repoDir)+" 2>/dev/null | head -60")
	if err != nil {
		return ""
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	if len(out) > 2*1024 {
		out = out[:2*1024] + "…"
	}
	return out
}

func capOutput(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func Ask(ctx context.Context, cfg agent.Config, exec Executor, container, repoDir, prompt string, history []map[string]any, onTrace func(agent.TraceEvent)) (Result, []ToolRound, *agent.Lineage, error) {
	var rounds []ToolRound
	var pending []ToolStep
	lin := &agent.Lineage{}
	sys := systemPrompt
	if listing := repoOrientation(exec, ctx, container, repoDir); listing != "" {
		sys += "\n\nRepo root orientation (top-level files/dirs of this project — use it to pick stack-appropriate first searches, e.g. package.json/src means JS, not Python):\n" + listing
	}
	out, err := agent.Run(ctx, cfg, sys, prompt, history, Tools(exec, container, repoDir), "codemap", schemaJSON(), 8,
		func(ev agent.TraceEvent) {
			switch ev.Kind {
			case "round":
				rounds = append(rounds, ToolRound{Thought: ev.Text})
			case "tool_start":
				pending = append(pending, ToolStep{Tool: ev.Tool, Args: ev.Args})
			case "tool_done":
				var st ToolStep
				if len(pending) > 0 {
					st = pending[0]
					pending = pending[1:]
				} else {
					// Unknown-tool path or out-of-order event: fall
					// back to the done event's own identity.
					st = ToolStep{Tool: ev.Tool, Args: ev.Args}
				}
				if ev.Err != "" {
					st.Err = capOutput(ev.Err, 4000)
				} else {
					st.Output = capOutput(ev.Result, 8000)
				}
				if len(rounds) == 0 {
					rounds = append(rounds, ToolRound{})
				}
				cur := &rounds[len(rounds)-1]
				cur.Steps = append(cur.Steps, st)
			}
			if onTrace != nil {
				onTrace(ev)
			}
		}, lin)
	if err != nil {
		return Result{}, nil, lin, err
	}
	var res Result
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		return Result{}, nil, lin, fmt.Errorf("structuredExtractor returned invalid codemap JSON: %w (output: %s)", err, excerpt(out))
	}
	if len(res.Sections) == 0 {
		return Result{}, nil, lin, fmt.Errorf("model returned no sections (output: %s)", excerpt(out))
	}
	// Clamp junk: many sections or giant ranges bloat the thread file.
	// Snippets are resolved deterministically below (never model-copied):
	// refs the repo cannot back are dropped, not displayed.
	if len(res.Sections) > 12 {
		res.Sections = res.Sections[:12]
	}
	// Collect every ref first, then resolve all snippets in one batched
	// shell round trip (one sed over all files instead of one exec per
	// ref): a 10-ref answer costs 1 container exec instead of 10.
	var all []*Ref
	for i := range res.Sections {
		if len(res.Sections[i].Refs) > 10 {
			res.Sections[i].Refs = res.Sections[i].Refs[:10]
		}
		for j := range res.Sections[i].Refs {
			all = append(all, &res.Sections[i].Refs[j])
		}
	}
	if len(all) > 0 {
		hydrateRefs(ctx, exec, container, repoDir, all, onTrace)
	}
	// Drop refs the repo cannot back after hydration.
	for i := range res.Sections {
		kept := res.Sections[i].Refs[:0]
		for _, r := range res.Sections[i].Refs {
			if r.Snippet != "" {
				kept = append(kept, r)
			} else if onTrace != nil {
				onTrace(agent.TraceEvent{Kind: "ref_drop", Tool: r.Path,
					Args: fmt.Sprintf("%d-%d", r.StartLine, r.EndLine), Result: "unresolvable"})
			}
		}
		res.Sections[i].Refs = kept
	}
	return res, rounds, lin, nil
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// hydrateRefs resolves every ref's snippet in one batched shell pass:
// refs are grouped per file, merged into disjoint line blocks, and each
// file is read with a single sed carrying all its ranges. N tool calls
// become ≤ one per file, so a 10-ref answer costs one container exec
// instead of ten. Refs that cannot be backed by the repo come back with
// an empty Snippet; the caller drops them.
func hydrateRefs(ctx context.Context, exec Executor, container, repoDir string, refs []*Ref, onTrace func(agent.TraceEvent)) {
	drop := func(r *Ref, reason string) {
		r.Snippet = ""
		if onTrace != nil {
			onTrace(agent.TraceEvent{Kind: "ref_drop", Tool: r.Path,
				Args: fmt.Sprintf("%d-%d", r.StartLine, r.EndLine), Result: reason})
		}
	}

	// Validate, clamp, and normalize paths first. Refs that fail here
	// never reach the shell.
	byPath := map[string][]*Ref{}
	for _, r := range refs {
		if !validRepoPath(r.Path) {
			if strings.HasPrefix(r.Path, repoDir+"/") {
				r.Path = strings.TrimPrefix(r.Path, repoDir+"/")
			}
			// Small models sometimes copy the absolute-looking path shown
			// in git output. Accept it only when its slash-trimmed form is
			// an actual file inside the configured repo.
			if strings.HasPrefix(r.Path, "/") {
				candidate := strings.TrimPrefix(r.Path, "/")
				if validRepoPath(candidate) {
					probe := fmt.Sprintf("test -f %s && printf yes", shQuote(repoDir+"/"+candidate))
					if got, err := exec.ExecCommand(ctx, container, probe); err == nil && strings.TrimSpace(got) == "yes" {
						r.Path = candidate
					}
				}
			}
			if !validRepoPath(r.Path) {
				drop(r, "invalid path")
				continue
			}
		}
		if r.StartLine < 1 {
			r.StartLine = 1
		}
		if r.EndLine < r.StartLine {
			r.EndLine = r.StartLine
		}
		if r.EndLine-r.StartLine > 9 {
			r.EndLine = r.StartLine + 9
		}
		if len(r.Function) > 200 {
			r.Function = r.Function[:200]
		}
		byPath[r.Path] = append(byPath[r.Path], r)
	}

	for path, group := range byPath {
		// Merge overlapping/touching ranges into disjoint blocks, ascending.
		sort.Slice(group, func(i, j int) bool {
			if group[i].StartLine != group[j].StartLine {
				return group[i].StartLine < group[j].StartLine
			}
			return group[i].EndLine < group[j].EndLine
		})
		type block struct{ start, end int }
		var blocks []block
		for _, r := range group {
			if n := len(blocks); n > 0 && r.StartLine <= blocks[n-1].end+1 {
				if r.EndLine > blocks[n-1].end {
					blocks[n-1].end = r.EndLine
				}
			} else {
				blocks = append(blocks, block{r.StartLine, r.EndLine})
			}
		}
		// One sed per file covering every block.
		var ranges strings.Builder
		for _, b := range blocks {
			fmt.Fprintf(&ranges, "%d,%dp;", b.start, b.end)
		}
		cmd := fmt.Sprintf("sed -n '%s' %s", strings.TrimSuffix(ranges.String(), ";"), shQuote(repoDir+"/"+path))
		out, err := exec.ExecCommand(ctx, container, cmd)
		if err != nil {
			for _, r := range group {
				drop(r, "read failed: "+err.Error())
			}
			continue
		}
		if strings.IndexByte(out, 0) >= 0 {
			for _, r := range group {
				drop(r, "binary file")
			}
			continue
		}
		if strings.TrimSpace(out) == "" {
			for _, r := range group {
				drop(r, "empty range")
			}
			continue
		}
		// sed emits blocks in ascending file order; walk the lines and
		// hand each ref its slice. Short files legitimately return fewer
		// lines than requested (EOF), so shortfalls are not errors.
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		pos := 0
		for _, r := range group {
			for len(blocks) > 0 && r.StartLine > blocks[0].end {
				pos += blocks[0].end - blocks[0].start + 1
				blocks = blocks[1:]
			}
			if len(blocks) == 0 || r.StartLine < blocks[0].start {
				drop(r, "empty range")
				continue
			}
			// pos marks how many output lines the skipped blocks consumed;
			// off is the ref's offset inside its own block.
			off := pos + (r.StartLine - blocks[0].start)
			end := off + (r.EndLine - r.StartLine) + 1
			if off > len(lines) {
				off = len(lines)
			}
			if end > len(lines) {
				end = len(lines)
			}
			if off >= end {
				drop(r, "empty range")
				continue
			}
			snip := strings.Join(lines[off:end], "\n") + "\n"
			if len(snip) > 4*1024 {
				snip = snip[:4*1024]
			}
			r.Snippet = snip
		}
	}
}

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
