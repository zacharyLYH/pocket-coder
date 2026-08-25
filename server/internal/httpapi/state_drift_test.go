package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"

	"sps/internal/docker"
	"sps/internal/state"
	"sps/internal/state/statetest"
	"strings"
)

// TestStateSurvivesInterleavedAPITraffic is the state-file drift guard: a
// long, deliberately confusing sequence of interleaved API calls — keys,
// harnesses, projects, installs, batch commands, stop/start/restart, every
// delete scope, rejected inputs, and a failed clone — with Docker fully
// mocked (the engine cannot change desired state). Afterwards the on-disk
// state.json must be EXACTLY the sum of the successful calls: nothing more,
// nothing less. Any drift the mutation pipeline could introduce (double
// writes, missed deletes, partial rollbacks) fails here.
//
// Runs at unit speed (no build tag, no engine). The FE e2e suite covers UI
// paths; this test owns byte-exact end-state verification.
// projectIDByRepo finds the id of the most recent project.create event for
// repo — the only trace of a project whose create response carried an error.
func projectIDByRepo(t *testing.T, d Deps, repo string) string {
	t.Helper()
	evs, err := d.Events.Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, e := range evs {
		if e.Type == "project.create" && e.Data["repo"] == repo {
			id, _ = e.Data["id"].(string)
		}
	}
	if id == "" {
		t.Fatalf("no project.create event for repo %q", repo)
	}
	return id
}

