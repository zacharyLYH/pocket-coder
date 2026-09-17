package codemap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"pcoder/internal/agent"
)

// stubExec records commands and replays canned outputs.
type stubExec struct {
	cmds []string
	out  map[string]string
	fail map[string]string
}

func (s *stubExec) ExecCommand(_ context.Context, _, command string) (string, error) {
	s.cmds = append(s.cmds, command)
	for sub, errStr := range s.fail {
		if strings.Contains(command, sub) {
			return "", errors.New(errStr)
		}
	}
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
		`{"path":"../x","start":1,"end":2}`,
		`{"path":"/../x","start":1,"end":2}`,
		`{"path":"a//b","start":1,"end":2}`,
		`{"path":"main.go","start":0,"end":2}`,
		`{"path":"main.go","start":1,"end":200}`,
		`{"path":"main.go","start":5,"end":3}`,
	} {
		if _, err := read.Run(ctx, args); err == nil {
			t.Errorf("read %q must fail", args)
		}
	}
	// Leading slashes normalize into the repo instead of failing: the
	// model emits them despite rule 5, so the layer converts rather
	// than errors.
	exec.cmds = nil
	if _, err := read.Run(ctx, `{"path":"/etc/passwd","start":1,"end":2}`); err != nil {
		t.Fatalf("leading slash must normalize, got %v", err)
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], `'etc/passwd'`) {
		t.Fatalf("leading slash not mapped into repo: %v", exec.cmds)
	}
	// Omitted bounds default to 1-50 instead of failing the step.
	exec.cmds = nil
	if _, err := read.Run(ctx, `{"path":"main.go"}`); err != nil {
		t.Fatalf("defaulted range must run, got %v", err)
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], "1,50p") {
		t.Fatalf("default range not read: %v", exec.cmds)
	}
	// Quote-breaking paths are allowed but must travel fully quoted: the
	// shell sees one argument, never a second command. Commands run from
	// inside the repo, so outputs and error echoes stay repo-relative.
	exec.cmds = nil
	if _, err := read.Run(ctx, `{"path":"main.go'; rm x; '","start":1,"end":2}`); err != nil {
		t.Fatalf("quoted path must run, got %v", err)
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], `cd '/workspace' &&`) {
		t.Fatalf("read must run from inside the repo: %v", exec.cmds)
	}
	if !strings.Contains(exec.cmds[0], `'main.go'"'"'; rm x; '"'"''`) {
		t.Fatalf("path not single-quoted: %v", exec.cmds)
	}
	// search_code matches come back repo-relative (never absolute), so
	// the model never learns an unusable path shape from our output.
	exec.cmds = nil
	if _, err := search.Run(ctx, `{"pattern":"main"}`); err != nil {
		t.Fatalf("search must run, got %v", err)
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], "grep -rn") {
		t.Fatalf("search not grep: %v", exec.cmds)
	}
	if strings.Contains(exec.cmds[0], "/workspace ") || strings.Contains(exec.cmds[0], "/workspace |") {
		t.Fatalf("search target must not be absolute: %v", exec.cmds)
	}
	list := tools[byName["list_dir"]]
	for _, args := range []string{
		`{"path":"../x"}`,
		`{"path":"/../x"}`,
	} {
		if _, err := list.Run(ctx, args); err == nil {
			t.Errorf("list %q must fail", args)
		}
	}
	// Omitted path lists the root; trailing slashes normalize; all from
	// inside the repo with directories marked.
	exec.cmds = nil
	if _, err := list.Run(ctx, `{}`); err != nil {
		t.Fatalf("list root must run, got %v", err)
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], `cd '/workspace' &&`) || !strings.Contains(exec.cmds[0], `'.'`) {
		t.Fatalf("list root not from inside the repo: %v", exec.cmds)
	}
	exec.cmds = nil
	if _, err := list.Run(ctx, `{"path":"src/"}`); err != nil {
		t.Fatalf("list subdir must run, got %v", err)
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], "ls -1 -p") || !strings.Contains(exec.cmds[0], `'src'`) {
		t.Fatalf("list subdir wrong command: %v", exec.cmds)
	}
	// Reading a directory serves its listing instead of an error
	// round-trip; only when the listing itself fails does it point at
	// list_dir.
	dirExec := &stubExec{
		fail: map[string]string{"sed -n": "exit 4: sed: read error on src/routes: Is a directory"},
		out:  map[string]string{"ls -1": "about/\nhome/\nprofile/\n"},
	}
	toolsDir := Tools(dirExec, "c", "/workspace")
	readDir := toolsDir[byName["read_file"]]
	got, err := readDir.Run(ctx, `{"path":"src/routes"}`)
	if err != nil || !strings.Contains(got, "about/") {
		t.Fatalf("directory read must list, got %q (%v)", got, err)
	}
	deadExec := &stubExec{fail: map[string]string{"sed -n": "Is a directory", "ls -1": "gone"}}
	readDead := Tools(deadExec, "c", "/workspace")[byName["read_file"]]
	if _, err := readDead.Run(ctx, `{"path":"src/routes"}`); err == nil || !strings.Contains(err.Error(), "list_dir") {
		t.Fatalf("unlistable directory must point at list_dir, got %v", err)
	}
	// A missed file names its parent's real contents, so one failure
	// teaches the correct path instead of starting a guess loop.
	missExec := &stubExec{
		fail: map[string]string{"sed -n": "exit 2: sed: can't read src/routes/home.jsx: No such file or directory"},
		out:  map[string]string{"ls -1": "about/\nhome/\nprofile/\n"},
	}
	toolsMiss := Tools(missExec, "c", "/workspace")
	readMiss := toolsMiss[byName["read_file"]]
	_, merr := readMiss.Run(ctx, `{"path":"src/routes/home.jsx"}`)
	if merr == nil || !strings.Contains(merr.Error(), "src/routes contains: about/ home/ profile/") {
		t.Fatalf("miss must name siblings, got %v", merr)
	}
	// A dead parent walks up: nothing to list anywhere means the raw
	// error, with no hint text attached.
	deadParent := &stubExec{fail: map[string]string{"sed -n": "exit 2", "ls -1": "gone"}}
	readDeadParent := Tools(deadParent, "c", "/workspace")[byName["read_file"]]
	_, perr := readDeadParent.Run(ctx, `{"path":"nope/missing.jsx"}`)
	if perr == nil || strings.Contains(perr.Error(), "contains:") {
		t.Fatalf("unlistable miss must stay raw, got %v", perr)
	}
	// Trailing slashes (list_dir's own directory marking) are accepted,
	// not rejected.
	exec.cmds = nil
	if _, err := read.Run(ctx, `{"path":"src/routes/"}`); err != nil {
		t.Fatalf("trailing slash must run, got %v", err)
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], `'src/routes'`) || strings.Contains(exec.cmds[0], `'src/routes/'`) {
		t.Fatalf("trailing slash not normalized: %v", exec.cmds)
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
	if sections["minItems"] != 1 {
		t.Fatalf("sections minItems = %v, want 1", sections["minItems"])
	}
	items, _ := sections["items"].(map[string]any)
	props, _ := items["properties"].(map[string]any)
	if props["title"].(map[string]any)["maxLength"] != 80 {
		t.Fatalf("title maxLength = %v, want 80", props["title"])
	}
	if props["summary"].(map[string]any)["maxLength"] != 600 {
		t.Fatalf("summary maxLength = %v, want 600", props["summary"])
	}
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

func TestShapeResult(t *testing.T) {
	res := shapeResult(Result{Sections: []Section{
		{Title: "Auth flow", Summary: "a"},
		{Title: "auth FLOW", Summary: "dupe"},
		{Title: "  ", Summary: "untitled"},
		{Title: "Long", Summary: strings.Repeat("x", 700)},
		{Title: "Data flow", Summary: "b"},
	}})
	if len(res.Sections) != 3 {
		t.Fatalf("sections = %+v, want 3 (dupe + untitled dropped)", res.Sections)
	}
	if res.Sections[0].Title != "Auth flow" || res.Sections[1].Title != "Long" || res.Sections[2].Title != "Data flow" {
		t.Fatalf("order/content wrong: %+v", res.Sections)
	}
	if len(res.Sections[1].Summary) != 603 || !strings.HasSuffix(res.Sections[1].Summary, "…") {
		t.Fatalf("summary not capped: len=%d", len(res.Sections[1].Summary))
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
