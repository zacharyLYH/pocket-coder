//go:build integration

// Smoke test for seeded state: a server booted against NOTHING but a
// state.json must recover the full desired state — the boot bootstrap
// (BringAllUp, the pass main runs before serving) recreates the
// container, and because a fresh engine has no repo volume, the repo is
// re-cloned from the URL in state.json. Recovery must not mutate desired
// state: the on-disk file is asserted byte-equal (as generic maps) before
// and after. The seed models test/state.mock.json's shape (owner/repo id,
// no name field) but points at a local fixture repo so the test is
// hermetic. Run with:
// go test -tags=integration -count=1 -run TestStateMockRecovery ./internal/httpapi/
package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"pcoder/internal/state/statetest"
	"strings"
	"testing"
)

func TestStateMockRecovery(t *testing.T) {
	// the fixture knows the project id its URL derives to under the
	// service hatch (set in newLiveDepsOnDir below)
	g := fixtureRepo(t)
	url, id := g.URL, g.ID

	// seed a fresh data dir with one project, shaped like the committed
	// mock (owner/repo key, repo + harness + session, no name field)
	seed := `{"user":{"email":"me@example.com"},"projects":{` +
		`"` + id + `":{"repo":"` + url + `","branch":"main","cloneMethod":"http",` +
		`"harnesses":["opencode"],"sessions":{"main":{},"oc1":{"harness":"opencode"}},` +
		`"shortcuts":[{"id":"qc-dev","alias":"dev","kind":"cmd","command":"npm install && npm start -- --host 0.0.0.0"}]}},` +
		`"harnesses":{"opencode":{"id":"opencode","name":"OpenCode","command":"opencode","install":"npm i -g opencode-ai"}}}`
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.json")
	if err := os.WriteFile(statePath, []byte(seed+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantDoc := statetest.Read(t, statePath)

	// same wiring as newLiveDeps, but over the seeded store
	h, _, svc, pinOut, _, _ := newLiveDepsOnDir(t, dataDir)

	cookie := login(t, h, pinOut)

	// Boot bootstrap (the pass main.go runs before serving): recreate the
	// container from the seeded state and — fresh engine, no volumes —
	// re-clone the repo. Harness installs are skipped here: the helper does
	// not wire an installer (main does), and this smoke test pins the
	// container+repo recovery path.
	if err := svc.BringAllUp(context.Background()); err != nil {
		t.Fatalf("boot bootstrap: %v", err)
	}

	// desired state survived the boot untouched
	statetest.AssertEqual(t, statePath, wantDoc)

	// the project is up before any session request touches it
	code, body := doJSON(t, h, cookie, http.MethodGet, projectPath(id, ""), "")
	if code != http.StatusOK {
		t.Fatalf("get seeded project: %d %v", code, body)
	}
	if body["status"] != "running" {
		t.Fatalf("boot bootstrap should leave the container running: %v", body["status"])
	}

	// the first session create lands on an already-provisioned container:
	// no lazy recovery left in the request path
	code, body = doJSON(t, h, cookie, http.MethodPost, projectPath(id, "/sessions"), `{"name":"main"}`)
	if code != http.StatusCreated {
		t.Fatalf("create session on recovered container: %d %v", code, body)
	}
	waitForStatus(t, h, cookie, id, "running")

	// the repo from state.json is really there (git clone puts the working
	// tree at /workspace/repo; the fixture ships hello.txt)
	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects/exec",
		`{"projectIds":["`+id+`"],"command":"cat /workspace/repo/hello.txt"}`)
	if code != http.StatusOK {
		t.Fatalf("exec in recovered container: %d %v", code, body)
	}
	results, _ := body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("exec results: %v", body)
	}
	r0 := results[0].(map[string]any)
	detail, _ := r0["detail"].(string)
	if r0["status"] != "ok" || !strings.Contains(detail, "hi") {
		t.Fatalf("repo not re-cloned: cat hello.txt = %v", r0)
	}

	// recovery is derived state only: the on-disk desired state is untouched
	// (the "main" session metadata was already seeded, so nothing changed)
	statetest.AssertEqual(t, statePath, wantDoc)

	// the boot reconcile + re-clone is visible in the project observe log
	for _, typ := range []string{"project.reconcile", "project.clone"} {
		code, body := doJSON(t, h, cookie, http.MethodGet, projectPath(id, "/observe?type="+typ), "")
		if code != http.StatusOK {
			t.Fatalf("observe %s: %d %v", typ, code, body)
		}
		logs, _ := body["logs"].([]any)
		if len(logs) == 0 {
			t.Fatalf("observe log missing %s lines", typ)
		}
	}

	// the recovered container is engine-global: clean it up by id
	deleteTestProject(t, h, cookie, id)
}
