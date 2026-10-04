// Package codemap is the first caller of the agent loop: it answers
// "what does this code do" with short sections and clickable snippets.
// Tools are read only by construction: search and read.
package codemap

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"pcoder/internal/agent"
	"pcoder/internal/prompt"
	"pcoder/internal/textutil"
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

// systemPrompt is the repo-QA system prompt, assembled from the shared
// prompt blocks (product context, prose) plus codemap's loop rules.
var systemPrompt = prompt.CodemapGuide()

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
				"type": "array", "minItems": 1,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":   map[string]any{"type": "string", "maxLength": 80, "description": "Short finding label naming the flow or area, e.g. the auth flow. Never an action like 'Read X': sections report findings, not the exploration"},
						"summary": map[string]any{"type": "string", "maxLength": 600, "description": "One or two sentences of findings: name the exact functions involved and the handoff between them (calls, emits, writes to). Never describe tool calls, reads, or searches. Markdown lite is fine (inline code, bold)"},
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

// cutRunes bounds s at max runes (rune-aware: byte slicing could split a
// multi-byte rune). capToolOutput is the bare cut for tool results;
// capOutput marks the cut with an ellipsis for traces.
func cutRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

func capToolOutput(s string, max int) string { return cutRunes(s, max) }

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
				// Run from inside the repo so matches come back
				// repo-relative: absolute matches teach the model to use
				// absolute paths, which it must never use.
				cmd := fmt.Sprintf("cd %s && grep -rn --exclude-dir=.git --exclude-dir=node_modules --exclude='*.lock' --exclude-dir=dist -I -m 50 -- %s . | sed -e \"s|^\\./||\" | head -50",
					shQuote(repoDir), shQuote(pattern))
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
			Description: "Read exact lines of one repo file. Pass an ABSOLUTE path under the repo root named at turn start. Ranges cap at 120 lines. Omit start/end to read lines 1-50. Only read paths you have actually seen from list_dir, search_code, or the repo listing — never invent paths.",
			Schema: objectSchema(map[string]any{
				"path":  map[string]any{"type": "string", "description": "Absolute path under the repo root"},
				"start": map[string]any{"type": "integer", "description": "First line, 1-based (default 1)"},
				"end":   map[string]any{"type": "integer", "description": "Last line inclusive (default start+49)"},
			}, "path"),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					Path  string `json:"path"`
					Start *int   `json:"start"`
					End   *int   `json:"end"`
				}
				if err := decodeArgs(argsJSON, &args); err != nil {
					return "", err
				}
				// list_dir marks directories with a trailing slash; accept
				// the same shape back instead of rejecting our own output.
				path, nerr := normalizeRepoPath(args.Path, repoDir)
				if nerr != nil {
					return "", nerr
				}
				// Weak models often call with a bare path. Default the
				// range instead of failing the step: explicit values are
				// still validated below.
				start := 1
				if args.Start != nil {
					start = *args.Start
				}
				end := start + 49
				if args.End != nil {
					end = *args.End
				}
				if start < 1 || end < start || end-start > 119 {
					return "", fmt.Errorf("range must span 1-120 lines with start >= 1")
				}
				// Same repo-relative rule as search_code: the path the
				// model sees in errors must be usable verbatim next call.
				cmd := fmt.Sprintf("cd %s && sed -n '%d,%dp' %s", shQuote(repoDir), start, end, shQuote(path))
				out, err := exec.ExecCommand(ctx, container, cmd)
				if err != nil {
					// The model reaches for read_file when it wants a
					// listing: serve a directory read as its listing
					// instead of an error round-trip.
					if strings.Contains(out, "Is a directory") || strings.Contains(err.Error(), "Is a directory") {
						if listing, lerr := listPath(exec, ctx, container, repoDir, path); lerr == nil {
							return listing, nil
						}
						return "", fmt.Errorf("%s is a directory — use list_dir to list it", path)
					}
					// Ground the next guess: a miss names its parent's
					// real contents best-effort, so one failure teaches
					// the correct path instead of starting a guess loop.
					// Echoed absolute (the convention), so the retry is
					// verbatim-reusable.
					if absParent, names := parentHint(exec, ctx, container, repoDir, path); absParent != "" {
						abs := repoDir + "/" + path
						absNames := make([]string, 0, len(names))
						for _, n := range names {
							absNames = append(absNames, absParent+"/"+n)
						}
						return "", fmt.Errorf("no such file %s — %s contains: %s", abs, absParent, strings.Join(absNames, " "))
					}
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
			Name:        "list_dir",
			Description: "List files in one repo directory, non-recursive; directories end with /. Pass an ABSOLUTE path under the repo root; omit path for the repo root. Use this to discover structure before reading.",
			Schema: objectSchema(map[string]any{
				"path": map[string]any{"type": "string", "description": "Absolute directory under the repo root (default root)"},
			}),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					Path string `json:"path"`
				}
				if err := decodeArgs(argsJSON, &args); err != nil {
					return "", err
				}
				p := "."
				if strings.TrimSpace(args.Path) != "" {
					var nerr error
					if p, nerr = normalizeRepoPath(args.Path, repoDir); nerr != nil {
						return "", nerr
					}
				}
				out, err := listPath(exec, ctx, container, repoDir, p)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(out) == "" {
					return "(empty)", nil
				}
				return capToolOutput(out, 12*1024), nil
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
				"path": map[string]any{"type": "string", "description": "Optional absolute path under the repo root to limit the diff"},
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
					p, nerr := normalizeRepoPath(args.Path, repoDir)
					if nerr != nil {
						return "", nerr
					}
					target = " -- " + shQuote(p)
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

