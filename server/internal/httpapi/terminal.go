// Terminal transport (Phase 7): a WebSocket bridging browser keystrokes to
// `tmux attach` inside a project container. Strict pre-flight — unknown
// project, stopped/missing container, or missing session are plain HTTP
// errors before the upgrade; after the upgrade everything speaks frames.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"sps/internal/project"
	"sps/internal/session"
)

// wsIn is a browser→server frame. input carries raw keystrokes; resize
// applies the terminal dimensions to the exec TTY.
type wsIn struct {
	Type string `json:"type"` // "input" | "resize"
	Data string `json:"data,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}

// wsOut is a server→browser frame. output is raw pty bytes; exit ends the
// stream with the attach's exit code.
type wsOut struct {
	Type string `json:"type"` // "output" | "exit"
	Data string `json:"data,omitempty"`
	Code int    `json:"code,omitempty"`
}

// Same-origin need not be re-checked here: the session cookie is
// SameSite=Strict, so a cross-site handshake cannot carry it, and RequireAuth
// already rejected unauthenticated upgrades.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// ensureProject reconciles a project's container — recreating it from the
// persisted volumes if Docker lost track of it — and requires it to be
// running. Every session/harness route goes through this so they share one
// behavior and one container-status round trip. Returns the project id and
// whether to continue.
func ensureProject(d Deps, w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	st, err := d.Projects.EnsureContainer(r.Context(), id)
	if err != nil {
		writeServiceErr(w, err)
		return "", false
	}
	if st.State != project.StateRunning {
		writeErr(w, http.StatusConflict, "container not running")
		return "", false
	}
	return id, true
}

// handleTerminal upgrades and bridges. Layout: one goroutine runs the
// blocking tmux attach; one reads browser frames (input → pty stdin, resize
// → ResizeTTY); the main goroutine waits for either side to end and closes
// the other down.
func handleTerminal(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("name")
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// Pre-flight while we can still answer with real HTTP statuses.
		if !session.ValidName(name) {
			writeErr(w, http.StatusBadRequest, "invalid session name")
			return
		}
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			return
		}
		exists, err := d.Sessions.Exists(ctx, project.ContainerName(id), name)
		if err != nil {
			writeInternalErr(w, "terminal", err)
			return
		}
		if !exists {
			writeErr(w, http.StatusNotFound, "no such session")
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return // upgrade response already written
		}
		defer conn.Close()
		_, _ = d.Events.Append("terminal.attach", map[string]any{"id": id, "session": name})
		defer func() { _, _ = d.Events.Append("terminal.detach", map[string]any{"id": id, "session": name}) }()

		runTerminal(ctx, cancel, d.Sessions, conn, project.ContainerName(id), name)
	}
}

// runTerminal pumps frames between the websocket and the attach exec until
// either end closes. The exec runs against ctx: when the browser goes away,
// cancel drops the docker connection but leaves the tmux session alive in
// the container — reconnect attaches to the same scrollback.
func runTerminal(ctx context.Context, cancel context.CancelFunc, svc *session.Service, conn *websocket.Conn, container, name string) {
	defer cancel()

	inR, inW := io.Pipe()
	defer inW.Close() // signals the attach on ws-side shutdown

	var wmu sync.Mutex // gorilla writes are not concurrent-safe

	// The pty hands us raw bytes in arbitrary chunks, and a chunk can end in
	// the middle of a multi-byte character. JSON text frames must carry valid
	// UTF-8: marshaling a split character would silently replace it with
	// U+FFFD and the browser would paint mojibake. So the writer holds back
	// a trailing incomplete sequence (at most 3 bytes) until the next chunk
	// completes it. carry is only touched under wmu.
	var carry []byte
	out := writerFunc(func(p []byte) (int, error) {
		wmu.Lock()
		defer wmu.Unlock()
		buf := p
		if len(carry) > 0 {
			buf = append(carry, p...)
		}
		n, _ := splitUTF8(buf)
		carry = append(carry[:0], buf[n:]...) // hold the tail for next time
		if n == 0 {
			return len(p), nil // nothing frameable yet; more bytes will come
		}
		frame, err := json.Marshal(wsOut{Type: "output", Data: string(buf[:n])})
		if err != nil {
			return 0, err
		}
		if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
			return 0, err
		}
		return len(p), nil
	})

	// exitFrame sends the terminal-closing frame; conn writes share wmu with
	// pty output (gorilla writes are not concurrent-safe).
	exitFrame := func(code int, detail string) {
		wmu.Lock()
		defer wmu.Unlock()
		frame, _ := json.Marshal(wsOut{Type: "exit", Code: code, Data: detail})
		_ = conn.WriteMessage(websocket.TextMessage, frame)
	}

	execID, attachDone, err := svc.Attach(ctx, container, name, inR, out, out)
	if err != nil {
		exitFrame(-1, err.Error())
		return
	}

	// inputs decouples websocket framing from attach-stdin writes. The pipe
	// is synchronous: a write parks until the pty drains it, and once the
	// exec stream ends nothing on the Docker side reads anymore. If the one
	// goroutine that could notice the browser leaving were ever parked in
	// that write, the handler would wedge forever (leaked goroutine, hijacked
	// connection, FD per abused tab). So the reader only enqueues, and this
	// pump owns the pipe; when the write side dies it drains the queue so the
	// reader can always reach its ReadMessage again. Dropping stale keystrokes
	// beats wedging the bridge; 64 frames is ample typing headroom.
	inputs := make(chan string, 64)
	go func() {
		for data := range inputs {
			if _, err := io.WriteString(inW, data); err != nil {
				for range inputs { // stdin is dead; keep draining so sends never block
				}
			}
		}
	}()

	resize := func(rows, cols int) {
		if rows > 0 && cols > 0 {
			_ = svc.Resize(ctx, execID, rows, cols)
		}
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return // browser went away or sent garbage at the transport level
			}
			var f wsIn
			if err := json.Unmarshal(raw, &f); err != nil {
				continue // tolerate junk frames; never kill the terminal for one bad line
			}
			switch f.Type {
			case "input":
				select {
				case inputs <- f.Data:
				default:
					// queue full: the pty stopped keeping up; shed input
				}
			case "resize":
				resize(f.Rows, f.Cols)
			}
		}
	}()

	select {
	case out_ := <-attachDone:
		if out_.Err != nil {
			exitFrame(-1, out_.Err.Error())
		} else {
			exitFrame(out_.ExitCode, "")
		}
		// The session ended server-side; close the socket so the reader
		// goroutine unblocks and this handler can return.
		_ = conn.Close()
	case <-closed:
		// browser disconnected; cancel drops the attach, session survives
	}
	<-closed      // let the reader drain before closing the pipe via defer
	close(inputs) // no more producers: let the pump finish and exit
}

// writerFunc adapts a function to io.Writer so pty output can be marshalled
// into frames as it streams.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// splitUTF8 finds where frameable output ends: it returns the length of the
// longest prefix of b that ends on a character boundary, plus the number of
// trailing bytes forming an incomplete sequence to carry into the next
// chunk. A start byte promises 2-4 total bytes; if the buffer ends before
// the promise is fulfilled, those bytes wait. Orphan continuation or invalid
// bytes pass through rather than being held forever.
func splitUTF8(b []byte) (n, hold int) {
	for i := 1; i <= 3 && i <= len(b); i++ {
		c := b[len(b)-i]
		switch {
		case c < 0x80:
			return len(b), 0 // ASCII: nothing pending
		case c >= 0xC2: // a start byte; C0/C1 never appear in valid UTF-8
			size := 2
			if c >= 0xF0 {
				if c > 0xF4 { // F5-FF are not valid starts: flush, don't stall
					return len(b), 0
				}
				size = 4
			} else if c >= 0xE0 {
				size = 3
			}
			if i < size {
				return len(b) - i, i // sequence cut short: hold what we have
			}
			return len(b), 0 // fully present after all
		}
		// continuation byte: keep looking backward
	}
	return len(b), 0 // 3 continuation bytes with no start: pass it through
}

func handleListSessions(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := ensureProject(d, w, r)
		if !ok {
			return
		}
		sessions, err := d.Sessions.List(r.Context(), project.ContainerName(id))
		if err != nil {
			writeInternalErr(w, "terminal", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
	}
}

// harnessNameConflict reports whether name falls inside a harness's
// namespace: the bare id ("opencode") or a numbered session ("opencode-2").
func harnessNameConflict(d Deps, name string) (string, bool) {
	hs, err := d.Harnesses.List()
	if err != nil {
		return "", false // don't block creates on a harness-list failure
	}
	for _, h := range hs {
		if name == h.ID || session.HarnessSuffixed(name, h.ID) {
			return h.ID, true
		}
	}
	return "", false
}

func handleCreateSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			Name      string `json:"name"`      // plain-shell session (Phase 7 terminal flow)
			HarnessID string `json:"harnessId"` // harness-driven session (<id>-<n>)
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			return
		}

		if body.HarnessID == "" {
			// plain-shell create (the terminal tab's ensure call)
			if !session.ValidName(body.Name) {
				writeErr(w, http.StatusBadRequest, "invalid session name")
				return
			}
			// Ensure semantics first: attaching to a session that already
			// exists (e.g. a live harness session "opencode-1") is success,
			// no matter what it is named. The reservation below only stops
			// NEW shells from squatting a harness namespace.
			if exists, _ := d.Sessions.Exists(r.Context(), project.ContainerName(id), body.Name); exists {
				writeJSON(w, http.StatusOK, map[string]any{"name": body.Name})
				return
			}
			// A shell squatting a harness's namespace ("opencode",
			// "opencode-1") shadows the real harness sessions in the picker
			// and confuses restart — reserve it.
			if hid, taken := harnessNameConflict(d, body.Name); taken {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("%q is reserved for the %q harness sessions; pick another name", body.Name, hid),
				})
				return
			}
			createShellSession(d, w, r.Context(), id, body.Name, false)
			return
		}

		// harness-driven create: install-on-demand + CLI validation + launch
		h, err := d.Harnesses.Get(body.HarnessID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		name, err := d.Sessions.Launch(r.Context(), project.ContainerName(id), h)
		if err != nil {
			writeLaunchErr(w, d, id, h.ID, err)
			return
		}
		_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": name, "harness": h.ID})
		_, _ = d.Events.Append("session.create", map[string]any{"id": id, "name": name, "harness": h.ID})
		writeJSON(w, http.StatusCreated, map[string]any{"name": name, "harness": h.ID})
	}
}

// createShellSession creates a plain shell session and writes the response:
// 201 for a fresh create, 200 for a restart under the same name. A duplicate
// name is success either way (create doubles as the ensure call). Name
// validity is enforced by session.Create; writeSessionErr maps the rejection.
func createShellSession(d Deps, w http.ResponseWriter, ctx context.Context, id, name string, restart bool) {
	if err := d.Sessions.Create(ctx, project.ContainerName(id), name); err != nil {
		writeSessionErr(w, d, id, name, err)
		return
	}
	event := map[string]any{"id": id, "name": name}
	status := http.StatusCreated
	if restart {
		event["restart"] = true
		status = http.StatusOK
	}
	_, _ = d.Events.Append("session.create", event)
	writeJSON(w, status, map[string]any{"name": name})
}

// writeSessionErr maps shell-create errors; duplicates are success because
// create doubles as the terminal flow's ensure call.
func writeSessionErr(w http.ResponseWriter, d Deps, id, name string, err error) {
	if errors.Is(err, session.ErrInvalidName) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, session.ErrDuplicate) {
		_, _ = d.Events.Append("session.create", map[string]any{"id": id, "name": name, "existing": true})
		writeJSON(w, http.StatusOK, map[string]any{"name": name})
		return
	}
	writeInternalErr(w, "terminal", err)
}

// writeLaunchErr maps harness-launch failures: a rejected non-CLI is a 422
// with the PRD message and a validation.failed event; anything else is a
// plain error.
func writeLaunchErr(w http.ResponseWriter, d Deps, id, harnessID string, err error) {
	if errors.Is(err, session.ErrNotCLI) || errors.Is(err, session.ErrInvalidName) || errors.Is(err, session.ErrNotInstalled) {
		_, _ = d.Events.Append("validation.failed", map[string]any{"id": id, "harness": harnessID, "detail": err.Error()})
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeInternalErr(w, "launch session", err)
}

// handleKillSession removes a tmux session. Killing an already-gone session
// is idempotent success.
func handleKillSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("name")
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			return
		}
		if err := d.Sessions.Kill(r.Context(), project.ContainerName(id), name); err != nil {
			writeInternalErr(w, "kill session", err)
			return
		}
		_, _ = d.Events.Append("session.exit", map[string]any{"id": id, "name": name})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleRestartSession kills a live session (if any) and relaunches the same
// thing: a harness session by its <id>-<n> prefix, or a plain shell.
func handleRestartSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("name")
		ctx := r.Context()
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			return
		}
		container := project.ContainerName(id)

		_ = d.Sessions.Kill(ctx, container, name) // already-gone is fine

		// resolve kind from the name: <harnessID>-<n> where harnessID is an
		// existing plugin; otherwise it is (or becomes) a plain shell. The
		// numeric suffix is required — a bare shell named like a harness id
		// ("opencode") must restart as the shell it is.
		base := session.ParseBase(name)
		h, herr := d.Harnesses.Get(base)

		if herr != nil || !session.HarnessSuffixed(name, base) {
			// plain shell restart under the same name
			createShellSession(d, w, ctx, id, name, true)
			return
		}

		restarted, err := d.Sessions.LaunchNamed(ctx, container, name, h)
		if err != nil {
			writeLaunchErr(w, d, id, base, err)
			return
		}
		_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": restarted, "harness": base, "restart": true})
		writeJSON(w, http.StatusOK, map[string]any{"name": restarted})
	}
}
