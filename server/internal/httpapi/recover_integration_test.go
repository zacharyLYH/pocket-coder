//go:build integration

// Smoke test for the dev seed state (dev/state.mock.json): a server
// booted against NOTHING but a state.json must recover the full desired
// state — the project appears, the first ensure recreates its container,
// and because a fresh engine has no repo volume, the repo is re-cloned
// from the URL in state.json. Recovery must not mutate desired state:
// the on-disk file is asserted byte-equal (as generic maps) before and
// after. Run with:
// go test -tags=integration -count=1 -run TestStateMockRecovery ./internal/httpapi/
package httpapi

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"pcoder/internal/state/statetest"
	"strings"
	"testing"
)

func TestStateMockRecovery(t *testing.T) {
	// seed a fresh data dir with the committed mock, retargeting the login
	// email to the one the test auth service expects and the project key
	// to a random test id so reruns never collide on a leftover container
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "dev", "state.mock.json"))
	if err != nil {
		t.Fatalf("read mock state: %v", err)
	}
	raw = bytes.ReplaceAll(raw, []byte("dev@example.com"), []byte("me@example.com"))
	id := newTestID(t)
	raw = bytes.ReplaceAll(raw, []byte("deadbeef"), []byte(id))
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.json")
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wantDoc := statetest.Read(t, statePath)

	// same wiring as newLiveDeps, but over the seeded store
	h, _, _, pinOut, _, _ := newLiveDepsOnDir(t, dataDir)

	cookie := login(t, h, pinOut)
	deleteTestProject(t, h, cookie, id)

	// desired state survived the boot untouched
	statetest.AssertEqual(t, statePath, wantDoc)

	// the mock project is listed before any Docker interaction
	code, body := doJSON(t, h, cookie, http.MethodGet, "/api/projects/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("get seeded project: %d %v", code, body)
	}
	if body["status"] == "running" {
		t.Fatalf("fresh engine should not have a running container yet: %v", body["status"])
	}

	// first ensure (the UI's Terminal click path) reconciles: container
	// recreated AND repo re-cloned, since a fresh engine has no volumes
	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects/"+id+"/sessions", `{"name":"main"}`)
	if code != http.StatusCreated {
		t.Fatalf("create session on recovered container: %d %v", code, body)
	}
	waitForStatus(t, h, cookie, id, "running")

	// the repo from state.json is really there (git clone puts the working
	// tree at /workspace/repo; octocat/Hello-World ships a README)
	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects/exec",
		`{"projectIds":["`+id+`"],"command":"ls /workspace/repo"}`)
	if code != http.StatusOK {
		t.Fatalf("exec in recovered container: %d %v", code, body)
	}
	results, _ := body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("exec results: %v", body)
	}
	r0 := results[0].(map[string]any)
	if r0["status"] != "ok" {
		t.Fatalf("exec in recovered container failed: %v", r0)
	}
	if detail, _ := r0["detail"].(string); !strings.Contains(detail, "README") {
		t.Fatalf("repo not re-cloned: ls /workspace/repo = %q, want it to contain README", detail)
	}

	// recovery is derived state only: the on-disk desired state is untouched,
	// except for the session metadata this test itself created above —
	// sessions are persisted by design, so expect exactly that one addition.
	proj := wantDoc["projects"].(map[string]any)[id].(map[string]any)
	proj["sessions"].(map[string]any)["main"] = map[string]any{}
	statetest.AssertEqual(t, statePath, wantDoc)

	// the reconcile + re-clone is visible in the audit trail
	logged, err := os.ReadFile(filepath.Join(dataDir, "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{`"project.reconcile"`, `"project.clone"`} {
		if !bytes.Contains(logged, []byte(typ)) {
			t.Fatalf("events.log missing %s:\n%s", typ, logged)
		}
	}
}