// normalizeRepoPath maps a model-supplied path onto a repo-relative path
// for the shell. The convention is absolute-under-root (the root is named
// in the turn system prompt, so the model copies one exact string instead
// of inventing workdirs): a repoDir prefix is stripped and succeeds.
// Anything else absolute is rejected WITHOUT executing — a rule error naming
// the root, not a "no such file" miss that teaches the model the file is
// missing and sends it guessing. Bare repo-relative paths still work.
func normalizeRepoPath(raw, repoDir string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", fmt.Errorf("path is required — absolute path under %s, e.g. %s/src/index.js", repoDir, repoDir)
	}
	if p == repoDir || p == repoDir+"/" {
		return ".", nil
	}
	if strings.HasPrefix(p, repoDir+"/") {
		p = strings.TrimPrefix(p, repoDir+"/")
	} else if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path must be under the repo root %s — got %q (copy the root from the turn prompt, e.g. %s/src/index.js)", repoDir, raw, repoDir)
	}
	p = cleanPath(p)
	if !validRepoPath(p) {
		return "", fmt.Errorf("invalid path")
	}
	return p, nil
}

// cleanPath trims slashes both ends; ".." escapes still fail validRepoPath.
func cleanPath(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), "/")
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

// parentDir is the containing directory of a repo-relative path, "." for
// top-level names.
func parentDir(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}

// listPath runs one directory listing from inside the repo. ls errors
// stay silent (stderr dropped): callers decide what empty means.
func listPath(exec Executor, ctx context.Context, container, repoDir, dir string) (string, error) {
	return exec.ExecCommand(ctx, container,
		fmt.Sprintf("cd %s && ls -1 -p -- %s 2>/dev/null | head -100", shQuote(repoDir), shQuote(dir)))
}

// parentHint names a failed path's nearest real directory best-effort,
// returned as (absoluteDir, names) so the miss error echoes verbatim-
// reusable absolute paths. ("", nil) on any failure: strictly a hint for
// the model's next guess, never an error. Missing parents walk up toward
// the root: a guess under a nonexistent dir still learns the nearest
// real listing.
func parentHint(exec Executor, ctx context.Context, container, repoDir, path string) (string, []string) {
	dir := parentDir(path)
	for {
		if !validRepoPath(dir) {
			return "", nil
		}
		if out, err := listPath(exec, ctx, container, repoDir, dir); err == nil {
			if names := strings.Fields(out); len(names) > 0 {
				if len(names) > 20 {
					names = names[:20]
				}
				abs := repoDir
				if dir != "." {
					abs = repoDir + "/" + dir
				}
				return abs, names
			}
		}
		if parent := parentDir(dir); parent == dir {
			return "", nil
		} else {
			dir = parent
		}
	}
}

