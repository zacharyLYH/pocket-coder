package codemap

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pcoder/internal/agent"
)

// stubExec records commands and replays canned outputs.
type stubExec struct {
	cmds []string
	out  map[string]string
}

func (s *stubExec) ExecCommand(_ context.Context, _, command string) (string, error) {
	s.cmds = append(s.cmds, command)
	for sub, out := range s.out {
		if strings.Contains(command, sub) {
			return out, nil
		}
	}
	return "", nil
}

func TestToolsRejectUnsafeArgs(t *testing.T) {
	exec := &stubExec{out: map[string]string{}}
	tools := Tools(exec, "c", "/workspace")
	byName := map[string]int{}
	for i, tool := range tools {
		byName[tool.Name] = i
	}
	search := tools[byName["search_code"]]
	read := tools[byName["read_file"]]
	ctx := context.Background()

	// Shell metacharacters must travel quoted, never raw.
	if _, err := search.Run(ctx, `{"pattern":"a; rm -rf /"}`); err != nil {
		t.Fatalf("quoted pattern must run, got %v", err)
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], `'a; rm -rf /'`) {
		t.Fatalf("pattern not single-quoted: %v", exec.cmds)
	}
	for _, args := range []string{
		`{"pattern":"a\nb"}`,
		`{"pattern":"` + strings.Repeat("x", 201) + `"}`,
		`{"pattern":""}`,
		`{"pattern":1}`,
	} {
		if _, err := search.Run(ctx, args); err == nil {
			t.Errorf("search %q must fail", args)
		}
	}
	for _, args := range []string{
		`{"path":"/etc/passwd","start":1,"end":2}`,
		`{"path":"../x","start":1,"end":2}`,
		`{"path":"a//b","start":1,"end":2}`,
		`{"path":"main.go","start":0,"end":2}`,
		`{"path":"main.go","start":1,"end":200}`,
	} {
		if _, err := read.Run(ctx, args); err == nil {
			t.Errorf("read %q must fail", args)
		}
	}
	// Quote-breaking paths are allowed but must travel fully quoted: the
	// shell sees one argument, never a second command.
	exec.cmds = nil
	if _, err := read.Run(ctx, `{"path":"main.go'; rm x; '","start":1,"end":2}`); err != nil {
		t.Fatalf("quoted path must run, got %v", err)
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], `'/workspace/main.go'"'"'; rm x; '"'"''`) {
		t.Fatalf("path not single-quoted: %v", exec.cmds)
	}
}

func TestRepoOrientation(t *testing.T) {
	exec := &stubExec{out: map[string]string{"ls -1": "package.json\nsrc/\nindex.html\n"}}
	got := repoOrientation(exec, context.Background(), "c", "/workspace")
	for _, want := range []string{"package.json", "src/", "index.html"} {
		if !strings.Contains(got, want) {
			t.Fatalf("orientation missing %q: %q", want, got)
		}
	}
	bad := &stubExec{out: map[string]string{}}
	if got := repoOrientation(bad, context.Background(), "c", "/workspace"); got != "" {
		t.Fatalf("failed ls must yield empty orientation, got %q", got)
	}
}

func TestSchemaHasFunction(t *testing.T) {
	// Style lives in the structured contract: refs carry an optional
	// function, summaries/refs describe flow requirements. Required
	// stays minimal for back-compat with old threads.
	s := schemaJSON()
	sections, _ := s["properties"].(map[string]any)["sections"].(map[string]any)
	items, _ := sections["items"].(map[string]any)
	props, _ := items["properties"].(map[string]any)
	refs, _ := props["refs"].(map[string]any)
	refItems, _ := refs["items"].(map[string]any)
	refProps, _ := refItems["properties"].(map[string]any)
	if _, ok := refProps["function"]; !ok {
		t.Fatalf("schema refs missing function property: %v", refProps)
	}
	// snippet and function stay optional: the model names the block,
	// the server attaches exact lines. Old threads (snippet, no
	// function) still parse either way.
	for _, want := range []string{"path", "startLine", "endLine"} {
		found := false
		for _, r := range refItems["required"].([]string) {
			if r == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("required lost %q", want)
		}
	}
	for _, r := range refItems["required"].([]string) {
		if r == "snippet" || r == "function" {
			t.Fatalf("%q must stay optional", r)
		}
	}
	var res Result
	raw := `{"sections":[{"title":"T","summary":"f() calls g().","refs":[{"path":"a.go","startLine":1,"endLine":2,"snippet":"x","function":"f"}]}]}`
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if res.Sections[0].Refs[0].Function != "f" {
		t.Fatalf("function lost in decode: %+v", res.Sections[0].Refs[0])
	}
}

