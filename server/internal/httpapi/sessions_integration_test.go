//go:build integration

// Phase 8 gate against a live engine: a fake CLI harness (its install
// writes the executable) launches in a real project, gets listed, killed,
// and restarted; a not-a-CLI fixture is rejected with the exact PRD message.
// Run with: go test -tags=integration -count=1 ./internal/httpapi/
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"pcoder/internal/docker"
	"pcoder/internal/harness"
	"pcoder/internal/state"
)

// writePlugin adds a plugin to the live test's registry — the same path the
// POST /api/harnesses endpoint takes.
func writePlugin(t *testing.T, st *state.Store, name, command, install string) {
	t.Helper()
	if _, err := harness.New(st).Save(harness.Harness{Name: name, Command: command, Install: install}); err != nil {
		t.Fatal(err)
	}
}

// Passes --version like a real CLI, then "crashes" with exit 9 two seconds
// into a bare run — so the || echo failure story becomes visible.
const fakeCLIInstall = "printf '#!/bin/sh\\nif [ $# -gt 0 ]; then echo \"fakecli v1\"; exit 0; fi\\necho \"fake-cli running\"\\nsleep 2\\nexit 9\\n' > /usr/local/bin/fakecli && chmod +x /usr/local/bin/fakecli"
const sleepyInstall = "printf '#!/bin/sh\\nexit 0\\n' > /usr/local/bin/sleepy && chmod +x /usr/local/bin/sleepy"

