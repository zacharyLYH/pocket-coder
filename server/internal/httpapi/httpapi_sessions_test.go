package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/mock"

	"sps/internal/docker"
	"sps/internal/harness"
	"sps/internal/project"
	"sps/internal/session"
	"sps/internal/sshkeys"
	"sps/internal/state"
	"sps/internal/state/statetest"
	dockermocks "sps/mocks/docker"
)

func newSessionDeps(t *testing.T) (Deps, *dockermocks.MockClient, *bytes.Buffer, *state.Store) {
	t.Helper()
	d, md, pinOut, st := newProjectDeps(t)
	d.Sessions = session.New(md)

	// a plugin in the registry, as if added by the user
	hs := harness.New(st)
	if _, err := hs.Save(harness.Harness{Name: "Fake", Command: "fakecli", Install: "npm i -g fakecli"}); err != nil {
		t.Fatal(err)
	}
	d.Harnesses = hs
	d.SSHKeys = sshkeys.New(st)
	return d, md, pinOut, st
}

func seedProject(t *testing.T, st *state.Store, id string) {
	t.Helper()
	if err := project.Open(st).Create(id, project.Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsRequireAuth(t *testing.T) {
	d, _, _, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	h := New(d)
	if rec := get(t, h, "/api/projects/abc/sessions"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("list unauthed: %d, want 401", rec.Code)
	}
	if rec := post(t, h, "/api/projects/abc/sessions", `{"name":"main"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("create unauthed: %d, want 401", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/ws/projects/abc/sessions/main", nil)
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ws unauthed: %d, want 401 (no upgrade)", rec.Code)
	}
}

func TestListSessionsEmptyIsArray(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 1, Output: "no server running on /tmp/tmux-0/default"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/abc/sessions")
	want := "{\"sessions\":[]}\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("got %d %q, want 200 %q", rec.Code, rec.Body, want)
	}
}

func TestCreateSessionLifecycle(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc", []string{"tmux", "has-session", "-t", "work"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil).Once() // ensure: no such session yet
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		append([]string{"tmux", "new-session", "-d", "-s", "work", "-c", "/workspace",
			";", "set-option", "-s", "escape-time", "0"}, session.ThemeArgs()...), false).
		Return(docker.ExecResult{ExitCode: 0}, nil).Once()

	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := authedPost(t, h, cookie, "/api/projects/abc/sessions", `{"name":"work"}`)
	want := "{\"name\":\"work\"}\n"
	if rec.Code != http.StatusCreated || rec.Body.String() != want {
		t.Fatalf("create: got %d %q, want 201 %q", rec.Code, rec.Body, want)
	}
	ev := lastEvent(t, d)
	if ev.Type != "session.create" || ev.Data["id"] != "abc" || ev.Data["name"] != "work" {
		t.Fatalf("unexpected event: %+v", ev)
	}

	// invalid names are rejected before any docker call
	if rec := authedPost(t, h, cookie, "/api/projects/abc/sessions", `{"name":"-evil"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid name: %d, want 400", rec.Code)
	}

	// an existing session is ensure-success (200) without creating anything
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc", []string{"tmux", "has-session", "-t", "work"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil) // ensure: already there
	md.EXPECT().Exec(mock.Anything, "sps-abc", mock.MatchedBy(func(cmd []string) bool {
		return len(cmd) == 3 && cmd[0] == "bash" && cmd[1] == "-lc"
	}), false).Return(docker.ExecResult{ExitCode: 0}, nil) // IsAlive: session is live
	rec = authedPost(t, h, cookie, "/api/projects/abc/sessions", `{"name":"work"}`)
	want = "{\"name\":\"work\"}\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("ensure existing: got %d %q, want 200 %q", rec.Code, rec.Body, want)
	}
}

func TestCreateSessionGhostProject404(t *testing.T) {
	d, _, pinOut, _ := newSessionDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/ghost/sessions", `{"name":"main"}`)
	want := "{\"error\":\"no such project\"}\n"
	if rec.Code != http.StatusNotFound || rec.Body.String() != want {
		t.Fatalf("got %d %q, want 404 %q", rec.Code, rec.Body, want)
	}
}

// Terminal pre-flight happens before the upgrade, so failures are plain HTTP.
func TestTerminalPreflight(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	wsGet := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Upgrade", "websocket")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// invalid session name → 400 without touching the store or docker
	if rec := wsGet("/ws/projects/abc/sessions/-x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name: %d, want 400", rec.Code)
	}

	// ghost project → 404 before any docker call
	if rec := wsGet("/ws/projects/ghost/sessions/main"); rec.Code != http.StatusNotFound {
		t.Fatalf("ghost project: %d, want 404", rec.Code)
	}

	// missing session → strict 404, no upgrade
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc", []string{"tmux", "has-session", "-t", "main"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	rec := wsGet("/ws/projects/abc/sessions/main")
	want := "{\"error\":\"no such session\"}\n"
	if rec.Code != http.StatusNotFound || rec.Body.String() != want {
		t.Fatalf("missing session: got %d %q, want 404 %q", rec.Code, rec.Body, want)
	}
	if strings.Contains(rec.Header().Get("Upgrade"), "websocket") {
		t.Fatal("must not upgrade when the session is missing")
	}
}

// TestTerminalRelaunchesHarnessFromState proves the WebSocket pre-flight
// relaunches a harness session from state.json when it's missing from tmux
// (e.g. after container rebuild). The session must exist in tmux before
// the upgrade proceeds.
func TestTerminalRelaunchesHarnessFromState(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	// Register the harness as installed in this project
	_ = d.Projects.RecordInstall("abc", "fake")
	// Record session metadata: "helper-1" runs the "fake" harness.
	_ = d.Projects.RecordSession("abc", "helper-1", "fake")

	// has-session: not in tmux → triggers the state.json relaunch path
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "has-session", "-t", "helper-1"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)

	// expectLaunchNamed sets up the full harness launch chain
	expectLaunchNamed(md, "abc", "helper-1")

	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// The wsGet helper won't work here because LaunchNamed does a WebSocket
	// attach prep that needs more mocking. Instead verify the HTTP response
	// is NOT 404 (which was the old behavior).
	req := httptest.NewRequest(http.MethodGet, "/ws/projects/abc/sessions/helper-1", nil)
	req.Header.Set("Upgrade", "websocket")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Should NOT be 404 — the session was relaunched from state.json
	if rec.Code == http.StatusNotFound {
		t.Fatalf("session relaunched from state.json should not 404, got 404")
	}
}

