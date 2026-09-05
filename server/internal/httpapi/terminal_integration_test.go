//go:build integration

// Phase 7 gate against a live engine: create a session via the API, drive
// it over the WebSocket bridge (input, resize), disconnect and reconnect to
// prove tmux state survives, then clean up. Run with:
// go test -tags=integration -count=1 ./internal/httpapi/
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTerminalLiveLifecycle(t *testing.T) {
	h, dkr, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// blank project (embedded image ships tmux)
	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects", `{}`)
	if code != http.StatusCreated {
		t.Fatalf("create project: %d %v", code, body)
	}
	id := body["id"].(string)
	deleteProjectAll(t, h, cookie, id)
	waitForStatus(t, h, cookie, id, "running")

	// create → 201 + event; duplicate → 409; list → [main]
	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects/"+id+"/sessions", `{"name":"main"}`)
	if code != http.StatusCreated || body["name"] != "main" {
		t.Fatalf("create session: %d %v", code, body)
	}
	code, _ = doJSON(t, h, cookie, http.MethodPost, "/api/projects/"+id+"/sessions", `{"name":"main"}`)
	if code != http.StatusOK {
		t.Fatalf("duplicate session (ensure semantics): %d, want 200", code)
	}
	code, body = doJSON(t, h, cookie, http.MethodGet, "/api/projects/"+id+"/sessions", "")
	if names, ok := body["sessions"].([]any); code != http.StatusOK || !ok || len(names) != 1 || names[0].(map[string]any)["name"] != "main" {
		t.Fatalf("list sessions: %d %v", code, body)
	}

	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	ws := "ws://" + ts.Listener.Addr().String() + "/ws/projects/" + id + "/sessions/main"

	dial := func() *websocket.Conn {
		t.Helper()
		return dialSessionWS(t, ws, cookie)
	}
	send := func(conn *websocket.Conn, f map[string]any) { wsSend(t, conn, f) }
	readUntilOutputContains := func(conn *websocket.Conn, want string) string {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		var seen strings.Builder
		for time.Now().Before(deadline) {
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read waiting for %q: %v (seen %q)", want, err, seen.String())
			}
			var f wsOut
			if json.Unmarshal(raw, &f) != nil {
				continue
			}
			if f.Type == "exit" {
				t.Fatalf("server ended the terminal: code=%d detail=%q", f.Code, f.Data)
			}
			if f.Type != "output" {
				continue
			}
			seen.WriteString(f.Data)
			if strings.Contains(seen.String(), want) {
				return seen.String()
			}
		}
		t.Fatalf("output never contained %q (seen %q)", want, seen.String())
		return ""
	}

	// connect, resize the TTY, run a command — output comes back framed
	conn := dial()
	send(conn, map[string]any{"type": "resize", "rows": 40, "cols": 100})
	send(conn, map[string]any{"type": "input", "data": "echo hi7\n"})
	readUntilOutputContains(conn, "hi7")
	time.Sleep(300 * time.Millisecond) // let the resize settle
	send(conn, map[string]any{"type": "input", "data": "stty size\n"})
	// tmux's default status bar takes one row: 40 requested → 39 inside
	readUntilOutputContains(conn, "39 100")

	// vanish; the tmux session must keep running server-side
	_ = conn.Close()

	// reconnect → same session; scrollback still holds the earlier line
	conn2 := dial()
	defer conn2.Close()
	send(conn2, map[string]any{"type": "input", "data": "echo back\n"})
	readUntilOutputContains(conn2, "back")
	res, err := dkr.Exec(t.Context(), "pcoder-"+id, []string{"tmux", "capture-pane", "-t", "main", "-p"}, false)
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Output, "hi7") {
		t.Fatalf("scrollback lost across reconnect: %+v err=%v", res, err)
	}

	// cleanup
}
