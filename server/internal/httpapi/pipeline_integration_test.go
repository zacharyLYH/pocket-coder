//go:build integration

// The create pipeline against a live engine: create from a real local
// fixture repo (git daemon), reach Running with the clone present,
// stop/restart with volumes intact, delete every scope. Projects are
// owner/repo (the fixture URL carries a unique owner per run), so the
// blank-project path no longer exists — it is covered as a 400 below.
// Run with:
// go test -tags=integration -count=1 ./internal/httpapi/.
package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pcoder/internal/auth"
	"pcoder/internal/docker"
	"pcoder/internal/events"
	"pcoder/internal/project"
	"pcoder/internal/testutil"
)

func TestProjectPipelineLifecycle(t *testing.T) {
	h, dkr, svc, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)
	url := fixtureRepo(t).URL

	// create → 201 with the exact metadata echo, running, clone present.
	// The helper registers a scope=all cleanup; intermediate scoped
	// deletes below are part of the test, the trailing cleanup delete
	// turns into a 404 which the helper accepts.
	id, body := createTestProject(t, h, cookie, url, "main", "")
	wantPayload := map[string]any{"id": id, "repo": url, "branch": "main"}
	if !reflect.DeepEqual(body, wantPayload) {
		t.Fatalf("create payload = %v, want %v", body, wantPayload)
	}
	cname := project.ContainerName(id)
	res, err := dkr.Exec(t.Context(), cname, []string{"git", "-C", "/workspace/repo", "log", "--oneline"}, false)
	if err != nil || res.ExitCode != 0 || !bytes.Contains([]byte(res.Output), []byte("first")) {
		t.Fatalf("clone verification failed: %+v err=%v", res, err)
	}

	waitForStatus(t, h, cookie, id, "running")

	// stop → exited, volumes survive; restart → running again
	code, body := doJSON(t, h, cookie, http.MethodPost, projectPath(id, "/stop"), "")
	if code != http.StatusOK || !reflect.DeepEqual(body, map[string]any{"ok": true}) {
		t.Fatalf("stop: %d %v", code, body)
	}
	waitForStatus(t, h, cookie, id, "exited")

	code, body = doJSON(t, h, cookie, http.MethodPost, projectPath(id, "/restart"), "")
	if code != http.StatusOK || !reflect.DeepEqual(body, map[string]any{"ok": true}) {
		t.Fatalf("restart: %d %v", code, body)
	}
	waitForStatus(t, h, cookie, id, "running")
	res, err = dkr.Exec(t.Context(), cname, []string{"cat", "/workspace/repo/hello.txt"}, false)
	if err != nil || res.ExitCode != 0 || res.Output != "hi\n" {
		t.Fatalf("repo volume did not survive restart: %+v err=%v", res, err)
	}

	// scoped deletes: the engine refuses to drop a volume referenced by any
	// container, so scope=repo takes the container with it (home survives)
	code, body = doJSON(t, h, cookie, http.MethodDelete, projectPath(id, "?scope=repo"), "")
	if code != http.StatusOK || !reflect.DeepEqual(body, map[string]any{"ok": true}) {
		t.Fatalf("delete repo scope: %d %v", code, body)
	}
	if _, err := dkr.Inspect(t.Context(), cname); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("after scope=repo inspect err = %v, want ErrNotFound", err)
	}
	entries, listErr := svc.List()
	wantEntries := []project.Entry{{ID: id}}
	if listErr != nil || !reflect.DeepEqual(entries, wantEntries) {
		t.Fatalf("metadata after scope=repo = %+v err=%v, want %v", entries, listErr, wantEntries)
	}
	code, body = doJSON(t, h, cookie, http.MethodDelete, projectPath(id, "?scope=all"), "")
	if code != http.StatusOK || !reflect.DeepEqual(body, map[string]any{"ok": true}) {
		t.Fatalf("delete all: %d %v", code, body)
	}
	code, body = doJSON(t, h, cookie, http.MethodGet, projectPath(id, ""), "")
	if code != http.StatusNotFound || !reflect.DeepEqual(body, map[string]any{"error": "no such project"}) {
		t.Fatalf("get after delete: %d %v", code, body)
	}
	if _, err := dkr.Inspect(t.Context(), cname); err == nil {
		t.Fatal("container should be gone after scope=all")
	}
}

func TestCreateValidationLive(t *testing.T) {
	h, dkr, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// blank URLs are refused before Docker (non-GitHub rejection is
	// covered by unit tests — the live hatch allows local fixtures)
	for _, bad := range []string{
		`{}`,
		`{"repoUrl":""}`,
	} {
		code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects", bad)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: got %d %v, want 400", bad, code, body)
		}
	}

	// a created project has the essentials and shows up in the list by id
	url := fixtureRepo(t).URL
	id, _ := createTestProject(t, h, cookie, url, "", "")

	ctx := context.Background()
	waitForStatus(t, h, cookie, id, "running")
	res, err := dkr.Exec(ctx, project.ContainerName(id),
		[]string{"sh", "-c", "command -v git && command -v tmux && command -v ss"}, false)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("project missing essentials: %+v err=%v", res, err)
	}
	for i, tool := range []string{"git", "tmux", "ss"} {
		line := strings.Split(strings.TrimSpace(res.Output), "\n")
		if filepath.Base(line[i]) != tool {
			t.Fatalf("essential %d = %q, want %q (output: %q)", i, line[i], tool, res.Output)
		}
	}

	code, body := doJSON(t, h, cookie, http.MethodGet, "/api/projects", "")
	wantList := map[string]any{"projects": []any{map[string]any{"id": id}}}
	if code != http.StatusOK || !reflect.DeepEqual(body, wantList) {
		t.Fatalf("list: got %d %v, want %v", code, body, wantList)
	}
}