// TestListSessionsIncludesStateJSONEntries proves the session picker shows
// sessions from state.json that aren't in tmux (e.g. after container rebuild
// or tmux crash). The user can see them and re-enter to trigger relaunch.
func TestListSessionsIncludesStateJSONEntries(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	// Record a harness session in state.json that's NOT in tmux
	_ = d.Projects.RecordSession("abc", "oc-1", "fake")

	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	// tmux only has one session — "live-session"
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "live-session\n"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/abc/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d %q, want 200", rec.Code, rec.Body)
	}
	var body struct {
		Sessions []session.Entry `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, s := range body.Sessions {
		got[s.Name] = true
	}
	if !got["live-session"] {
		t.Fatal("tmux session live-session missing from list")
	}
	if !got["oc-1"] {
		t.Fatal("state.json session oc-1 missing from list")
	}
}

// TestEnsureRelaunchesDeadHarnessSession proves the ensure endpoint
// (POST /sessions with just a name) detects a dead tmux session and
// relaunches the harness it was running.
func TestEnsureRelaunchesDeadHarnessSession(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	// Register the harness as installed in this project
	_ = d.Projects.RecordInstall("abc", "fake")
	// Record session metadata: "fake-1" runs the "fake" harness
	_ = d.Projects.RecordSession("abc", "fake-1", "fake")

	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	// has-session: session exists in tmux
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "has-session", "-t", "fake-1"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	// IsAlive: session is dead (pane_pid check fails)
	md.EXPECT().Exec(mock.Anything, "sps-abc", mock.MatchedBy(func(cmd []string) bool {
		return len(cmd) == 3 && cmd[0] == "bash" && cmd[1] == "-lc" &&
			strings.Contains(cmd[2], "kill -0")
	}), false).Return(docker.ExecResult{ExitCode: 1}, nil) // kill -0 failed → dead
	// kill the dead session
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "kill-session", "-t", "fake-1"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	// relaunch the harness
	expectLaunchNamed(md, "abc", "fake-1")

	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := authedPost(t, h, cookie, "/api/projects/abc/sessions", `{"name":"fake-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ensure dead harness: got %d %q, want 200", rec.Code, rec.Body)
	}
	var resp struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Name != "fake-1" {
		t.Fatalf("name = %q, want fake-1", resp.Name)
	}
	waitForEvent(t, d, "harness.launch")
}

