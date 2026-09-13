package codemap

import (
	"context"
	"strings"
	"testing"
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