func TestStateSurvivesInterleavedAPITraffic(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	if err := st.Mutate(func(doc *state.Document) error {
		doc.User.Email = "me@example.com"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d.Projects.SetSSHKeys(d.SSHKeys)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// ─── docker mock: permissive catch-alls (registered FIRST, so the
	// specific expectations below win). Only success/failure matters here.
	md.EXPECT().EnsureNetwork(mock.Anything, mock.Anything).Return(nil).Maybe()
	md.EXPECT().Inspect(mock.Anything, mock.Anything).
		Return(docker.Container{Running: true, Status: "running"}, nil).Maybe()
	md.EXPECT().InspectImage(mock.Anything, mock.Anything).Return(nil).Maybe()
	md.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil).Maybe()
	md.EXPECT().Stop(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	md.EXPECT().Start(mock.Anything, mock.Anything).Return(nil).Maybe()
	md.EXPECT().Remove(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	md.EXPECT().RemoveVolume(mock.Anything, mock.Anything).Return(nil).Maybe()
	md.EXPECT().WriteFile(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	// catch-all for every successful exec — disjoint from the failing clone
	// below (testify scans expectations in registration order, so a greedy
	// catch-all registered first would shadow the specific one)
	md.EXPECT().Exec(mock.Anything, mock.Anything, mock.MatchedBy(func(cmd []string) bool {
		for _, a := range cmd {
			if strings.Contains(a, "fail.example") {
				return false
			}
		}
		return true
	}), mock.Anything).
		Return(docker.ExecResult{ExitCode: 0, Output: "ok"}, nil).Maybe()
	// one project's clone fails — a failed create must leave NO state behind
	md.EXPECT().Exec(mock.Anything, mock.Anything, mock.MatchedBy(func(cmd []string) bool {
		return len(cmd) >= 3 && cmd[0] == "git" && cmd[1] == "clone" && cmd[len(cmd)-2] == "https://fail.example/x.git"
	}), mock.Anything).
		Return(docker.ExecResult{ExitCode: 128, Output: "fatal: repository not found"}, nil).Maybe()

	// ─── 1. ssh keys: add, duplicate-reject, add another ────────────────
	rec := authedPost(t, h, cookie, "/api/ssh-keys", `{"publicKey":"ssh-ed25519 AAAA-key-one","label":"laptop"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add key1: %d %q", rec.Code, rec.Body)
	}
	if rec := authedPost(t, h, cookie, "/api/ssh-keys", `{"publicKey":"ssh-ed25519 AAAA-key-one"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate key: %d, want 400", rec.Code)
	}
	if rec := authedPost(t, h, cookie, "/api/ssh-keys", `{"publicKey":"ssh-ed25519 AAAA-key-two"}`); rec.Code != http.StatusCreated {
		t.Fatalf("add key2: %d %q", rec.Code, rec.Body)
	}
	statetest.AssertSection(t, st.Path(), "sshKeys", []any{
		map[string]any{"fingerprint": "sha256-Bcu-3Z2LLPbarMquGC8r4w", "publicKey": "ssh-ed25519 AAAA-key-one", "label": "laptop", "email": "me@example.com"},
		map[string]any{"fingerprint": "sha256-_r_26MQJIPO1QjdZEfShlg", "publicKey": "ssh-ed25519 AAAA-key-two", "email": "me@example.com"},
	})

	// ─── 2. harnesses: add two, duplicate-reject ─────────────────────────
	if rec := authedPost(t, h, cookie, "/api/harnesses", `{"name":"My Agent","command":"my-agent","install":"npm i -g my-agent"}`); rec.Code != http.StatusCreated {
		t.Fatalf("add my-agent: %d %q", rec.Code, rec.Body)
	}
	if rec := authedPost(t, h, cookie, "/api/harnesses", `{"name":"Cfg Agent","command":"cfg-agent","install":"pip install cfg-agent"}`); rec.Code != http.StatusCreated {
		t.Fatalf("add cfg-agent: %d %q", rec.Code, rec.Body)
	}
	if rec := authedPost(t, h, cookie, "/api/harnesses", `{"name":"My Agent","command":"other"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate harness: %d, want 400", rec.Code)
	}
	statetest.AssertSection(t, st.Path(), "harnesses", map[string]any{
		"fake":      map[string]any{"id": "fake", "name": "Fake", "command": "fakecli", "install": "npm i -g fakecli"},
		"my-agent":  map[string]any{"id": "my-agent", "name": "My Agent", "command": "my-agent", "install": "npm i -g my-agent"},
		"cfg-agent": map[string]any{"id": "cfg-agent", "name": "Cfg Agent", "command": "cfg-agent", "install": "pip install cfg-agent"},
	})

	// ─── 3. projects: http, blank, ssh, branch-pinned, failed clone ─────
	create := func(body string) (int, string) {
		rec := authedPost(t, h, cookie, "/api/projects", body)
		var id string
		_ = json.Unmarshal(rec.Body.Bytes(), &struct {
			ID *string `json:"id"`
		}{&id})
		return rec.Code, id
	}
	codeA, idA := create(`{"repoUrl":"https://github.com/x/hello.git"}`)
	if codeA != http.StatusCreated {
		t.Fatalf("create A: %d %q", codeA, idA)
	}
	codeB, idB := create(`{}`)
	if codeB != http.StatusCreated {
		t.Fatalf("create B: %d %q", codeB, idB)
	}
	codeC, idC := create(`{"repoUrl":"git@github.com:me/private.git","cloneMethod":"ssh"}`)
	if codeC != http.StatusCreated {
		t.Fatalf("create C: %d %q", codeC, idC)
	}
	codeD, idD := create(`{"repoUrl":"https://github.com/x/hello.git","branch":"dev"}`)
	if codeD != http.StatusCreated {
		t.Fatalf("create D: %d %q", codeD, idD)
	}
	// a failed clone returns an error but KEEPS the project record on
	// purpose: the sandbox is up, the user can retry the clone. The failed
	// response carries no id — the audit trail is the record of it.
	codeE, _ := create(`{"repoUrl":"https://fail.example/x.git"}`)
	if codeE == http.StatusCreated {
		t.Fatalf("failed clone returned %d, want an error status", codeE)
	}
	idE := projectIDByRepo(t, d, "https://fail.example/x.git")
	statetest.AssertSection(t, st.Path(), "projects", map[string]any{
		idA: map[string]any{"name": "hello", "repo": "https://github.com/x/hello.git", "cloneMethod": "http"},
		idB: map[string]any{"name": "untitled", "repo": "", "cloneMethod": "http"},
		idC: map[string]any{"name": "private", "repo": "git@github.com:me/private.git", "cloneMethod": "ssh"},
		idD: map[string]any{"name": "hello", "repo": "https://github.com/x/hello.git", "branch": "dev", "cloneMethod": "http"},
		idE: map[string]any{"name": "x", "repo": "https://fail.example/x.git", "cloneMethod": "http"},
	})

	// ─── 4. interleaved operations ───────────────────────────────────────
	// installs (explicit, per project)
	if rec := authedPost(t, h, cookie, "/api/harnesses/my-agent/install", `{"projectIds":["`+idA+`","`+idC+`"]}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %d %q", rec.Code, rec.Body)
	}
	// batch commands
	if rec := authedPost(t, h, cookie, "/api/projects/exec", `{"projectIds":["`+idA+`","`+idB+`","`+idC+`"],"command":"echo hi"}`); rec.Code != http.StatusOK {
		t.Fatalf("exec: %d %q", rec.Code, rec.Body)
	}
	// a session restart (sessions never touch state, but the pipeline runs)
	if rec := authedPost(t, h, cookie, "/api/projects/"+idA+"/sessions/fake-1/restart", ""); rec.Code != http.StatusOK {
		t.Fatalf("session restart: %d %q", rec.Code, rec.Body)
	}
	// stop/start cycle
	if rec := authedRequest(t, h, cookie, http.MethodPost, "/api/projects/"+idB+"/stop"); rec.Code != http.StatusOK {
		t.Fatalf("stop: %d %q", rec.Code, rec.Body)
	}
	if rec := authedRequest(t, h, cookie, http.MethodPost, "/api/projects/"+idB+"/start"); rec.Code != http.StatusOK {
		t.Fatalf("start: %d %q", rec.Code, rec.Body)
	}
	// key1 is deleted after project C already consumed the key set
	if rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/ssh-keys/sha256-Bcu-3Z2LLPbarMquGC8r4w"); rec.Code != http.StatusOK {
		t.Fatalf("delete key1: %d %q", rec.Code, rec.Body)
	}
	// the harness that was installed into projects is removed from the
	// registry — installs are live state, the registry entry is desired state
	if rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/harnesses/my-agent"); rec.Code != http.StatusOK {
		t.Fatalf("delete harness: %d %q", rec.Code, rec.Body)
	}
	// rejected calls must not mutate: unknown project op, empty exec, bad key
	if rec := authedRequest(t, h, cookie, http.MethodPost, "/api/projects/ghost1234/start"); rec.Code != http.StatusNotFound {
		t.Fatalf("ghost op: %d, want 404", rec.Code)
	}
	if rec := authedPost(t, h, cookie, "/api/projects/exec", `{"projectIds":["`+idA+`"]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("exec without command: %d, want 400", rec.Code)
	}
	if rec := authedPost(t, h, cookie, "/api/ssh-keys", `{"publicKey":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad key: %d, want 400", rec.Code)
	}

	// ─── 5. deletes across every scope ───────────────────────────────────
	// scope=all: record + container + volumes
	if rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/"+idB+"?scope=all"); rec.Code != http.StatusOK {
		t.Fatalf("delete B: %d %q", rec.Code, rec.Body)
	}

	// scope=repo: container + repo volume go, the RECORD STAYS
	if rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/"+idC+"?scope=repo"); rec.Code != http.StatusOK {
		t.Fatalf("delete C: %d %q", rec.Code, rec.Body)
	}

	// scope=metadata: only the record goes
	if rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/"+idD+"?scope=metadata"); rec.Code != http.StatusOK {
		t.Fatalf("delete D: %d %q", rec.Code, rec.Body)
	}

	// one more project after all that churn
	codeF, idF := create(`{}`)
	if codeF != http.StatusCreated {
		t.Fatalf("create F: %d %q", codeF, idF)
	}

	// ─── final: the file is EXACTLY the sum of every successful call ────
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": "me@example.com"},
		"harnesses": map[string]any{
			"fake":      map[string]any{"id": "fake", "name": "Fake", "command": "fakecli", "install": "npm i -g fakecli"},
			"cfg-agent": map[string]any{"id": "cfg-agent", "name": "Cfg Agent", "command": "cfg-agent", "install": "pip install cfg-agent"},
		},
		"sshKeys": []any{
			map[string]any{"fingerprint": "sha256-_r_26MQJIPO1QjdZEfShlg", "publicKey": "ssh-ed25519 AAAA-key-two", "email": "me@example.com"},
		},
		"projects": map[string]any{
			idA: map[string]any{"name": "hello", "repo": "https://github.com/x/hello.git", "cloneMethod": "http"},
			// scope=repo removed the container, the record survives
			idC: map[string]any{"name": "private", "repo": "git@github.com:me/private.git", "cloneMethod": "ssh"},
			// the failed-clone project survives too (retryable sandbox)
			idE: map[string]any{"name": "x", "repo": "https://fail.example/x.git", "cloneMethod": "http"},
			idF: map[string]any{"name": "untitled", "repo": "", "cloneMethod": "http"},
		},
	})
}
