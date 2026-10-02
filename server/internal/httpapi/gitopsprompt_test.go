package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	dockermocks "pcoder/mocks/docker"
)

// Ops-prompt tests: deterministic templates over mocked exec — no model,
// no live engine. The shared gitopsSetup seeds project "abc".

// opsPromptState stubs the full opsState probe sequence for a clean repo
// on main tracking origin/main, one commit ahead.
func opsPromptState(md *dockermocks.MockClient, base string) {
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("rev-parse --abbrev-ref HEAD"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "main\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("@{u}"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "origin/main\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("rev-list --left-right --count"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "0\t1\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("DIRTY-BEGIN"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "DIRTY-BEGIN\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("--diff-filter=U"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("--verify --quiet HEAD"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "0\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("symbolic-ref refs/remotes/origin/HEAD"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: base + "\n"}, nil).Maybe()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("check-ref-format"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil).Maybe()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("check-ref-format"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil).Maybe()
}

func TestGitOpsPromptPR(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	opsPromptState(md, "refs/remotes/origin/main")

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/ops-prompt",
		`{"op":"pr","newBranch":"feat/login","base":"main"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ops-prompt pr: got %d %q, want 200", rec.Code, rec.Body)
	}
	for _, want := range []string{"feat/login", "main", "pull request"} {
		if !strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(want)) {
			t.Fatalf("pr prompt missing %q: %q", want, rec.Body)
		}
	}
}

func TestGitOpsPromptValidation(t *testing.T) {
	newSetup := func(t *testing.T) (Deps, *dockermocks.MockClient, *http.Cookie, http.Handler) {
		d, md, pinOut, dataDir := newSessionDeps(t)
		seedProject(t, dataDir, "abc")
		md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
		md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
			Return(docker.ExecResult{ExitCode: 1}, nil)
		h := New(d)
		return d, md, loginCookie(t, h, pinOut), h
	}
	t.Run("bad op", func(t *testing.T) {
		_, _, cookie, h := newSetup(t)
		rec := authedPost(t, h, cookie, "/api/projects/abc/git/ops-prompt", `{"op":"rebase"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("bad op: got %d %q, want 400", rec.Code, rec.Body)
		}
	})
	t.Run("pr needs newBranch", func(t *testing.T) {
		d, md, pinOut, dataDir := newSessionDeps(t)
		seedProject(t, dataDir, "abc")
		md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
		md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
			Return(docker.ExecResult{ExitCode: 1}, nil)
		opsPromptState(md, "refs/remotes/origin/main")
		h := New(d)
		cookie := loginCookie(t, h, pinOut)
		rec := authedPost(t, h, cookie, "/api/projects/abc/git/ops-prompt", `{"op":"pr","base":"main"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("pr without branch: got %d %q, want 400", rec.Code, rec.Body)
		}
	})
}

func TestGitOpsPromptSyncUndo(t *testing.T) {
	setup := func(t *testing.T) (*http.Cookie, http.Handler) {
		d, md, pinOut, dataDir := newSessionDeps(t)
		seedProject(t, dataDir, "abc")
		md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
		md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
			Return(docker.ExecResult{ExitCode: 1}, nil)
		// Empty symbolic-ref output exercises the "main" fallback.
		opsPromptState(md, "")
		h := New(d)
		return loginCookie(t, h, pinOut), h
	}
	t.Run("sync falls back to main", func(t *testing.T) {
		cookie, h := setup(t)
		rec := authedPost(t, h, cookie, "/api/projects/abc/git/ops-prompt", `{"op":"sync"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync: got %d %q, want 200", rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "rebase") || !strings.Contains(rec.Body.String(), "main") {
			t.Fatalf("sync prompt not tailored: %q", rec.Body)
		}
	})
	t.Run("undo names the branch", func(t *testing.T) {
		cookie, h := setup(t)
		rec := authedPost(t, h, cookie, "/api/projects/abc/git/ops-prompt", `{"op":"undo"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("undo: got %d %q, want 200", rec.Code, rec.Body)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "main") || !strings.Contains(strings.ToLower(body), "confirm") {
			t.Fatalf("undo prompt missing branch/confirm: %q", body)
		}
	})
}
