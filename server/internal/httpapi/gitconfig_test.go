package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
)

// There is no stored git identity or token to rotate: clone and pull auth
// failures always point at the server deploy key.
func TestGitPullAuthErrorCopy(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("pull --ff-only"), false).
		Return(docker.ExecResult{ExitCode: 128, Output: "fatal: Authentication failed"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/pull", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("pull failure: got %d %q, want 502", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "GitHub rejected the server key") {
		t.Fatalf("pull auth error must point at SSH setup: %q", rec.Body)
	}
}
