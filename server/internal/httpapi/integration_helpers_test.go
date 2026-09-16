//go:build integration

package httpapi

// Shared live-engine helpers, used by every integration test in this
// package (pipeline, sessions, terminal, sshkeys, recover).
//
// Project ids are owner/repo, so every test project is created under the
// "itest-*" owner namespace (local git-daemon fixtures) or "itest/<hex>"
// (hand-seeded state). The resulting containers (pcoder-itest-*) and named
// volumes are explicitly marked test artifacts on the host engine and easy
// to bulk-clean:
//
//	docker ps -a --filter 'name=pcoder-itest-'
//	docker volume ls --filter 'name=pcoder-itest-'
//
// All project helpers register a t.Cleanup that issues a scope=all delete via
// the authenticated API, ensuring containers and volumes are cleaned up on
// test completion.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"pcoder/internal/auth"
	"pcoder/internal/codemapthreads"
	"pcoder/internal/docker"
	"pcoder/internal/events"
	"pcoder/internal/harness"
	"pcoder/internal/project"
	"pcoder/internal/session"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
	"pcoder/internal/testutil"
)

// testIDPrefix marks hand-seeded test ids (see newTestID).
const testIDPrefix = "itest/"

func newLiveDeps(t *testing.T) (http.Handler, *docker.Docker, *project.Service, *bytes.Buffer, *events.Log, *state.Store) {
	t.Helper()
	return newLiveDepsOnDir(t, t.TempDir())
}

// newLiveDepsOnDir is newLiveDeps over an existing data dir (already holding
// state.json, e.g. a test seed) instead of a fresh TempDir.
func newLiveDepsOnDir(t *testing.T, dataDir string) (http.Handler, *docker.Docker, *project.Service, *bytes.Buffer, *events.Log, *state.Store) {
	t.Helper()
	lc := testutil.NewLifecycle(t)
	lc.EnsureNetwork(t)

	ev, err := events.Open(filepath.Join(dataDir, "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ev.Close() })

	dkr := lc.Docker()

	st, err := state.Open(dataDir, state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}

	var pinOut bytes.Buffer
	authSvc := auth.New("me@example.com", []byte(testSecret), auth.ConsoleMailer{Out: &pinOut})
	sshKeyStore := sshkeys.New(st)
	svc := project.NewService(project.Open(st), dkr, ev)
	svc.SetSSHKeys(sshKeyStore)
	// Live tests clone from a local git daemon, not GitHub.
	// Production (config.AllowAnyRepo false) requires GitHub.
	svc.SetAllowAnyRepo(true)
	h := New(Deps{Events: ev, Version: "itest", Auth: authSvc, Projects: svc,
		Sessions: session.New(dkr), Harnesses: harness.New(st), SSHKeys: sshKeyStore, State: st,
		Codemaps: codemapthreads.New(filepath.Join(dataDir, "codemaps"))})
	return h, dkr, svc, &pinOut, ev, st
}

// newTestID returns a fresh "itest/<8 hex>" identifier. Used by the
// recovery test, which seeds state.json by hand and must own the id
// before any API call.
func newTestID(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("newTestID: %v", err)
	}
	return testIDPrefix + hex.EncodeToString(b[:])
}

// projectPath builds an /api/projects/{id} URL with the id escaped —
// ids are owner/repo, and the slash must not split the route segment.
func projectPath(id, suffix string) string {
	return "/api/projects/" + url.PathEscape(id) + suffix
}

// createTestProject posts to /api/projects and registers a scope=all
// cleanup. The returned id is the create response's "id" field; the
// returned body is the full create response so callers can assert on
// the metadata echo. repoURL is required (cloning is the only way to
// create a project); branch and cloneMethod are optional.
func createTestProject(t *testing.T, h http.Handler, cookie *http.Cookie, repoURL, branch, cloneMethod string) (string, map[string]any) {
	t.Helper()
	if repoURL == "" {
		t.Fatal("createTestProject: repoURL is required")
	}
	body := fmt.Sprintf(`{"repoUrl":%q`, repoURL)
	if branch != "" {
		body += fmt.Sprintf(`,"branch":%q`, branch)
	}
	if cloneMethod != "" {
		body += fmt.Sprintf(`,"cloneMethod":%q`, cloneMethod)
	}
	body += `}`
	code, resp := doJSON(t, h, cookie, http.MethodPost, "/api/projects", body)
	if code != http.StatusCreated {
		t.Fatalf("createTestProject: %d %v", code, resp)
	}
	id, _ := resp["id"].(string)
	if id == "" || !strings.Contains(id, "/") {
		t.Fatalf("createTestProject: id %q is not owner/repo (response: %v)", id, resp)
	}
	deleteTestProject(t, h, cookie, id)
	return id, resp
}

// deleteTestProject schedules a t.Cleanup that issues a scope=all
// delete. Safe to call more than once: the second delete is a 404 and
// is treated as success.
func deleteTestProject(t *testing.T, h http.Handler, cookie *http.Cookie, id string) {
	t.Helper()
	t.Cleanup(func() {
		code, _ := doJSON(t, h, cookie, http.MethodDelete, projectPath(id, "?scope=all"), "")
		if code != http.StatusOK && code != http.StatusNotFound {
			t.Errorf("cleanup: delete project %s → %d", id, code)
		}
	})
}

func login(t *testing.T, h http.Handler, pinOut *bytes.Buffer) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/request-pin",
		bytes.NewBufferString(`{"email":"me@example.com"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("request pin: %d", rec.Code)
	}
	pin := pinRe.FindString(pinOut.String())
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/verify",
		strings.NewReader(fmt.Sprintf(`{"email":"me@example.com","pin":%q}`, pin)))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func doJSON(t *testing.T, h http.Handler, cookie *http.Cookie, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func fixtureRepo(t *testing.T) *testutil.GitDaemonFixture {
	t.Helper()
	return testutil.NewGitDaemonFixture(t)
}

func waitForStatus(t *testing.T, h http.Handler, cookie *http.Cookie, id, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		code, body := doJSON(t, h, cookie, http.MethodGet, projectPath(id, ""), "")
		if code == http.StatusOK && body["status"] == want {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("project %s never reached status %q within 30s", id, want)
}

func hasEvent(t *testing.T, ev *events.Log, typ, id string) bool {
	t.Helper()
	evs, err := ev.Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Type == typ && e.Data["id"] == id {
			return true
		}
	}
	return false
}

// dialSessionWS opens an authenticated terminal websocket. Shared by the
// sessions and terminal integration tests (their readers differ
// deliberately — exit-frame handling — so only dial+send are shared).
func dialSessionWS(t *testing.T, wsURL string, cookie *http.Cookie) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL,
		http.Header{"Cookie": []string{cookie.Name + "=" + cookie.Value}})
	if err != nil {
		t.Fatalf("dial %s: %v (resp %v)", wsURL, err, resp)
	}
	return conn
}

func wsSend(t *testing.T, conn *websocket.Conn, f map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(f)
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("send %v: %v", f, err)
	}
}