// ToolRound is the legacy persisted shape (one entry per model round,
// each with thought + steps). Turns now persist flat []agent.Step like
// butler — the FE never read thought, both serving paths already
// flattened, and replay only needs ordered steps. Kept for reading old
// threads; nothing writes it anymore.
type ToolRound struct {
	Thought string       `json:"thought,omitempty"`
	Steps   []agent.Step `json:"steps"`
}

func capOutput(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func repoOrientation(exec Executor, ctx context.Context, container, repoDir string) string {
	out, err := exec.ExecCommand(ctx, container, "ls -1 "+shQuote(repoDir)+" 2>/dev/null | head -60")
	if err != nil || strings.TrimSpace(out) == "" {
		return ""
	}
	return capOutput(strings.TrimSpace(out), 2*1024)
}

func Ask(ctx context.Context, cfg agent.Config, exec Executor, container, repoDir, question string, history []map[string]any, onTrace func(agent.TraceEvent)) (Result, []agent.Step, *agent.Lineage, error) {
	lin := &agent.Lineage{}
	sys := systemPrompt
	// Name the one exact root the model must copy: absolute tool paths
	// under anything else are rejected, so hallucinating a workdir fails
	// fast with the rule instead of failing slow as missed files.
	sys += "\n\nRepo root: " + repoDir + " — pass absolute paths under it for read_file/list_dir (e.g. " + repoDir + "/src/index.js)."
	if listing := repoOrientation(exec, ctx, container, repoDir); listing != "" {
		sys += "\n\nRepo root orientation (top-level files/dirs of this project — use it to pick stack-appropriate first searches, e.g. package.json/src means JS, not Python):\n" + listing
	}
	tools := append([]agent.Tool{agent.TodoTool(lin)}, Tools(exec, container, repoDir)...)
	pipe := agent.Pipeline{
		Scope: &agent.ScopeGate{
			Prompt: prompt.CodemapScopePrompt(), SchemaName: "codemap_scope", Schema: prompt.CodemapScopeSchema(),
			Refused: func(verdict string) bool { return !codemapScopeParse(verdict) },
			Refusal: `This looks like an ops or non-code request — CodeMaps answers questions about this repo's code.`,
		},
		Context: func(string) []map[string]any { return history },
		Run: agent.TurnSpec{
			System: func() string { return sys },
			Tools:  tools, MaxSteps: 8,
			Schema: schemaJSON(), SchemaName: "codemap",
		},
		OnTrace: capTrace(onTrace),
	}
	res := pipe.Execute(ctx, cfg, lin, question)
	if res.Err != nil {
		return Result{}, nil, lin, res.Err
	}
	steps := capSteps(res.Rounds)
	out := res.Answer
	var parsed Result
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		return Result{}, nil, lin, fmt.Errorf("structuredExtractor returned invalid codemap JSON: %w (output: %s)", err, excerpt(out))
	}
	if len(parsed.Sections) == 0 {
		return Result{}, nil, lin, fmt.Errorf("model returned no sections (output: %s)", excerpt(out))
	}
	// Clamp junk: many sections or giant ranges bloat the thread file.
	// Snippets are resolved deterministically below (never model-copied):
	// refs the repo cannot back are dropped, not displayed.
	if len(parsed.Sections) > 12 {
		parsed.Sections = parsed.Sections[:12]
	}
	// Collect every ref first, then resolve all snippets in one batched
	// shell round trip (one sed over all files instead of one exec per
	// ref): a 10-ref answer costs 1 container exec instead of 10.
	var all []*Ref
	for i := range parsed.Sections {
		if len(parsed.Sections[i].Refs) > 10 {
			parsed.Sections[i].Refs = parsed.Sections[i].Refs[:10]
		}
		for j := range parsed.Sections[i].Refs {
			all = append(all, &parsed.Sections[i].Refs[j])
		}
	}
	if len(all) > 0 {
		hydrateRefs(ctx, exec, container, repoDir, all, onTrace)
	}
	// Drop refs the repo cannot back after hydration.
	for i := range parsed.Sections {
		kept := parsed.Sections[i].Refs[:0]
		for _, r := range parsed.Sections[i].Refs {
			if r.Snippet != "" {
				kept = append(kept, r)
			} else if onTrace != nil {
				onTrace(agent.TraceEvent{Kind: "ref_drop", Tool: r.Path,
					Args: fmt.Sprintf("%d-%d", r.StartLine, r.EndLine), Result: "unresolvable"})
			}
		}
		parsed.Sections[i].Refs = kept
	}
	return shapeResult(parsed), steps, lin, nil
}

