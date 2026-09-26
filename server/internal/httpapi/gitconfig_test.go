package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/state"
)

// fakeGitHub serves the token live-check: only "good-token" passes.
func fakeGitHub(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer good-token" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"login":"me"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	old := gitHubUserURL
	gitHubUserURL = srv.URL
	t.Cleanup(func() { gitHubUserURL = old })
}

func clearGit(t *testing.T, st *state.Store) {
	t.Helper()
	if err := st.Mutate(func(doc *state.Document) error {
		doc.GitIDs = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGitConfigTestThenSave(t *testing.T) {
	d, _, pinOut, st := newProjectDeps(t)
	clearGit(t, st)
	fakeGitHub(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// Bad token: 502, nothing saved.
	rec := authedPost(t, h, cookie, "/api/git/identities", `{"label":"w","name":"N","email":"n@e.com","token":"bad-token"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("bad token: %d %q, want 502", rec.Code, rec.Body)
	}

	// Good token saves, and the stored secret never renders.
	rec = authedPost(t, h, cookie, "/api/git/identities", `{"label":"w","name":"N","email":"n@e.com","token":"good-token"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("save: %d %q", rec.Code, rec.Body)
	}
	rec = authedGet(t, h, cookie, "/api/git/identities")
	var got struct {
		Identities []map[string]any `json:"identities"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Identities) != 1 || got.Identities[0]["name"] != "N" || got.Identities[0]["hasToken"] != true {
		t.Fatalf("get configured: %d %v", rec.Code, got)
	}
	if strings.Contains(rec.Body.String(), "good-token") {
		t.Fatalf("GET must never leak the token: %q", rec.Body)
	}
}

func TestCreateProjectGatedOnGit(t *testing.T) {
	d, _, pinOut, st := newProjectDeps(t)
	clearGit(t, st)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := authedPost(t, h, cookie, "/api/projects", `{"repoUrl":"https://github.com/x/hello.git"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "git not configured") {
		t.Fatalf("create ungated: %d %q, want 409 git not configured", rec.Code, rec.Body)
	}
}

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
	if !strings.Contains(rec.Body.String(), "Git credentials rejected") {
		t.Fatalf("pull auth error must point at Git setup: %q", rec.Body)
	}
}
