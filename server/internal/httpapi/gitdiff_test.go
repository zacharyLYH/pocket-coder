package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
)

// Sessions.ExecCommand trims output, which eats the leading XY space of a
// first porcelain line like " M notes.txt". The status handler prefixes a
// sentinel line so entry lines survive trimming; these tests pin that.
func TestParsePorcelainLeadingSpaceFirstLine(t *testing.T) {
	got := parsePorcelain(" M notes.txt\n?? untracked.txt")
	if got["notes.txt"] != [2]string{" ", "M"} {
		t.Fatalf(`notes.txt = %q, want [" " "M"]`, got["notes.txt"])
	}
	if got["untracked.txt"] != [2]string{"?", "?"} {
		t.Fatalf(`untracked.txt = %q, want ["?" "?"]`, got["untracked.txt"])
	}
}

func TestGitStatusEndpointKeepsLeadingSpace(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	// RepoTarget: no clone at /workspace/repo, so the repo dir is /workspace.
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", "echo STATUS-BEGIN; git -C '/workspace' status --porcelain=v1 --untracked-files=normal"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "STATUS-BEGIN\n M notes.txt\n?? untracked.txt\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", "git -C '/workspace' rev-parse --abbrev-ref HEAD"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "master\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", "git -C '/workspace' diff --numstat; true"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "1\t0\tnotes.txt\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", "git -C '/workspace' diff --cached --numstat; true"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/abc/git/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d %q, want 200", rec.Code, rec.Body)
	}
	var body struct {
		Branch string `json:"branch"`
		Files  []struct {
			Path        string `json:"path"`
			Staged      string `json:"staged"`
			Unstaged    string `json:"unstaged"`
			UnstagedAdd int    `json:"unstagedAdd"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Files) != 2 {
		t.Fatalf("files = %+v, want 2", body.Files)
	}
	byPath := map[string]struct{ staged, unstaged string; unstagedAdd int }{}
	for _, f := range body.Files {
		byPath[f.Path] = struct {
			staged      string
			unstaged    string
			unstagedAdd int
		}{f.Staged, f.Unstaged, f.UnstagedAdd}
	}
	n, ok := byPath["notes.txt"]
	if !ok {
		t.Fatalf("no notes.txt in %+v (leading space was trimmed)", body.Files)
	}
	if n.staged != " " || n.unstaged != "M" || n.unstagedAdd != 1 {
		t.Fatalf("notes.txt = %+v, want staged=space unstaged=M unstagedAdd=1", n)
	}
}
