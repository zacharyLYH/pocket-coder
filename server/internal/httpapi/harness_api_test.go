package httpapi

// Harness endpoint tests — extracted from httpapi_sessions_test.go: these tests cover the harness registry/install API,
// not session semantics.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"

	"sps/internal/docker"
	"sps/internal/harness"
	"sps/internal/project"
	"sps/internal/state/statetest"
)

// TestInstallHarnessEndpoint drives POST /api/harnesses/{id}/install with an
// explicit project selection: only chosen projects are touched (the mock
// fails on any unexpected exec, so zero calls for "def" proves selectivity),
// running containers get the full install+validate chain, and per-project
// results come back to the UI.
func TestInstallHarnessEndpoint(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	if err := project.Open(dataDir).Create("def", project.Project{Name: "stopped-one"}); err != nil {
		t.Fatal(err)
	}

	// only "abc" is selected: nothing may run against "def"
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)

	// running project: lookup (miss) → install → re-lookup (hit) → validate
	lookup := []string{"bash", "-lc", "command -v fakecli"}
	install := []string{"bash", "-lc", "npm i -g fakecli"}
	validate := []string{"bash", "-lc", "fakecli --version || fakecli --help"}
	// misses: InstallHarness's initial probe; the post-install re-check hits
	lookups := 0
	md.EXPECT().Exec(mock.Anything, "sps-abc", lookup, false).
		RunAndReturn(func(context.Context, string, []string, bool) (docker.ExecResult, error) {
			lookups++
			if lookups < 2 {
				return docker.ExecResult{ExitCode: 1}, nil
			}
			return docker.ExecResult{ExitCode: 0, Output: "/usr/bin/fakecli"}, nil
		})
	md.EXPECT().Exec(mock.Anything, "sps-abc", install, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "added 1 package\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc", validate, true).
		Return(docker.ExecResult{ExitCode: 0, Output: "fakecli 1.0\n"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/harnesses/fake/install", `{"projectIds":["abc"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: got %d %q, want 200", rec.Code, rec.Body)
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
	if len(body.Results) != 1 || body.Results[0].Project != "x" || body.Results[0].Status != "ok" {
		t.Fatalf("results = %+v, want exactly the selected project ok", body.Results)
	}
	waitForEvent(t, d, "harness.install")

	// empty selection is refused before touching anything
	if rec := authedPost(t, h, cookie, "/api/harnesses/fake/install", `{"projectIds":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty selection: got %d, want 400", rec.Code)
	}
}

// TestExecCommandEndpoint covers the generic "run in projects" endpoint:
// arbitrary commands run synchronously in exactly the selected projects,
// output comes back, and a failing command surfaces its exit code and
// output while the other project still reports success.

func TestHarnessRegistryAPI(t *testing.T) {
	d, _, pinOut, dataDir := newSessionDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// seeded plugin is listed with its file-derived id
	rec := authedGet(t, h, cookie, "/api/harnesses")
	var list struct {
		Harnesses []harness.Harness `json:"harnesses"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Harnesses) != 1 || list.Harnesses[0].ID != "fake" {
		t.Fatalf("list = %+v err=%v", list, err)
	}

	// add-harness form writes a real plugin file
	rec = authedPost(t, h, cookie, "/api/harnesses", `{"name":"My Agent","command":"my-agent","install":"npm i -g my-agent"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create harness: %d %q", rec.Code, rec.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID != "my-agent" {
		t.Fatalf("id = %q, want my-agent", created.ID)
	}

	// duplicate name → refused (the registry already has that slug)
	rec = authedPost(t, h, cookie, "/api/harnesses", `{"name":"My Agent","command":"other"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate: %d %q, want 400", rec.Code, rec.Body)
	}
	// the refused write changed nothing: exactly the seeded + added plugins
	statetest.AssertSection(t, dataDir.Path(), "harnesses", map[string]any{
		"fake":     wantFakeHarnessEntry,
		"my-agent": map[string]any{"id": "my-agent", "name": "My Agent", "command": "my-agent", "install": "npm i -g my-agent"},
	})
}

// TestDeleteHarnessEndpoint: DELETE /api/harnesses/{id} removes exactly the
// named plugin from the state file, leaves every other section byte-for-byte
// intact, and deleting an unknown id is idempotent success.
// TestDeleteHarnessEndpoint: DELETE /api/harnesses/{id} removes exactly the
// named plugin from the state file, leaves every other section byte-for-byte
// intact, and deleting an unknown id is idempotent success.
func TestDeleteHarnessEndpoint(t *testing.T) {
	d, _, pinOut, dataDir := newSessionDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// add a second plugin so the delete provably spares the neighbors
	rec := authedPost(t, h, cookie, "/api/harnesses", `{"name":"Temp","command":"tempcli"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %q", rec.Code, rec.Body)
	}

	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/harnesses/temp")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("delete: %d %q", rec.Code, rec.Body)
	}
	// exactly the seeded plugin remains, untouched
	statetest.AssertSection(t, dataDir.Path(), "harnesses", map[string]any{
		"fake": wantFakeHarnessEntry,
	})

	// deleting an unknown harness is idempotent success and changes nothing
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/harnesses/ghost")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete unknown: %d %q, want 200", rec.Code, rec.Body)
	}
	statetest.AssertSection(t, dataDir.Path(), "harnesses", map[string]any{
		"fake": wantFakeHarnessEntry,
	})
	waitForEvent(t, d, "harness.deleted")
}

func TestProjectHarnessesShowsInstalled(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	// second plugin whose command is missing from the container, plus one
	// whose command carries arguments (the probe targets the binary only)
	if _, err := d.Harnesses.Save(harness.Harness{Name: "Ghosty", Command: "ghosty"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Harnesses.Save(harness.Harness{Name: "Shelly", Command: "vi hello.txt"}); err != nil {
		t.Fatal(err)
	}

	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"bash", "-lc", `for c in fakecli ghosty vi; do command -v "$c" >/dev/null && echo "$c"; done`}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "fakecli\nvi\n"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/abc/harnesses")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		Harnesses []struct {
			ID        string `json:"id"`
			Installed bool   `json:"installed"`
		} `json:"harnesses"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Harnesses) != 3 {
		t.Fatalf("got %d harnesses, want 3", len(body.Harnesses))
	}
	installedByID := map[string]bool{}
	for _, x := range body.Harnesses {
		installedByID[x.ID] = x.Installed
	}
	if !installedByID["fake"] || installedByID["ghosty"] || !installedByID["shelly"] {
		t.Fatalf("installed flags wrong: %+v", installedByID)
	}

	// stopped container → 409 (EnsureContainer sees exited and leaves it)
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: false, Status: "exited"}, nil).Once()
	rec = authedGet(t, h, cookie, "/api/projects/abc/harnesses")
	if rec.Code != http.StatusConflict {
		t.Fatalf("stopped container: %d, want 409", rec.Code)
	}
}
