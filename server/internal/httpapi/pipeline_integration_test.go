//go:build integration

// The create pipeline against a live engine: create from a real local
// fixture repo (git daemon), reach Running with the clone present,
// stop/restart with volumes intact, delete every scope, plus the
// blank-project path. Run with:
// go test -tags=integration -count=1 ./internal/httpapi/.
package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sps/internal/auth"
	"sps/internal/docker"
	"sps/internal/events"
	"sps/internal/project"
)

// deleteProjectAll registers scope=all deletion as test cleanup, so a failed
// assertion can never leak a project (and its volumes) on the
// engine. Safe to call even when the test itself deletes the project: the
// final cleanup delete is a no-op 404.

// fixtureRepo creates a one-commit git repo under dir/repo and serves it
// with git daemon, returning a git:// URL reachable from containers via the
// sps-net gateway.

func TestProjectPipelineLifecycle(t *testing.T) {
	h, dkr, svc, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)
	url := fixtureRepo(t, dkr)

	// create → 201 with the exact metadata echo, running, clone present
	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects",
		fmt.Sprintf(`{"repoUrl":%q,"branch":"main"}`, url))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id, _ := body["id"].(string)
	deleteProjectAll(t, h, cookie, id)
	wantPayload := map[string]any{"id": id, "name": "repo", "repo": url, "branch": "main"}
	if id == "" || !reflect.DeepEqual(body, wantPayload) {
		t.Fatalf("create payload = %v, want %v", body, wantPayload)
	}

	ctx := context.Background()
	res, err := dkr.Exec(ctx, "sps-"+id, []string{"git", "-C", "/workspace/repo", "log", "--oneline"}, false)
	if err != nil || res.ExitCode != 0 || !bytes.Contains([]byte(res.Output), []byte("first")) {
		t.Fatalf("clone verification failed: %+v err=%v", res, err)
	}

	waitForStatus(t, h, cookie, id, "running")

	// stop → exited, volumes survive; restart → running again
	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects/"+id+"/stop", "")
	if code != http.StatusOK || !reflect.DeepEqual(body, map[string]any{"ok": true}) {
		t.Fatalf("stop: %d %v", code, body)
	}
	waitForStatus(t, h, cookie, id, "exited")

	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects/"+id+"/restart", "")
	if code != http.StatusOK || !reflect.DeepEqual(body, map[string]any{"ok": true}) {
		t.Fatalf("restart: %d %v", code, body)
	}
	waitForStatus(t, h, cookie, id, "running")
	res, err = dkr.Exec(ctx, "sps-"+id, []string{"cat", "/workspace/repo/hello.txt"}, false)
	if err != nil || res.ExitCode != 0 || res.Output != "hi\n" {
		t.Fatalf("repo volume did not survive restart: %+v err=%v", res, err)
	}

	// scoped deletes: the engine refuses to drop a volume referenced by any
	// container, so scope=repo takes the container with it (home survives)
	code, body = doJSON(t, h, cookie, http.MethodDelete, "/api/projects/"+id+"?scope=repo", "")
	if code != http.StatusOK || !reflect.DeepEqual(body, map[string]any{"ok": true}) {
		t.Fatalf("delete repo scope: %d %v", code, body)
	}
	if _, err := dkr.Inspect(ctx, "sps-"+id); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("after scope=repo inspect err = %v, want ErrNotFound", err)
	}
	entries, listErr := svc.List()
	wantEntries := []project.Entry{{ID: id, Name: "repo"}}
	if listErr != nil || !reflect.DeepEqual(entries, wantEntries) {
		t.Fatalf("metadata after scope=repo = %+v err=%v, want %v", entries, listErr, wantEntries)
	}
	code, body = doJSON(t, h, cookie, http.MethodDelete, "/api/projects/"+id+"?scope=all", "")
	if code != http.StatusOK || !reflect.DeepEqual(body, map[string]any{"ok": true}) {
		t.Fatalf("delete all: %d %v", code, body)
	}
	code, body = doJSON(t, h, cookie, http.MethodGet, "/api/projects/"+id, "")
	if code != http.StatusNotFound || !reflect.DeepEqual(body, map[string]any{"error": "no such project"}) {
		t.Fatalf("get after delete: %d %v", code, body)
	}
	if _, err := dkr.Inspect(ctx, "sps-"+id); err == nil {
		t.Fatal("container should be gone after scope=all")
	}
}