// hasEvent reports whether the event log contains a type with matching id.

func TestProjectBranchPinning(t *testing.T) {
	h, dkr, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)
	url := fixtureRepo(t).URL

	id, _ := createTestProject(t, h, cookie, url, "dev", "")

	res, err := dkr.Exec(context.Background(), project.ContainerName(id),
		[]string{"git", "-C", "/workspace/repo", "rev-parse", "--abbrev-ref", "HEAD"}, false)
	if err != nil || res.ExitCode != 0 || res.Output != "dev\n" {
		t.Fatalf("branch pinning: got %+v err=%v, want HEAD on dev", res, err)
	}
}

func TestCloneFailureLiveKeepsProjectAndLogsError(t *testing.T) {
	h, _, svc, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// port 1 on the gateway: connection refused, deterministic failure.
	// The project survives per design, so we have to clean it up ourselves
	// (the helper would have fataled on the non-201 response).
	badURL := "git://host.docker.internal:1/itest-fail/nope.git"
	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects",
		`{"repoUrl":"git://host.docker.internal:1/itest-fail/nope.git"}`)
	errMsg, _ := body["error"].(string)
	wantPrefix := "clone " + badURL + ": "
	if code != http.StatusInternalServerError || !strings.HasPrefix(errMsg, wantPrefix) {
		t.Fatalf("clone failure: got %d %q, want 500 with %q…", code, errMsg, wantPrefix)
	}
	entries, listErr := svc.List()
	if listErr != nil || len(entries) != 1 {
		t.Fatalf("project must survive failed clone: %+v err=%v", entries, listErr)
	}
	wantEntries := []project.Entry{{ID: "itest-fail/nope"}}
	if !reflect.DeepEqual(entries, wantEntries) {
		t.Fatalf("project must survive failed clone as %+v: %+v", wantEntries, entries)
	}
	id := entries[0].ID
	deleteTestProject(t, h, cookie, id)
	code, body = doJSON(t, h, cookie, http.MethodGet, projectPath(id, ""), "")
	wantStatus := map[string]any{"id": id, "repo": badURL, "branch": "", "cloneMethod": "http", "status": "running", "quickCommands": nil}
	if code != http.StatusOK || !reflect.DeepEqual(body, wantStatus) {
		t.Fatalf("post-failure status: got %d %v, want %v", code, body, wantStatus)
	}
	code, body = doJSON(t, h, cookie, http.MethodGet, projectPath(id, "/observe?type=project.clone&level=error"), "")
	if code != http.StatusOK {
		t.Fatalf("observe project.clone: %d %v", code, body)
	}
	found := false
	for _, e := range body["logs"].([]any) {
		line, _ := e.(map[string]any)
		attrs, _ := line["attrs"].(map[string]any)
		if line["type"] == "project.clone" && attrs["detail"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a project.clone error line for id " + id)
	}
}

func TestCreateRejectsBadInputBeforeDocker(t *testing.T) {
	h, _, svc, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	for _, bad := range []string{
		`{"repoUrl":"--upload-pack=evil"}`,
		`{"repoUrl":"https://x/y.git","branch":"-b/evil"}`,
	} {
		code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects", bad)
		want := map[string]any{"error": "invalid input: repo url and branch must not start with \"-\""}
		if code != http.StatusBadRequest || !reflect.DeepEqual(body, want) {
			t.Fatalf("%s: got %d %v, want 400 %v", bad, code, body, want)
		}
	}
	entries, listErr := svc.List()
	if listErr != nil || len(entries) != 0 {
		t.Fatalf("rejected creates must leave no metadata: %+v err=%v", entries, listErr)
	}
}

func TestProjectIsolationAndRestartSurvival(t *testing.T) {
	h, _, _, pinOut, ev, st := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// one repo is one project: two distinct fixture repos, both tracked
	// by the helper and surviving any single cleanup racing the next create.
	idA, _ := createTestProject(t, h, cookie, fixtureRepo(t).URL, "", "")
	idB, _ := createTestProject(t, h, cookie, fixtureRepo(t).URL, "", "")
	if idA == idB {
		t.Fatal("ids collided")
	}
	waitForStatus(t, h, cookie, idA, "running")
	waitForStatus(t, h, cookie, idB, "running")

	// deleting A leaves B untouched
	if code, _ := doJSON(t, h, cookie, http.MethodDelete, projectPath(idA, ""), ""); code != http.StatusOK {
		t.Fatal("delete A failed")
	}
	waitForStatus(t, h, cookie, idB, "running")
	if !hasEvent(t, ev, "project.delete", idA) || hasEvent(t, ev, "project.delete", idB) {
		t.Fatal("delete events scoped to the wrong project")
	}

	// a fresh Service over the same data dir (server restart) still sees B
	// and can drive its container
	ev2, err := events.Open(filepath.Join(filepath.Dir(st.Path()), "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer ev2.Close()
	var pinOut2 bytes.Buffer
	auth2 := auth.New("me@example.com", []byte(testSecret), auth.ConsoleMailer{Out: &pinOut2})
	lc2 := testutil.NewLifecycle(t)
	h2 := New(Deps{Events: ev2, Version: "itest", Auth: auth2,
		Projects: project.NewService(project.Open(st), lc2.Docker())})
	code, body := doJSON(t, h2, cookie, http.MethodGet, projectPath(idB, ""), "")
	if code != http.StatusOK || body["status"] != "running" {
		t.Fatalf("restarted server lost the project: %d %v", code, body)
	}
}
