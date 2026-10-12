package codemap

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
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
	// Absolute paths under the repo root strip to relative and run;
	// absolutes anywhere else fail fast naming the root (no exec, no
	// "no such file" miss that sends the model guessing).
	exec.cmds = nil
	if _, err := read.Run(ctx, `{"path":"/workspace/src/main.go","start":1,"end":2}`); err != nil {
		t.Fatalf("rooted absolute must run, got %v", err)
	}
	if len(exec.cmds) != 1 || !strings.Contains(exec.cmds[0], `'src/main.go'`) {
		t.Fatalf("rooted absolute not stripped: %v", exec.cmds)
	}
	if _, err := read.Run(ctx, `{"path":"/etc/passwd","start":1,"end":2}`); err == nil ||
		!strings.Contains(err.Error(), "/workspace") {
		t.Fatalf("outside-root absolute must name the root, got %v", err)
	}
	if len(exec.cmds) != 1 {
		t.Fatalf("outside-root absolute must not execute: %v", exec.cmds)
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
	if merr == nil || !strings.Contains(merr.Error(), "/workspace/src/routes/home.jsx") ||
		!strings.Contains(merr.Error(), "/workspace/src/routes contains: /workspace/src/routes/about/ /workspace/src/routes/home/ /workspace/src/routes/profile/") {
		t.Fatalf("miss must name absolute siblings, got %v", merr)
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

func TestNormalizeRepoPath(t *testing.T) {
	const root = "/workspace"
	for path, want := range map[string]string{
		root + "/src/main.go": "src/main.go",
		root:                  ".",
		"src/main.go":         "src/main.go",
		"src/routes/":         "src/routes",
	} {
		got, err := normalizeRepoPath(path, root)
		if err != nil || got != want {
			t.Errorf("normalize %q = %q (%v), want %q", path, got, err, want)
		}
	}
	for _, path := range []string{"", "/etc/passwd", "/home/u/repo/x.go", "../x", "a//b"} {
		if got, err := normalizeRepoPath(path, root); err == nil {
			t.Errorf("normalize %q must fail, got %q", path, got)
		}
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

func TestGlobTool(t *testing.T) {
	files := "main.go\x00src/app.go\x00src/app_test.go\x00web/index.html\x00README.md\x00"
	exec := &stubExec{out: map[string]string{"ls-files": files}}
	tools := Tools(exec, "c", "/workspace")
	var glob agent.Tool
	for _, tool := range tools {
		if tool.Name == "glob" {
			glob = tool
		}
	}
	if glob.Name == "" {
		t.Fatal("Tools must include glob")
	}
	ctx := context.Background()
	run := func(pattern string) string {
		t.Helper()
		out, err := glob.Run(ctx, `{"pattern":`+strconv.Quote(pattern)+`}`)
		if err != nil {
			t.Fatalf("glob %q failed: %v", pattern, err)
		}
		return out
	}
	// No slash: base name at any depth (gitignore-style). `**`
	// crosses directories.
	for pattern, want := range map[string]string{
		"*.go":          "main.go\nsrc/app.go\nsrc/app_test.go",
		"*.md":          "README.md",
		"src/**/*.go":   "src/app.go\nsrc/app_test.go",
		"**/*.html":     "web/index.html",
		"src/*_test.go": "src/app_test.go",
		"READ??.md":     "README.md",
		"nope*.zz":      "(no files match)",
	} {
		if got := run(pattern); got != want {
			t.Errorf("glob %q = %q, want %q", pattern, got, want)
		}
	}
	for _, pattern := range []string{"", "a\nb", strings.Repeat("x", 201)} {
		if _, err := glob.Run(ctx, `{"pattern":`+strconv.Quote(pattern)+`}`); err == nil {
			t.Errorf("glob %q must fail", pattern)
		}
	}
	// Cap: 100 paths plus a narrowing hint, never the full dump.
	var many strings.Builder
	for i := 0; i < 150; i++ {
		many.WriteString("f" + strconv.Itoa(i) + ".go\x00")
	}
	exec.out["ls-files"] = many.String()
	got := run("*.go")
	lines := strings.Split(got, "\n")
	if len(lines) != 101 || !strings.Contains(lines[100], "50 more") {
		t.Fatalf("capped glob = %d lines, last %q", len(lines), lines[len(lines)-1])
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

func TestReadFileLineNumbers(t *testing.T) {
	exec := &stubExec{out: map[string]string{"sed -n": "package main\n\nfunc main() {\n"}}
	tools := Tools(exec, "c", "/workspace")
	var read agent.Tool
	for _, tool := range tools {
		if tool.Name == "read_file" {
			read = tool
		}
	}
	out, err := read.Run(context.Background(), `{"path":"main.go","start":10,"end":12}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "10\tpackage main\n11\t\n12\tfunc main() {" {
		t.Fatalf("numbered read = %q", out)
	}
}

func TestGitTool(t *testing.T) {
	exec := &stubExec{out: map[string]string{
		"status --short": "## main\n M a.go\n",
		"diff HEAD":      "---\n+new line\n",
		"log --oneline":  "abc1234 first\n",
	}}
	tools := Tools(exec, "c", "/workspace")
	var git agent.Tool
	for _, tool := range tools {
		if tool.Name == "git" {
			git = tool
		}
	}
	ctx := context.Background()
	run := func(args string) string {
		t.Helper()
		out, err := git.Run(ctx, args)
		if err != nil {
			t.Fatalf("git %s failed: %v", args, err)
		}
		return out
	}
	if got := run(`{"op":"status"}`); !strings.Contains(got, "M a.go") {
		t.Errorf("git status = %q", got)
	}
	if got := run(`{"op":"diff"}`); !strings.Contains(got, "+new line") {
		t.Errorf("git diff = %q", got)
	}
	if got := run(`{"op":"log"}`); !strings.Contains(got, "abc1234") {
		t.Errorf("git log = %q", got)
	}
	if _, err := git.Run(ctx, `{"op":"push"}`); err == nil {
		t.Error("unknown op must fail")
	}
	if _, err := git.Run(ctx, `{"op":"diff","path":"/etc/passwd"}`); err == nil {
		t.Error("outside-root path must fail")
	}
}

func TestFindSymbolTool(t *testing.T) {
	tags := `{"_type":"tag","name":"Verify","path":"server/auth.go","line":10,"kind":"function","signature":"(pin string) bool"}` + "\n" +
		`{"_type":"tag","name":"Verify","path":"server/other.go","line":3,"kind":"variable"}` + "\n" +
		`{"_type":"tag","name":"Other","path":"server/other.go","line":1,"kind":"class","signature":""}` + "\n"
	exec := &stubExec{out: map[string]string{"ctags": tags}}
	tools := Tools(exec, "c", "/workspace")
	var find agent.Tool
	for _, tool := range tools {
		if tool.Name == "find_symbol" {
			find = tool
		}
	}
	ctx := context.Background()
	out, err := find.Run(ctx, `{"name":"Verify"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "server/auth.go:10 [function] (pin string) bool\nserver/other.go:3 [variable]" {
		t.Fatalf("find_symbol = %q", out)
	}
	out, err = find.Run(ctx, `{"name":"Missing"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no definition") {
		t.Fatalf("unknown symbol = %q", out)
	}
	for _, args := range []string{`{"name":""}`, `{"name":"a\nb"}`} {
		if _, err := find.Run(ctx, args); err == nil {
			t.Errorf("find_symbol %q must fail", args)
		}
	}
	// No ctags binary: degrade to a grep hint, never a raw exec error.
	broken := &stubExec{out: map[string]string{}}
	tools = Tools(broken, "c", "/workspace")
	for _, tool := range tools {
		if tool.Name == "find_symbol" {
			find = tool
		}
	}
	out, err = find.Run(ctx, `{"name":"Verify"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "search_code") {
		t.Fatalf("missing ctags = %q, want grep fallback hint", out)
	}
}
