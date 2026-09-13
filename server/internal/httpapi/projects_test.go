package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/project"
	"pcoder/internal/state/statetest"
)

// newProjectDeps, authedPost and authedRequest live in httpapi_test.go
// (shared fixtures).

func TestProjectsRequireAuth(t *testing.T) {
	d, _, _, _ := newProjectDeps(t)
	h := New(d)
	if rec := get(t, h, "/api/projects"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/projects unauthed: %d, want 401", rec.Code)
	}
	if rec := post(t, h, "/api/projects", `{}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/projects unauthed: %d, want 401", rec.Code)
	}
}

func TestCreateListGetProjectAPI(t *testing.T) {
	d, md, pinOut, st := newProjectDeps(t)
	h := New(d)

	md.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	md.EXPECT().InspectImage(mock.Anything, project.ProjectImage).Return(nil)
	md.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	md.EXPECT().Exec(mock.Anything, "cid", []string{"git", "clone", "https://github.com/x/hello.git", "/workspace/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	cookie := loginCookie(t, h, pinOut)

	rec := authedPost(t, h, cookie, "/api/projects", `{"repoUrl":"https://github.com/x/hello.git"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", rec.Code, rec.Body)
	}
	var created struct {
		ID     string `json:"id"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	want := "https://github.com/x/hello.git"
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil ||
		created.ID != "x/hello" ||
		created.Repo != want || created.Branch != "" {
		t.Fatalf("created = %+v (want id=x/hello repo=%s branch=\"\"), err=%v", created, want, err)
	}

	// the source of truth on disk is exactly this project — repo only
	// (branch empty → omitted), nothing else in the document
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
		"projects": map[string]any{
			created.ID: map[string]any{"repo": "https://github.com/x/hello.git", "cloneMethod": "http"},
		},
	})

	rec = authedGet(t, h, cookie, "/api/projects")
	var list struct {
		Projects []project.Entry `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Projects) != 1 || list.Projects[0].ID != created.ID {
		t.Fatalf("list = %+v err=%v", list, err)
	}

	md.EXPECT().Inspect(mock.Anything, project.ContainerName(created.ID)).
		Return(docker.Container{Running: true, Status: "running"}, nil)
	rec = authedGet(t, h, cookie, "/api/projects/"+url.PathEscape(created.ID))
	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Status != "running" {
		t.Fatalf("got = %+v err=%v", got, err)
	}
}

func TestCreateCloneFailureSurfacesDetail(t *testing.T) {
	d, md, pinOut, _ := newProjectDeps(t)
	h := New(d)

	md.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	md.EXPECT().InspectImage(mock.Anything, project.ProjectImage).Return(nil)
	md.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	md.EXPECT().Exec(mock.Anything, "cid", mock.Anything, false).
		Return(docker.ExecResult{ExitCode: 128, Output: "fatal: repository not found"}, nil)

	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects", `{"repoUrl":"https://github.com/x/nope.git"}`)
	want := "{\"error\":\"clone https://github.com/x/nope.git: fatal: repository not found\"}\n"
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != want {
		t.Fatalf("create failure: got %d %q, want 500 %q", rec.Code, rec.Body, want)
	}
}

func TestCreateRequiresGitHubRepo(t *testing.T) {
	d, md, pinOut, _ := newProjectDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	for _, body := range []string{
		`{}`,
		`{"repoUrl":""}`,
		`{"repoUrl":"https://example.com/x/hello.git"}`,
	} {
		rec := authedPost(t, h, cookie, "/api/projects", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("create %s: got %d %q, want 400", body, rec.Code, rec.Body)
		}
	}
	// no docker calls for rejected input
	md.AssertNotCalled(t, "Run", mock.Anything, mock.Anything)
}

func TestCreateDuplicateRepoIsConflict(t *testing.T) {
	d, md, pinOut, _ := newProjectDeps(t)
	h := New(d)

	md.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	md.EXPECT().InspectImage(mock.Anything, project.ProjectImage).Return(nil)
	md.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	md.EXPECT().Exec(mock.Anything, "cid", mock.Anything, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	cookie := loginCookie(t, h, pinOut)
	body := `{"repoUrl":"https://github.com/x/hello.git"}`
	if rec := authedPost(t, h, cookie, "/api/projects", body); rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %q", rec.Code, rec.Body)
	}
	rec := authedPost(t, h, cookie, "/api/projects", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second create: got %d %q, want 409", rec.Code, rec.Body)
	}
}

func TestCreateInvalidBodyIs400(t *testing.T) {
	d, _, pinOut, _ := newProjectDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects", `{invalid json`)
	want := "{\"error\":\"invalid JSON body\"}\n"
	if rec.Code != http.StatusBadRequest || rec.Body.String() != want {
		t.Fatalf("got %d %q, want 400 %q", rec.Code, rec.Body, want)
	}
}

func TestGetEngineFailureIs500(t *testing.T) {
	d, md, pinOut, dataDir := newProjectDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	if err := project.Open(dataDir).Create("abc", project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{}, errors.New("engine down"))
	rec := authedGet(t, h, cookie, "/api/projects/abc")
	want := "{\"error\":\"internal error\"}\n"
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != want {
		t.Fatalf("got %d %q, want 500 %q", rec.Code, rec.Body, want)
	}
}

// Metadata exists but the container is gone (scope=container deleted earlier):
// ops must map the engine's not-found to a 404 with its own message.
func TestOpMissingContainerIs404(t *testing.T) {
	d, md, pinOut, dataDir := newProjectDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	if err := project.Open(dataDir).Create("abc", project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Start(mock.Anything, "pcoder-abc").
		Return(fmt.Errorf("start container pcoder-abc: %w", docker.ErrNotFound))

	rec := authedPost(t, h, cookie, "/api/projects/abc/start", "")
	want := "{\"error\":\"container not found\"}\n"
	if rec.Code != http.StatusNotFound || rec.Body.String() != want {
		t.Fatalf("got %d %q, want 404 %q", rec.Code, rec.Body, want)
	}
}

func TestProjectOpsAndScopesAPI(t *testing.T) {
	d, _, pinOut, _ := newProjectDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	cases := []struct {
		method, path string
		wantStatus   int
		wantBody     string
	}{
		{http.MethodPost, "/api/projects/ghost/start", http.StatusNotFound, "{\"error\":\"no such project\"}\n"},
		{http.MethodPost, "/api/projects/ghost/stop", http.StatusNotFound, "{\"error\":\"no such project\"}\n"},
		{http.MethodPost, "/api/projects/ghost/restart", http.StatusNotFound, "{\"error\":\"no such project\"}\n"},
		{http.MethodDelete, "/api/projects/ghost?scope=everything", http.StatusBadRequest, "{\"error\":\"invalid delete scope: \\\"everything\\\"\"}\n"},
		{http.MethodDelete, "/api/projects/ghost?scope=all", http.StatusNotFound, "{\"error\":\"no such project\"}\n"},
		{http.MethodDelete, "/api/projects/ghost", http.StatusNotFound, "{\"error\":\"no such project\"}\n"},
		// the frontend iterates the array; a JSON null would crash it
		{http.MethodGet, "/api/projects", http.StatusOK, "{\"projects\":[]}\n"},
	}
	for _, tc := range cases {
		rec := authedRequest(t, h, cookie, tc.method, tc.path)
		if rec.Code != tc.wantStatus || rec.Body.String() != tc.wantBody {
			t.Fatalf("%s %s: got %d %q, want %d %q",
				tc.method, tc.path, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
		}
	}
}

// TestLazyReconciliationViaSessionHandler moved from httpapi_sessions_test.go:
// project-container recovery, observed through the session list handler.
// TestLazyReconciliationViaSessionHandler proves the disk-is-truth
// guarantee: when a container is missing but the project exists on disk,
// EnsureContainer inside the handler recreates it.
func TestLazyReconciliationViaSessionHandler(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	// first call: container missing → triggers reconciliation
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{}, docker.ErrNotFound).Once()
	md.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	md.EXPECT().InspectImage(mock.Anything, project.ProjectImage).Return(nil)
	md.EXPECT().Run(mock.Anything, mock.Anything).Return("new-cid", nil)
	// EnsureContainer re-inspects after reconciling: now running
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Once()
	// after reconciliation, the session list uses the container NAME (not cid)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 1, Output: "no server running"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/abc/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("list sessions after reconcile: %d %s", rec.Code, rec.Body)
	}
	// the reconcile event should have been emitted
	evs, _ := d.Events.Read(0, 0)
	found := false
	for _, e := range evs {
		if e.Type == "project.reconcile" && e.Data["id"] == "abc" {
			found = true
		}
	}
	if !found {
		t.Fatal("no project.reconcile event after lazy reconciliation")
	}
}