func TestBlankProjectLifecycle(t *testing.T) {
	h, dkr, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects", `{}`)
	if code != http.StatusCreated || body["name"] != "untitled" {
		t.Fatalf("blank create: %d %v", code, body)
	}
	id := body["id"].(string)
	deleteProjectAll(t, h, cookie, id)
	wantPayload := map[string]any{"id": id, "name": "untitled", "repo": "", "branch": ""}
	if !reflect.DeepEqual(body, wantPayload) {
		t.Fatalf("blank create payload = %v, want %v", body, wantPayload)
	}

	ctx := context.Background()
	waitForStatus(t, h, cookie, id, "running")
	res, err := dkr.Exec(ctx, "sps-"+id,
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

	// blank projects are named "untitled"; make sure list works too
	code, body = doJSON(t, h, cookie, http.MethodGet, "/api/projects", "")
	wantList := map[string]any{"projects": []any{map[string]any{"id": id, "name": "untitled"}}}
	if code != http.StatusOK || !reflect.DeepEqual(body, wantList) {
		t.Fatalf("list: got %d %v, want %v", code, body, wantList)
	}

	code, body = doJSON(t, h, cookie, http.MethodDelete, "/api/projects/"+id, "")
	if code != http.StatusOK || !reflect.DeepEqual(body, map[string]any{"ok": true}) {
		t.Fatalf("cleanup delete: %d %v", code, body)
	}
}

// hasEvent reports whether the event log contains a type with matching id.

func TestProjectBranchPinning(t *testing.T) {
	h, dkr, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)
	url := fixtureRepo(t, dkr)

	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects",
		fmt.Sprintf(`{"repoUrl":%q,"branch":"dev"}`, url))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["id"].(string)
	deleteProjectAll(t, h, cookie, id)
	wantPayload := map[string]any{"id": id, "name": "repo", "repo": url, "branch": "dev"}
	if !reflect.DeepEqual(body, wantPayload) {
		t.Fatalf("create payload = %v, want %v", body, wantPayload)
	}

	res, err := dkr.Exec(context.Background(), "sps-"+id,
		[]string{"git", "-C", "/workspace/repo", "rev-parse", "--abbrev-ref", "HEAD"}, false)
	if err != nil || res.ExitCode != 0 || res.Output != "dev\n" {
		t.Fatalf("branch pinning: got %+v err=%v, want HEAD on dev", res, err)
	}
	_, _ = doJSON(t, h, cookie, http.MethodDelete, "/api/projects/"+id, "")
}

func TestCloneFailureLiveKeepsProjectAndLogsError(t *testing.T) {
	h, _, svc, pinOut, ev, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// port 1 on the gateway: connection refused, deterministic failure
	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects",
		`{"repoUrl":"git://host.docker.internal:1/nope.git"}`)
	errMsg, _ := body["error"].(string)
	wantPrefix := "clone git://host.docker.internal:1/nope.git: "
	if code != http.StatusInternalServerError || !strings.HasPrefix(errMsg, wantPrefix) {
		t.Fatalf("clone failure: got %d %q, want 500 with %q…", code, errMsg, wantPrefix)
	}
	entries, listErr := svc.List()
	if listErr != nil || len(entries) != 1 {
		t.Fatalf("project must survive failed clone: %+v err=%v", entries, listErr)
	}
	wantEntries := []project.Entry{{ID: entries[0].ID, Name: "nope"}}
	if !reflect.DeepEqual(entries, wantEntries) {
		t.Fatalf("project must survive failed clone as %+v: %+v", wantEntries, entries)
	}
	id := entries[0].ID
	deleteProjectAll(t, h, cookie, id)
	code, body = doJSON(t, h, cookie, http.MethodGet, "/api/projects/"+id, "")
	wantStatus := map[string]any{"id": id, "name": "nope", "repo": "git://host.docker.internal:1/nope.git", "branch": "", "cloneMethod": "http", "status": "running"}
	if code != http.StatusOK || !reflect.DeepEqual(body, wantStatus) {
		t.Fatalf("post-failure status: got %d %v, want %v", code, body, wantStatus)
	}
	evs, err := ev.Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Type == "error" && e.Data["id"] == id && e.Data["op"] == "project.clone" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an error event with op=project.clone for id " + id)
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

	createBlank := func() string {
		t.Helper()
		code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects", `{}`)
		if code != http.StatusCreated {
			t.Fatalf("blank create: %d %v", code, body)
		}
		return body["id"].(string)
	}
	idA, idB := createBlank(), createBlank()
	deleteProjectAll(t, h, cookie, idA)
	deleteProjectAll(t, h, cookie, idB)
	if idA == idB {
		t.Fatal("ids collided")
	}
	waitForStatus(t, h, cookie, idA, "running")
	waitForStatus(t, h, cookie, idB, "running")

	// deleting A leaves B untouched
	if code, _ := doJSON(t, h, cookie, http.MethodDelete, "/api/projects/"+idA, ""); code != http.StatusOK {
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
	dkr2, err := docker.New(os.Getenv("SPS_DOCKER_SOCK"))
	if err != nil {
		t.Fatal(err)
	}
	h2 := New(Deps{Events: ev2, Version: "itest", Auth: auth2,
		Projects: project.NewService(project.Open(st), dkr2, ev2)})
	code, body := doJSON(t, h2, cookie, http.MethodGet, "/api/projects/"+idB, "")
	if code != http.StatusOK || body["status"] != "running" {
		t.Fatalf("restarted server lost the project: %d %v", code, body)
	}

	_, _ = doJSON(t, h2, cookie, http.MethodDelete, "/api/projects/"+idB, "")
}
