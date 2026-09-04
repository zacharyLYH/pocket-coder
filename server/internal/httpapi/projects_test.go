package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"sps/internal/docker"
	"sps/internal/project"
	"sps/internal/state"
	"sps/internal/state/statetest"
	dockermocks "sps/mocks/docker"
)

// newProjectDeps wires the real project.Service over a mocked Docker client
// into the handler, so the HTTP layer is tested against the actual pipeline.
// Returns the shared state store so tests can seed it directly.
func newProjectDeps(t *testing.T) (Deps, *dockermocks.MockClient, *bytes.Buffer, *state.Store) {
	t.Helper()
	d, pinOut := newTestDeps(t)
	md := dockermocks.NewMockClient(t)
	st, err := state.Open(t.TempDir(), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	d.Projects = project.NewService(project.Open(st), md, d.Events)
	d.State = st
	return d, md, pinOut, st
}

func authedPost(t *testing.T, h http.Handler, cookie *http.Cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	return rec
}

func authedRequest(t *testing.T, h http.Handler, cookie *http.Cookie, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	return rec
}

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
		Name   string `json:"name"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	want := "https://github.com/x/hello.git"
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil ||
		created.Name != "hello" || created.ID == "" ||
		created.Repo != want || created.Branch != "" {
		t.Fatalf("created = %+v (want name=hello repo=%s branch=\"\"), err=%v", created, want, err)
	}

	// the source of truth on disk is exactly this project — name+repo
	// (branch/cloneMethod empty → omitted), nothing else in the document
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
		"projects": map[string]any{
			created.ID: map[string]any{"name": "hello", "repo": "https://github.com/x/hello.git", "cloneMethod": "http",},
		},
	})

	rec = authedGet(t, h, cookie, "/api/projects")
	var list struct {
		Projects []project.Entry `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Projects) != 1 || list.Projects[0].ID != created.ID {
		t.Fatalf("list = %+v err=%v", list, err)
	}

	md.EXPECT().Inspect(mock.Anything, "sps-"+created.ID).
		Return(docker.Container{Running: true, Status: "running"}, nil)
	rec = authedGet(t, h, cookie, "/api/projects/"+created.ID)
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

func TestListEmptyIsArrayNotNUll(t *testing.T) {
	d, _, pinOut, _ := newProjectDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects")
	// the frontend iterates the array; a JSON null would crash it
	want := "{\"projects\":[]}\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("got %d %q, want 200 %q", rec.Code, rec.Body, want)
	}
}

func TestGetEngineFailureIs500(t *testing.T) {
	d, md, pinOut, dataDir := newProjectDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	if err := project.Open(dataDir).Create("abc", project.Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{}, errors.New("engine down"))
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

	if err := project.Open(dataDir).Create("abc", project.Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Start(mock.Anything, "sps-abc").
		Return(fmt.Errorf("start container sps-abc: %w", docker.ErrNotFound))

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
	}
	for _, tc := range cases {
		rec := authedRequest(t, h, cookie, tc.method, tc.path)
		if rec.Code != tc.wantStatus || rec.Body.String() != tc.wantBody {
			t.Fatalf("%s %s: got %d %q, want %d %q",
				tc.method, tc.path, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
		}
	}
}