func TestHydrateRefs(t *testing.T) {
	ctx := context.Background()
	var drops []string
	onTrace := func(ev agent.TraceEvent) {
		if ev.Kind == "ref_drop" {
			drops = append(drops, ev.Tool)
		}
	}
	hydrate := func(exec *stubExec, repoDir string, refs ...Ref) []Ref {
		t.Helper()
		ptrs := make([]*Ref, len(refs))
		for i := range refs {
			ptrs[i] = &refs[i]
		}
		hydrateRefs(ctx, exec, "c", repoDir, ptrs, onTrace)
		return refs
	}

	// Exact lines win over model text: the paraphrase is overwritten.
	exec := &stubExec{out: map[string]string{"sed -n": "func main() {\n\treturn\n}\n"}}
	got := hydrate(exec, "/workspace",
		Ref{Path: "main.go", StartLine: 10, EndLine: 12, Snippet: "a paraphrased lie"})
	if got[0].Snippet != "func main() {\n\treturn\n}\n" {
		t.Fatalf("snippet not hydrated exactly: %+v", got[0])
	}

	// Missing snippet is fine: the server fills it.
	exec = &stubExec{out: map[string]string{"sed -n": "line1\nline2\n"}}
	got = hydrate(exec, "/workspace", Ref{Path: "a.go", StartLine: 1, EndLine: 2})
	if got[0].Snippet != "line1\nline2\n" {
		t.Fatalf("empty snippet not filled: %+v", got[0])
	}
	exec = &stubExec{out: map[string]string{"sed -n": "absolute path resolved\n"}}
	got = hydrate(exec, "/workspace/repo",
		Ref{Path: "/workspace/repo/src/main.jsx", StartLine: 1, EndLine: 2})
	if got[0].Path != "src/main.jsx" {
		t.Fatalf("absolute repo path not normalized: %+v", got[0])
	}

	// Spans clamp to 10 lines before the read.
	exec = &stubExec{out: map[string]string{"sed -n": "x\n"}}
	got = hydrate(exec, "/workspace", Ref{Path: "big.go", StartLine: 1, EndLine: 200})
	if got[0].EndLine != 10 {
		t.Fatalf("span not clamped: %+v", got[0])
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], "1,10p") {
		t.Fatalf("clamped range not read: %v", exec.cmds)
	}

	// Multiple refs in one file batch into one sed, ascending ranges.
	// The stub replays what sed emits for blocks 1-2 and 5-6 of a file
	// holding lines 1..10: "1,2,5,6".
	exec = &stubExec{out: map[string]string{"sed -n": "1\n2\n5\n6\n"}}
	got = hydrate(exec, "/workspace",
		Ref{Path: "multi.go", StartLine: 5, EndLine: 6},
		Ref{Path: "multi.go", StartLine: 1, EndLine: 2})
	if len(exec.cmds) != 1 {
		t.Fatalf("refs in one file must batch into one exec: %v", exec.cmds)
	}
	if !strings.Contains(exec.cmds[0], "1,2p") || !strings.Contains(exec.cmds[0], "5,6p") {
		t.Fatalf("batched sed missing ranges: %v", exec.cmds[0])
	}
	if got[0].Snippet != "5\n6\n" || got[1].Snippet != "1\n2\n" {
		t.Fatalf("batched snippets wrong: %+v", got)
	}

	// Invalid paths, empty ranges, and binary files drop with a trace.
	got = hydrate(&stubExec{}, "/workspace",
		Ref{Path: "/etc/passwd", StartLine: 1, EndLine: 2},
		Ref{Path: "../x", StartLine: 1, EndLine: 2})
	for _, r := range got {
		if r.Snippet != "" {
			t.Fatalf("invalid path kept: %+v", r)
		}
	}
	got = hydrate(&stubExec{out: map[string]string{}}, "/workspace",
		Ref{Path: "empty.go", StartLine: 1, EndLine: 2})
	if got[0].Snippet != "" {
		t.Fatal("empty range kept")
	}
	got = hydrate(&stubExec{out: map[string]string{"sed -n": "a\x00b"}}, "/workspace",
		Ref{Path: "img.png", StartLine: 1, EndLine: 2})
	if got[0].Snippet != "" {
		t.Fatal("binary kept")
	}
	if len(drops) != 4 {
		t.Fatalf("drop traces = %v, want 4", drops)
	}
}