// TestHarnessConfigLandsInProject is the config-v2 integration check: a
// plugin with configPath+config launches, and the CLI's native config file
// exists inside the project at exactly the path the tool reads, byte for
// byte — written before the launch command ever runs.
func TestHarnessConfigLandsInContainer(t *testing.T) {
	h, dkr, _, pinOut, _, st := newLiveDeps(t)

	if _, err := harness.New(st).Save(harness.Harness{
		Name: "Cfg CLI", Command: "fakecli", Install: fakeCLIInstall,
		ConfigPath: "/root/.fakecli/config.json",
		Config:     json.RawMessage(`{"model":"test-model","apiKey":"k-123"}`),
	}); err != nil {
		t.Fatal(err)
	}
	cookie := login(t, h, pinOut)

	id, _ := createTestProject(t, h, cookie, "", "", "")
	waitForStatus(t, h, cookie, id, "running")

	// installs are explicit: inject the CLI before launching
	code, body := doJSON(t, h, cookie, http.MethodPost,
		"/api/harnesses/cfg-cli/install", fmt.Sprintf(`{"projectIds":[%q]}`, id))
	if code != http.StatusOK {
		t.Fatalf("install: %d %v", code, body)
	}

	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects/"+id+"/sessions",
		`{"harnessId":"cfg-cli"}`)
	if code != http.StatusCreated {
		t.Fatalf("launch cfg-cli: %d %v", code, body)
	}

	ctx := context.Background()
	var cat docker.ExecResult
	for i := 0; i < 10; i++ {
		res, err := dkr.Exec(ctx, "pcoder-"+id,
			[]string{"cat", "/root/.fakecli/config.json"}, false)
		if err == nil && res.ExitCode == 0 {
			cat = res
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	want := `{"model":"test-model","apiKey":"k-123"}`
	if strings.TrimSpace(cat.Output) != want {
		t.Fatalf("config file in project = %q, want %q", cat.Output, want)
	}
}

func TestHarnessSessionLifecycle(t *testing.T) {
	h, dkr, _, pinOut, ev, st := newLiveDeps(t)

	writePlugin(t, st, "Fake CLI", "fakecli", fakeCLIInstall)
	writePlugin(t, st, "Sleepy", "sleepy", sleepyInstall)

	cookie := login(t, h, pinOut)

	id, _ := createTestProject(t, h, cookie, "", "", "")
	waitForStatus(t, h, cookie, id, "running")

	ctx := context.Background()

	// --- explicit install, then launch: installs never happen implicitly
	// at launch (the 422 story) — the home page injects them first ---
	code, body := doJSON(t, h, cookie, http.MethodPost,
		"/api/harnesses/fake-cli/install", fmt.Sprintf(`{"projectIds":[%q]}`, id))
	if code != http.StatusOK {
		t.Fatalf("install: %d %v", code, body)
	}
	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects/"+id+"/sessions",
		`{"harnessId":"fake-cli"}`)
	if code != http.StatusCreated || body["name"] != "fake-cli-1" {
		t.Fatalf("launch: %d %v", code, body)
	}
	if res, err := dkr.Exec(ctx, "pcoder-"+id, []string{"tmux", "has-session", "-t", "fake-cli-1"}, false); err != nil || res.ExitCode != 0 {
		t.Fatalf("session missing after launch: %+v err=%v", res, err)
	}
	if res, err := dkr.Exec(ctx, "pcoder-"+id, []string{"bash", "-lc", "command -v fakecli"}, false); err != nil || res.ExitCode != 0 {
		t.Fatalf("install did not put fakecli on PATH: %+v err=%v", res, err)
	}
	// launching an uninstalled harness is the 422 PRD rejection
	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects/"+id+"/sessions",
		`{"harnessId":"sleepy"}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("uninstalled launch: %d %v, want 422", code, body)
	}

	// listed among sessions
	code, body = doJSON(t, h, cookie, http.MethodGet, "/api/projects/"+id+"/sessions", "")
	if names, ok := body["sessions"].([]any); code != http.StatusOK || !ok || len(names) == 0 {
		t.Fatalf("list sessions: %d %v", code, body)
	}

	// --- kill → gone from tmux ---
	code, _ = doJSON(t, h, cookie, http.MethodDelete, "/api/projects/"+id+"/sessions/fake-cli-1", "")
	if code != http.StatusOK {
		t.Fatalf("kill: %d", code)
	}
	if res, err := dkr.Exec(ctx, "pcoder-"+id, []string{"tmux", "has-session", "-t", "fake-cli-1"}, false); err == nil && res.ExitCode == 0 {
		t.Fatal("killed session still exists in tmux")
	}

	// --- restart → same name back through the full pipeline ---
	code, body = doJSON(t, h, cookie, http.MethodPost,
		"/api/projects/"+id+"/sessions/fake-cli-1/restart", "")
	if code != http.StatusOK || body["name"] != "fake-cli-1" {
		t.Fatalf("restart: %d %v", code, body)
	}
	if res, err := dkr.Exec(ctx, "pcoder-"+id, []string{"tmux", "has-session", "-t", "fake-cli-1"}, false); err != nil || res.ExitCode != 0 {
		t.Fatalf("restarted session missing: %+v err=%v", res, err)
	}

	// --- the || echo failure story: fakecli crashes with exit 9 two seconds
	// in; remain-on-exit keeps the pane showing "[fakecli exited: 9]" ---
	var exitLine string
	for i := 0; i < 24; i++ {
		time.Sleep(250 * time.Millisecond)
		capres, cerr := dkr.Exec(ctx, "pcoder-"+id,
			[]string{"tmux", "capture-pane", "-t", "fake-cli-1", "-p"}, false)
		if cerr == nil && capres.ExitCode == 0 && strings.Contains(capres.Output, "exited:") {
			exitLine = capres.Output
			break
		}
	}
	if !strings.Contains(exitLine, "[fakecli exited: 9]") {
		t.Fatalf("the || echo failure story never appeared (captured %q)", exitLine)
	}

	// --- not-a-CLI rejection with the exact PRD message ---
	// (sleepy must be installed first: validation only runs post-install)
	code, body = doJSON(t, h, cookie, http.MethodPost,
		"/api/harnesses/sleepy/install", fmt.Sprintf(`{"projectIds":[%q]}`, id))
	if code != http.StatusOK {
		t.Fatalf("install sleepy: %d %v", code, body)
	}
	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects/"+id+"/sessions",
		`{"harnessId":"sleepy"}`)
	errMsg, _ := body["error"].(string)
	if code != http.StatusUnprocessableEntity ||
		!strings.HasPrefix(errMsg, "not a CLI — it looks like it wants a display (an IDE/GUI), and the platform only runs terminal programs.") {
		t.Fatalf("not-a-CLI: got %d %q, want 422 with the PRD message", code, errMsg)
	}
	evs, err := ev.Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Type == "validation.failed" && e.Data["harness"] == "sleepy" {
			found = true
		}
	}
	if !found {
		t.Fatal("no validation.failed event for sleepy")
	}

	// --- cleanup ---
}

// TestRealTUIGate is the faithful transport check: a genuine full-screen TUI
// (busybox vi, already in the project image) launched as a harness, driven
// over the WebSocket bridge with real keystrokes. Asserts that what the
// browser sends actually edits the file the TUI holds — the same interaction
// pattern as coding CLIs, no downloads needed.
func TestRealTUIGate(t *testing.T) {
	h, dkr, _, pinOut, ev, st := newLiveDeps(t)

	// vi opens /workspace/hello.txt; write-quit makes the session exit,
	// which lets us assert keystroke fidelity by reading the file back
	writePlugin(t, st, "Vi", "vi hello.txt", "")
	cookie := login(t, h, pinOut)

	id, _ := createTestProject(t, h, cookie, "", "", "")
	waitForStatus(t, h, cookie, id, "running")
	ctx := context.Background()

	// launch the vi harness session
	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects/"+id+"/sessions",
		`{"harnessId":"vi"}`)
	if code != http.StatusCreated {
		t.Fatalf("launch vi: %d %v", code, body)
	}
	sessionName := body["name"].(string)

	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	dial := func(name string) *websocket.Conn {
		t.Helper()
		return dialSessionWS(t, "ws://"+ts.Listener.Addr().String()+"/ws/projects/"+id+"/sessions/"+name, cookie)
	}

	conn := dial(sessionName)
	defer conn.Close()
	sendFrame := func(f map[string]any) { wsSend(t, conn, f) }
	readUntilOutputContains := func(want string) string {
		t.Helper()
		var seen strings.Builder
		for i := 0; i < 200; i++ {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read waiting for %q (seen %q): %v", want, seen.String(), err)
			}
			var f wsOut
			if json.Unmarshal(raw, &f) != nil || f.Type != "output" {
				continue
			}
			seen.WriteString(f.Data)
			if strings.Contains(seen.String(), want) {
				return seen.String()
			}
		}
		t.Fatalf("never saw %q (seen %q)", want, seen.String())
		return ""
	}

	// vi opens on the empty file; insert a line and write-quit
	sendFrame(map[string]any{"type": "resize", "rows": 30, "cols": 100})
	readUntilOutputContains("~") // vi's empty-line marker proves it rendered
	for _, k := range []string{"i", "hello from the tui gate", "\x1b", "ZZ"} {
		sendFrame(map[string]any{"type": "input", "data": k})
		time.Sleep(150 * time.Millisecond)
	}

	// THE assertion: browser keystrokes really edited the file on disk.
	// (ZZ succeeds here, so the || echo failure story correctly stays
	// silent — that path is covered by the fakecli crash above.)
	var cat docker.ExecResult
	for i := 0; i < 15; i++ {
		res, err := dkr.Exec(ctx, "pcoder-"+id, []string{"cat", "/workspace/hello.txt"}, false)
		if err == nil && res.ExitCode == 0 && strings.Contains(res.Output, "hello from the tui gate") {
			cat = res
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !strings.Contains(cat.Output, "hello from the tui gate") {
		t.Fatalf("keystrokes did not reach vi (file=%q)", cat.Output)
	}

	// the terminal.attach event for this session landed in the audit log
	evs, err := ev.Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	sawAttach := false
	for _, e := range evs {
		if e.Type == "terminal.attach" && e.Data["session"] == sessionName {
			sawAttach = true
		}
	}
	if !sawAttach {
		t.Fatal("no terminal.attach event for the vi session")
	}
}