// shapeResult deterministically enforces the house rules the schema can
// only suggest: one section per title, no untitled sections, summaries
// capped. The model owns content; the backend owns shape.
func shapeResult(res Result) Result {
	seen := map[string]bool{}
	out := make([]Section, 0, len(res.Sections))
	for _, s := range res.Sections {
		title := strings.TrimSpace(s.Title)
		if title == "" {
			continue
		}
		key := strings.ToLower(title)
		if seen[key] {
			continue
		}
		seen[key] = true
		if r := []rune(s.Summary); len(r) > 600 {
			s.Summary = string(r[:600]) + "…"
		}
		out = append(out, s)
	}
	res.Sections = out
	return res
}

var shQuote = textutil.ShellQuote

// codemapScopeParse reads the gate verdict. False only on a clean parse
// of about_code=false; anything unparseable fails open into the loop.
func codemapScopeParse(raw string) bool {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "{"); i > 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var v struct {
		AboutCode *bool `json:"about_code"`
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil || v.AboutCode == nil {
		return true
	}
	return *v.AboutCode
}

// capSteps flattens the shared transcript onto codemap's persisted step
// shape (flat like butler — no per-round thought), applying output/err
// caps as it goes.
func capSteps(rounds []agent.Round) []agent.Step {
	out := []agent.Step{}
	for _, r := range rounds {
		for _, s := range r.Steps {
			out = append(out, agent.Step{
				Tool: s.Tool, Args: s.Args,
				Output: capOutput(s.Output, 8000),
				Err:    capOutput(s.Err, 4000),
			})
		}
	}
	return out
}

// capTrace passes live events through to the caller's observer with the
// codemap-specific fields capped for logging.
func capTrace(onTrace func(agent.TraceEvent)) func(agent.TraceEvent) {
	if onTrace == nil {
		return nil
	}
	return func(ev agent.TraceEvent) {
		if ev.Err != "" {
			ev.Err = capOutput(ev.Err, 4000)
		}
		if ev.Result != "" {
			ev.Result = capOutput(ev.Result, 8000)
		}
		onTrace(ev)
	}
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
		// Normalize like tool args: repoDir-rooted absolutes strip to
		// relative, anything else absolute is dropped. Refs stay
		// repo-relative downstream (schema + file endpoint).
		if strings.HasPrefix(strings.TrimSpace(r.Path), "/") {
			np, nerr := normalizeRepoPath(r.Path, repoDir)
			if nerr != nil {
				drop(r, "path must be under the repo root")
				continue
			}
			r.Path = np
		}
		if !validRepoPath(r.Path) {
			drop(r, "invalid path")
			continue
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
			r.Function = string([]rune(r.Function)[:200])
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
			if r := []rune(snip); len(r) > 4*1024 {
				snip = string(r[:4*1024])
			}
			r.Snippet = snip
		}
	}
}

func excerpt(s string) string {
	s = strings.TrimSpace(s)
	const max = 500
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	if s == "" {
		return "(empty)"
	}
	return s
}
