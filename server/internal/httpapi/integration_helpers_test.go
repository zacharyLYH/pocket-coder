//go:build integration

package httpapi

// Shared live-engine helpers, used by every integration test in this
// package (pipeline, sessions, terminal, sshkeys, recover). Moved out of
// pipeline_integration_test.go so the pipeline file holds only its tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"pcoder/internal/auth"
	"pcoder/internal/docker"
	"pcoder/internal/events"
	"pcoder/internal/harness"
	"pcoder/internal/project"
	"pcoder/internal/session"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
)

func newLiveDeps(t *testing.T) (http.Handler, *docker.Docker, *project.Service, *bytes.Buffer, *events.Log, *state.Store) {
	t.Helper()
	return newLiveDepsOnDir(t, t.TempDir())
}

// newLiveDepsOnDir is newLiveDeps over an existing data dir (already holding
// state.json, e.g. a test seed) instead of a fresh TempDir.
func newLiveDepsOnDir(t *testing.T, dataDir string) (http.Handler, *docker.Docker, *project.Service, *bytes.Buffer, *events.Log, *state.Store) {
	t.Helper()
	ev, err := events.Open(filepath.Join(dataDir, "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ev.Close() })

	dkr, err := docker.New(os.Getenv("PCODER_DOCKER_SOCK"))
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := dkr.Ping(ctx); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}

	st, err := state.Open(dataDir, state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}

	var pinOut bytes.Buffer
	authSvc := auth.New("me@example.com", []byte(testSecret), auth.ConsoleMailer{Out: &pinOut})
	sshKeyStore := sshkeys.New(st)
	svc := project.NewService(project.Open(st), dkr, ev)
	svc.SetSSHKeys(sshKeyStore)
	h := New(Deps{Events: ev, Version: "itest", Auth: authSvc, Projects: svc,
		Sessions: session.New(dkr), Harnesses: harness.New(st), SSHKeys: sshKeyStore, State: st})
	return h, dkr, svc, &pinOut, ev, st
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

func deleteProjectAll(t *testing.T, h http.Handler, cookie *http.Cookie, id string) {
	t.Helper()
	t.Cleanup(func() {
		code, _ := doJSON(t, h, cookie, http.MethodDelete, "/api/projects/"+id+"?scope=all", "")
		if code != http.StatusOK && code != http.StatusNotFound {
			t.Errorf("cleanup: delete project %s → %d", id, code)
		}
	})
}

func fixtureRepo(t *testing.T, dkr *docker.Docker) string {
	t.Helper()
	if err := dkr.EnsureNetwork(context.Background(), docker.DefaultNetwork); err != nil {
		t.Fatalf("ensure network: %v", err)
	}

	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if out, err := exec.Command("git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		env := append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("-C", repo, "add", "-A")
	run("-C", repo, "commit", "-m", "first")
	run("-C", repo, "branch", "dev") // for branch-pinning tests

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	daemon := exec.Command("git", "daemon",
		"--base-path="+dir, "--export-all", "--reuseaddr",
		"--listen=0.0.0.0", "--port="+fmt.Sprint(port))
	if err := daemon.Start(); err != nil {
		t.Fatalf("git daemon: %v", err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill(); _, _ = daemon.Process.Wait() })

	return fmt.Sprintf("git://host.docker.internal:%d/repo", port)
}

func waitForStatus(t *testing.T, h http.Handler, cookie *http.Cookie, id, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		code, body := doJSON(t, h, cookie, http.MethodGet, "/api/projects/"+id, "")
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
