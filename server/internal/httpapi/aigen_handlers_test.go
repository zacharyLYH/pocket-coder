package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
)

// Handler tests for the AI-assisted git endpoints. The model is mocked at
// the HTTP level (fakeModel); git execs via the docker mock.

func TestGitCommitMessageSuccess(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	fm := newFakeModel(t, func(w http.ResponseWriter, _ map[string]any) {
		writeCompletion(w, "stop", `{"subject":"feat: add greeting","body":"Appends a line."}`, nil)
	})
	seedAI(t, d.State, fm.srv.URL)
	mockEnsure(md)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("diff --cached -U3"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "diff --git a/hello.txt b/hello.txt"}, nil)
	// Empty unstaged → the staged diff is what the model sees.
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("diff -U3; true"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/commit-message", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("commit-message: got %d %q, want 200", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"subject":"feat: add greeting"`) {
		t.Fatalf("subject missing: %q", rec.Body)
	}
}

func TestGitCommitMessageNoChanges(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	fm := newFakeModel(t) // zero steps: any model call fails the test
	seedAI(t, d.State, fm.srv.URL)
	mockEnsure(md)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("diff --cached -U3"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("diff -U3; true"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil)
	// Empty everything still probes untracked files.
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("ls-files --others"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/commit-message", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("commit-message clean tree: got %d %q, want 400", rec.Code, rec.Body)
	}
}

func TestGitCommitMessageUnconfigured(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	// No AI config: the handler 409s before touching docker.
	_ = md

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/commit-message", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("commit-message unconfigured: got %d %q, want 409", rec.Code, rec.Body)
	}
}

func TestGitPRBodySuccess(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	fm := newFakeModel(t, func(w http.ResponseWriter, _ map[string]any) {
		writeCompletion(w, "stop", `{"title":"Add greeting","body":"## What\nAdds a line."}`, nil)
	})
	seedAI(t, d.State, fm.srv.URL)
	mockEnsure(md)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("rev-parse --verify"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "0\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("log -1 --format=%B"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "feat: add greeting\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("show 'abc1234'"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "diff --git a/hello.txt b/hello.txt"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/pr-body", `{"sha":"abc1234"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pr-body: got %d %q, want 200", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"title":"Add greeting"`) {
		t.Fatalf("title missing: %q", rec.Body)
	}
}

func TestGitPRBodyValidation(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	fm := newFakeModel(t) // zero steps
	seedAI(t, d.State, fm.srv.URL)
	mockEnsure(md)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	// Bad sha format → 400 with no model call and no exec beyond ensure.
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/pr-body", `{"sha":"../evil"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("pr-body bad sha: got %d, want 400", rec.Code)
	}
	// Unknown commit → 400.
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("rev-parse --verify"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "1\n"}, nil)
	rec = authedPost(t, h, cookie, "/api/projects/abc/git/pr-body", `{"sha":"deadbeef"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("pr-body unknown commit: got %d, want 400", rec.Code)
	}
}
