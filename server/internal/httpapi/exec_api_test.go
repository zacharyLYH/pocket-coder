package httpapi

// Exec endpoint tests — extracted from httpapi_sessions_test.go: these tests cover the project exec API,
// not session semantics.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"sps/internal/docker"
	"sps/internal/project"
)

// TestExecCommandEndpoint covers the generic "run in projects" endpoint:
// arbitrary commands run synchronously in exactly the selected projects,
// output comes back, and a failing command surfaces its exit code and
// output while the other project still reports success.
func TestExecCommandEndpoint(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	if err := project.Open(dataDir).Create("def", project.Project{Name: "second"}); err != nil {
		t.Fatal(err)
	}

	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Inspect(mock.Anything, "sps-def").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"bash", "-lc", "echo hi"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "hi\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-def",
		[]string{"bash", "-lc", "echo hi"}, false).
		Return(docker.ExecResult{ExitCode: 3, Output: "boom\n"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/exec", `{"projectIds":["abc","def"],"command":"echo hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("exec: got %d %q, want 200", rec.Code, rec.Body)
	}
	var body struct {
		Results []struct {
			Project string `json:"project"`
			Status  string `json:"status"`
			Detail  string `json:"detail"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	byProject := map[string][2]string{}
	for _, r := range body.Results {
		byProject[r.Project] = [2]string{r.Status, r.Detail}
	}
	if got := byProject["x"]; got[0] != "ok" || got[1] != "hi" { // seededProject names "abc" as "x"
		t.Fatalf(`abc = %+v, want ok/"hi"`, got)
	}
	if got := byProject["second"]; got[0] != "error" || !strings.Contains(got[1], "exit 3") || !strings.Contains(got[1], "boom") {
		t.Fatalf(`def = %+v, want error surfacing exit 3 + output`, got)
	}
	waitForEvent(t, d, "projects.exec")

	// missing command / empty selection are refused
	if rec := authedPost(t, h, cookie, "/api/projects/exec", `{"projectIds":["abc"]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no command: got %d, want 400", rec.Code)
	}
	if rec := authedPost(t, h, cookie, "/api/projects/exec", `{"command":"echo hi"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no selection: got %d, want 400", rec.Code)
	}
}