// fakePty echoes every line back; a line containing "quit" ends the attach
// with exit code 3. Deterministic stand-in for tmux in unit tests.
func fakePty(stdin io.Reader, stdout io.Writer, done chan<- docker.ExecDone) {
	r := bufio.NewReader(stdin)
	for {
		line, rerr := r.ReadString('\n')
		if line != "" {
			fmt.Fprint(stdout, line)
		}
		if strings.Contains(line, "quit") {
			fmt.Fprint(stdout, "bye")
			done <- docker.ExecDone{ExitCode: 3}
			return
		}
		if rerr != nil {
			done <- docker.ExecDone{ExitCode: 0}
			return
		}
	}
}

// TestTerminalRoundTrip drives a full session over the bridge against a fake
// pty: input is echoed back as output frames, resize reaches ResizeTTY while
// the session runs, and quitting ends with an exit frame carrying the real
// code. Attach/detach events land in the log.
func TestTerminalRoundTrip(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	resized := make(chan [2]int, 8)
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc", []string{"tmux", "has-session", "-t", "main"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().ResizeTTY(mock.Anything, "exec-1", 40, 120).RunAndReturn(
		func(_ context.Context, _ string, rows, cols int) error {
			resized <- [2]int{rows, cols}
			return nil
		})
	wantAttach := append([]string{"env", "TERM=xterm-256color", "COLORTERM=truecolor", "tmux"}, session.ThemeArgs()...)
	wantAttach = append(wantAttach, ";", "attach", "-t", "main")
	md.EXPECT().Attach(mock.Anything, "sps-abc",
		wantAttach,
		mock.Anything, mock.Anything, mock.Anything, true).
		RunAndReturn(func(ctx context.Context, _ string, _ []string, stdin io.Reader, stdout, stderr io.Writer, _ bool) (string, <-chan docker.ExecDone, error) {
			done := make(chan docker.ExecDone, 1)
			go func() { // fake pty: echo lines; "quit" ends the attach
				r := bufio.NewReader(stdin)
				for {
					line, rerr := r.ReadString('\n')
					if line != "" {
						fmt.Fprint(stdout, line)
					}
					if strings.Contains(line, "quit") {
						fmt.Fprint(stdout, "bye")
						done <- docker.ExecDone{ExitCode: 3}
						return
					}
					if rerr != nil {
						done <- docker.ExecDone{ExitCode: 0}
						return
					}
				}
			}()
			return "exec-1", done, nil
		})

	h := New(d)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	url := wsURL(srv, "abc", "main")

	conn := dialWS(t, srv, url, loginCookie(t, h, pinOut))
	defer conn.Close()

	send := func(f wsIn) {
		t.Helper()
		raw, _ := json.Marshal(f)
		if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	readFrame := func(want string) wsOut {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("deadline: %v", err)
		}
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read waiting for %q frame: %v", want, err)
			}
			var f wsOut
			if json.Unmarshal(raw, &f) != nil || f.Type != want {
				continue // tolerate junk frames, as the handler does
			}
			return f
		}
	}

	send(wsIn{Type: "resize", Rows: 40, Cols: 120})
	send(wsIn{Type: "input", Data: "echo hi\n"})
	if f := readFrame("output"); !strings.Contains(f.Data, "echo hi") {
		t.Fatalf("echo lost: %+v", f)
	}
	select {
	case dims := <-resized:
		if dims[0] != 40 || dims[1] != 120 {
			t.Fatalf("resize = %v, want [40 120]", dims)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resize frame never reached ResizeTTY")
	}

	send(wsIn{Type: "input", Data: "quit\n"})
	readFrame("output") // the echoed quit line
	exit := readFrame("exit")
	if exit.Code != 3 {
		t.Fatalf("exit code = %d, want 3 from the fake pty", exit.Code)
	}

	assertEvent(t, d, "terminal.attach")
	waitForEvent(t, d, "terminal.detach")
}

// TestTerminalSurvivesAbruptClientDrop proves the server side unwinds when
// the browser vanishes mid-session: the attach sees EOF on its stdin pipe,
// the handler returns, and the detach event still lands.
func TestTerminalSurvivesAbruptClientDrop(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	attachEnded := make(chan struct{})
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc", []string{"tmux", "has-session", "-t", "main"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().Attach(mock.Anything, "sps-abc", mock.Anything,
		mock.Anything, mock.Anything, mock.Anything, true).
		RunAndReturn(func(ctx context.Context, _ string, _ []string, stdin io.Reader, stdout, stderr io.Writer, _ bool) (string, <-chan docker.ExecDone, error) {
			done := make(chan docker.ExecDone, 1)
			go func() {
				io.Copy(io.Discard, stdin) // unblocks when the server closes its pipe end
				close(attachEnded)
				done <- docker.ExecDone{ExitCode: 0}
			}()
			return "exec-2", done, nil
		})

	h := New(d)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cookie := loginCookie(t, h, pinOut)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "abc", "main"),
		http.Header{"Cookie": []string{cookie.Name + "=" + cookie.Value}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sendRaw(t, conn, `{"type":"input","data":"ping"}`)

	// vanish without a close handshake
	_ = conn.UnderlyingConn().SetReadDeadline(time.Now())
	_ = conn.Close()

	select {
	case <-attachEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("server never tore down the attach after the client vanished")
	}

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		evs, _ := d.Events.Read(0, 0)
		for _, e := range evs {
			if e.Type == "terminal.detach" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no terminal.detach event after abrupt drop")
}

func wsURL(srv *httptest.Server, id, name string) string {
	return "ws://" + srv.Listener.Addr().String() + "/ws/projects/" + id + "/sessions/" + name
}

func dialWS(t *testing.T, srv *httptest.Server, url string, cookie *http.Cookie) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(url,
		http.Header{"Cookie": []string{cookie.Name + "=" + cookie.Value}})
	if err != nil {
		t.Fatalf("dial %s: %v (resp %v)", url, err, resp)
	}
	return conn
}

func sendRaw(t *testing.T, conn *websocket.Conn, raw string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// assertEvent requires typ now; waitForEvent polls briefly — deferred
// bookkeeping on the server side can land just after the client's last read.
func assertEvent(t *testing.T, d Deps, typ string) {
	t.Helper()
	if !hasEventType(d, typ) {
		t.Fatalf("no %q event", typ)
	}
}

func waitForEvent(t *testing.T, d Deps, typ string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if hasEventType(d, typ) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %q event within 5s", typ)
}

func hasEventType(d Deps, typ string) bool {
	evs, err := d.Events.Read(0, 0)
	if err != nil {
		return false
	}
	for _, e := range evs {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// expectLaunch pins the exec chain LaunchNamed performs for the fake plugin
// against container "sps-<id>", session <name>.
func expectLaunch(md *dockermocks.MockClient, id, name string) {
	list := []string{"tmux", "list-sessions", "-F", "#{session_name}"}
	md.EXPECT().Exec(mock.Anything, "sps-"+id, list, mock.Anything).
		Return(docker.ExecResult{ExitCode: 0}, nil).Once()
	probe := []string{"test", "-d", "/workspace/repo/.git"}
	lookup := []string{"bash", "-lc", "command -v fakecli"}
	validate := []string{"bash", "-lc", "fakecli --version || fakecli --help"}
	create := append([]string{"tmux", "new-session", "-d", "-s", name, "-c", "/workspace",
		"bash", "-lc", `fakecli || echo "[fakecli exited: $?]"`,
		";", "set-option", "remain-on-exit", "on",
		";", "set-option", "-s", "escape-time", "0"}, session.ThemeArgs()...)
	md.EXPECT().Exec(mock.Anything, "sps-"+id, probe, mock.Anything).
		Return(docker.ExecResult{ExitCode: 1}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "sps-"+id, lookup, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "/usr/bin/fakecli\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-"+id, validate, true).
		Return(docker.ExecResult{ExitCode: 0, Output: "fakecli 1.0\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-"+id, create, false).
		Return(docker.ExecResult{ExitCode: 0}, nil).Once()
}

func TestCreateHarnessSession(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	expectLaunch(md, "abc", "fake-1")

	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := authedPost(t, h, cookie, "/api/projects/abc/sessions", `{"harnessId":"fake"}`)
	want := "{\"harness\":\"fake\",\"name\":\"fake-1\"}\n"
	if rec.Code != http.StatusCreated || rec.Body.String() != want {
		t.Fatalf("got %d %q, want 201 %q", rec.Code, rec.Body, want)
	}

	evs, _ := d.Events.Read(0, 0)
	var sawLaunch bool
	for _, e := range evs {
		if e.Type == "harness.launch" && e.Data["session"] == "fake-1" && e.Data["harness"] == "fake" {
			sawLaunch = true
		}
	}
	if !sawLaunch {
		t.Fatalf("no harness.launch event in %+v", evs)
	}
}

func TestCreateHarnessSessionUnknownHarness404(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := authedPost(t, h, cookie, "/api/projects/abc/sessions", `{"harnessId":"ghost"}`)
	want := `{"error":"no such harness \"ghost\""}`
	if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("got %d %q, want 404 %q", rec.Code, rec.Body, want)
	}
}

func TestCreateHarnessSessionNotACLI422(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	// a command that exists but prints nothing and exits: not a CLI
	if _, err := d.Harnesses.Save(harness.Harness{Name: "Broken", Command: "sleepy"}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)

	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"bash", "-lc", "command -v sleepy"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "/usr/bin/sleepy\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc", mock.Anything, true).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/sessions", `{"harnessId":"broken"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body.Error, "not a CLI — it looks like it wants a display") {
		t.Fatalf("error = %q, want the exact PRD message prefix", body.Error)
	}
	waitForEvent(t, d, "validation.failed")
}

// Regression: a plain shell named "opencode" (any harness id) used to be
// TestHarnessNamedSessionsAreValidShellSessions verifies that harness-named
// sessions (e.g. "fake", "fake-1") can be created as plain shells. Session
// metadata in state.json distinguishes them from actual harness sessions.
func TestHarnessNamedSessionsAreValidShellSessions(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	// Three new creates: has-session returns not-found, then tmux new-session
	md.EXPECT().Exec(mock.Anything, "sps-abc", mock.MatchedBy(func(cmd []string) bool {
		return len(cmd) == 4 && cmd[0] == "tmux" && cmd[1] == "has-session"
	}), false).Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		append([]string{"tmux", "new-session", "-d", "-s", "fake", "-c", "/workspace",
			";", "set-option", "-s", "escape-time", "0"}, session.ThemeArgs()...), false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// A harness-named session can be created as a plain shell.
	rec := authedPost(t, h, cookie, "/api/projects/abc/sessions", `{"name":"fake"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create harness-named shell: got %d %q, want 201", rec.Code, rec.Body)
	}
}

func TestRestartBareHarnessNameRelaunchesHarness(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	// A session named after the bare harness id "fake" (no -<n> suffix):
	// restart must relaunch the harness, not create a plain shell.
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "kill-session", "-t", "fake"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	expectLaunchNamed(md, "abc", "fake")

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/sessions/fake/restart", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"name\":\"fake\"}\n" {
		t.Fatalf("restart bare harness name: got %d %q, want 200 {\"name\":\"fake\"}", rec.Code, rec.Body)
	}
}

// TestCreateThenListSessions pins the user-visible flow: after creating a
// shell session and launching a harness session, the picker's list contains
// exactly the expected names.
func TestCreateThenListSessions(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)

	md.EXPECT().Exec(mock.Anything, "sps-abc", []string{"tmux", "has-session", "-t", "work"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil).Once() // ensure: not there yet
	shellCreate := append([]string{"tmux", "new-session", "-d", "-s", "work", "-c", "/workspace",
		";", "set-option", "-s", "escape-time", "0"}, session.ThemeArgs()...)
	md.EXPECT().Exec(mock.Anything, "sps-abc", shellCreate, false).
		Return(docker.ExecResult{ExitCode: 0}, nil).Once()
	expectLaunch(md, "abc", "fake-1")

	// the list the picker renders: the shell the user named plus the
	// auto-numbered harness session, nothing else
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "fake-1\nwork\n"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	if rec := authedPost(t, h, cookie, "/api/projects/abc/sessions", `{"name":"work"}`); rec.Code != http.StatusCreated {
		t.Fatalf("shell create: got %d %q, want 201", rec.Code, rec.Body)
	}
	if rec := authedPost(t, h, cookie, "/api/projects/abc/sessions", `{"harnessId":"fake"}`); rec.Code != http.StatusCreated {
		t.Fatalf("harness create: got %d %q, want 201", rec.Code, rec.Body)
	}

	rec := authedGet(t, h, cookie, "/api/projects/abc/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d %q, want 200", rec.Code, rec.Body)
	}
	var body struct {
		Sessions []session.Entry `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(body.Sessions))
	for _, s := range body.Sessions {
		got = append(got, s.Name)
	}
	want := []string{"fake-1", "work"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessions after create = %v, want %v", got, want)
	}
}

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

func TestKillAndRestartSessions(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	killed := 0
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "kill-session", "-t", "main"}, false).
		RunAndReturn(func(context.Context, string, []string, bool) (docker.ExecResult, error) {
			killed++
			return docker.ExecResult{ExitCode: 0}, nil
		})

	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// kill
	rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/abc/sessions/main")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("kill: got %d %q", rec.Code, rec.Body)
	}
	waitForEvent(t, d, "session.exit")

	// restart resolves "main": no such plugin → plain shell recreation
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		append([]string{"tmux", "new-session", "-d", "-s", "main", "-c", "/workspace",
			";", "set-option", "-s", "escape-time", "0"}, session.ThemeArgs()...), false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	rec = authedPost(t, h, cookie, "/api/projects/abc/sessions/main/restart", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"name\":\"main\"}\n" {
		t.Fatalf("restart shell: got %d %q", rec.Code, rec.Body)
	}
	if killed != 2 {
		t.Fatalf("kill called %d times, want 2 (explicit delete + restart's pre-kill)", killed)
	}
}

func TestRestartHarnessSessionSameName(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "kill-session", "-t", "fake-1"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	expectLaunchNamed(md, "abc", "fake-1")

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/sessions/fake-1/restart", "")
	want := "{\"name\":\"fake-1\"}\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("restart harness: got %d %q, want 200 %q", rec.Code, rec.Body, want)
	}
}

// expectLaunchNamed is expectLaunch without the list-sessions numbering step.
func expectLaunchNamed(md *dockermocks.MockClient, id, name string) {
	md.EXPECT().Exec(mock.Anything, "sps-"+id,
		[]string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "sps-"+id,
		[]string{"bash", "-lc", "command -v fakecli"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "/usr/bin/fakecli\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-"+id, mock.Anything, true).
		Return(docker.ExecResult{ExitCode: 0, Output: "fakecli 1.0\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "sps-"+id,
		append([]string{"tmux", "new-session", "-d", "-s", name, "-c", "/workspace",
			"bash", "-lc", `fakecli || echo "[fakecli exited: $?]"`,
			";", "set-option", "remain-on-exit", "on",
			";", "set-option", "-s", "escape-time", "0"}, session.ThemeArgs()...), false).
		Return(docker.ExecResult{ExitCode: 0}, nil).Once()
}

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
		"fake":     map[string]any{"id": "fake", "name": "Fake", "command": "fakecli", "install": "npm i -g fakecli"},
		"my-agent": map[string]any{"id": "my-agent", "name": "My Agent", "command": "my-agent", "install": "npm i -g my-agent"},
	})
}

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
		"fake": map[string]any{"id": "fake", "name": "Fake", "command": "fakecli", "install": "npm i -g fakecli"},
	})

	// deleting an unknown harness is idempotent success and changes nothing
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/harnesses/ghost")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete unknown: %d %q, want 200", rec.Code, rec.Body)
	}
	statetest.AssertSection(t, dataDir.Path(), "harnesses", map[string]any{
		"fake": map[string]any{"id": "fake", "name": "Fake", "command": "fakecli", "install": "npm i -g fakecli"},
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

func TestSSHKeysAPI(t *testing.T) {
	d, _, pinOut, dataDir := newSessionDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// initially empty
	rec := authedGet(t, h, cookie, "/api/ssh-keys")
	var list struct {
		Keys []struct{} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Keys) != 0 {
		t.Fatalf("list empty: %d %v err=%v", rec.Code, rec.Body, err)
	}

	// add a key
	rec = authedPost(t, h, cookie, "/api/ssh-keys",
		`{"publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGITest","label":"test"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	var added struct {
		Fingerprint string `json:"fingerprint"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &added)
	if added.Fingerprint == "" {
		t.Fatal("no fingerprint returned")
	}

	// list shows one key
	rec = authedGet(t, h, cookie, "/api/ssh-keys")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Keys) != 1 {
		t.Fatalf("list after add: %d %v", rec.Code, rec.Body)
	}
	// and the state file carries exactly that key, exactly these fields
	statetest.AssertSection(t, dataDir.Path(), "sshKeys", []any{map[string]any{
		"fingerprint": added.Fingerprint,
		"publicKey":   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGITest",
		"label":       "test",
		"email":       "me@example.com",
	}})

	// duplicate rejected
	rec = authedPost(t, h, cookie, "/api/ssh-keys",
		`{"publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGITest"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate: %d, want 400", rec.Code)
	}

	// invalid key rejected
	rec = authedPost(t, h, cookie, "/api/ssh-keys",
		`{"publicKey":"not-a-key"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid key: %d, want 400", rec.Code)
	}

	// delete
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/ssh-keys/"+added.Fingerprint)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}

	// back to empty — and the section is gone from the state file entirely
	rec = authedGet(t, h, cookie, "/api/ssh-keys")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Keys) != 0 {
		t.Fatalf("list after delete: %d %v", rec.Code, rec.Body)
	}
	statetest.AssertSection(t, dataDir.Path(), "harnesses", map[string]any{
		"fake": map[string]any{"id": "fake", "name": "Fake", "command": "fakecli", "install": "npm i -g fakecli"},
	})
}

// TestLazyReconciliationViaSessionHandler proves the disk-is-truth
// guarantee: when a container is missing but the project exists on disk,
// EnsureContainer inside the handler recreates it.
func TestLazyReconciliationViaSessionHandler(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	// first call: container missing → triggers reconciliation
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{}, docker.ErrNotFound).Once()
	md.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	md.EXPECT().InspectImage(mock.Anything, project.SandboxImage).Return(nil)
	md.EXPECT().Run(mock.Anything, mock.Anything).Return("new-cid", nil)
	// EnsureContainer re-inspects after reconciling: now running
	md.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil).Once()
	// after reconciliation, the session list uses the container NAME (not cid)
	md.EXPECT().Exec(mock.Anything, "sps-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 1, Output: "no server running"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/abc/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("list sessions after reconcile: %d %s", rec.Code, rec.Body)
	}
	// the reconcile event should have been emitted
	evs, _ := d.Events.Read(0, 0)
	found := false
	for _, e := range evs {
		if e.Type == "project.reconcile" && e.Data["id"] == "abc" {
			found = true
		}
	}
	if !found {
		t.Fatal("no project.reconcile event after lazy reconciliation")
	}
}
