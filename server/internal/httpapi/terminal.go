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
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	"pcoder/internal/project"
	"pcoder/internal/session"
	"pcoder/internal/state"
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
		container := project.ContainerName(id)
		exists, err := d.Sessions.Exists(ctx, container, name)
		if err != nil {
			writeInternalErr(w, "terminal", err)
			return
		}
		if !exists {
			// Session not in tmux. If state.json knows this was a harness
			// session, relaunch it so the WebSocket can attach.
			sess, hasMeta := d.Projects.GetSession(id, name)
			if hasMeta {
				if sess.Harness != "" {
					if h, herr := d.Harnesses.Get(sess.Harness); herr == nil {
						if _, lerr := d.Sessions.LaunchNamed(ctx, container, name, h); lerr != nil {
							if !errors.Is(lerr, session.ErrDuplicate) {
								writeLaunchErr(w, d, id, sess.Harness, lerr)
								return
							}
						}
					} else {
						// Harness no longer registered — create a plain shell.
						_ = d.Sessions.Create(ctx, container, name)
					}
				} else {
					// Plain shell recorded in state — recreate it.
					_ = d.Sessions.Create(ctx, container, name)
				}
			} else {
				// No state.json metadata — session truly doesn't exist.
				writeErr(w, http.StatusNotFound, "no such session")
				return
			}
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
		// Merge state.json sessions that aren't in tmux yet (e.g. after
		// container rebuild or tmux crash). These appear in the picker so
		// the user can re-enter and the ensure path relaunches them.
		seen := make(map[string]bool, len(sessions))
		for _, s := range sessions {
			seen[s.Name] = true
		}
		// Read session metadata directly from state.json (no Docker call).
		var proj state.Project
		d.State.View(func(doc *state.Document) { proj = doc.Projects[id] })
		for name := range proj.Sessions {
			if !seen[name] {
				sessions = append(sessions, session.Entry{Name: name})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
	}
}

func handleCreateSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			Name      string `json:"name"`      // plain-shell session or harness session with explicit name
			HarnessID string `json:"harnessId"` // harness-driven session
			// Create marks explicit intent to make a new session (the New
			// Session dialog). Without it, an unknown name may only be
			// resurrected (re-entry after kill, rebuild ghosts) — never
			// materialized from a typo or a hand-edited URL.
			Create bool `json:"create"`
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
			container := project.ContainerName(id)
			exists, _ := d.Sessions.Exists(r.Context(), container, body.Name)
			if exists {
				// Session exists in tmux — but is it alive? A dead session
				// (command exited, remain-on-exit) means the harness crashed
				// or the user exited it. Kill the corpse and relaunch.
				alive, _ := d.Sessions.IsAlive(r.Context(), container, body.Name)
				if alive {
					writeJSON(w, http.StatusOK, map[string]any{"name": body.Name})
					return
				}
				// Dead session — kill it, then relaunch as the harness it was.
				_ = d.Sessions.Kill(r.Context(), container, body.Name)
				sess, hasMeta := d.Projects.GetSession(id, body.Name)
				harnessID := sess.Harness
				if harnessID == "" && hasMeta {
					// Recorded as a plain shell — recreate as shell.
					createShellSession(d, w, r.Context(), id, body.Name, false)
					return
				}
				if harnessID == "" {
					// No metadata — guess from the name's harness-id prefix.
					harnessID = session.ParseBase(body.Name)
				}
				if h, herr := d.Harnesses.Get(harnessID); herr == nil {
					restarted, lerr := d.Sessions.LaunchNamed(r.Context(), container, body.Name, h)
					if lerr != nil {
						writeLaunchErr(w, d, id, harnessID, lerr)
						return
					}
					_ = d.Projects.RecordSession(id, restarted, harnessID)
					_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": restarted, "harness": harnessID})
					writeJSON(w, http.StatusOK, map[string]any{"name": restarted})
					return
				}
				// Harness gone — fall back to plain shell and clear stale metadata.
				_ = d.Projects.RemoveSession(id, body.Name)
				createShellSession(d, w, r.Context(), id, body.Name, false)
				return
			}
			// Not in tmux. Creating from nothing requires intent: an explicit
			// create, the default "main" entry point, or recorded metadata
			// (re-entry after kill, ghosts after a rebuild). Anything else is
			// a typo or a hand-edited URL — 404, not a surprise session.
			sess, hasMeta := d.Projects.GetSession(id, body.Name)
			if !body.Create && !hasMeta && body.Name != "main" {
				writeErr(w, http.StatusNotFound, "no such session")
				return
			}
			if hasMeta && sess.Harness != "" {
				if h, herr := d.Harnesses.Get(sess.Harness); herr == nil {
					container := project.ContainerName(id)
					restarted, lerr := d.Sessions.LaunchNamed(r.Context(), container, body.Name, h)
					if lerr != nil {
						if errors.Is(lerr, session.ErrDuplicate) {
							// Session exists despite tmux check — attach to it.
							writeJSON(w, http.StatusOK, map[string]any{"name": body.Name})
							return
						}
						writeLaunchErr(w, d, id, sess.Harness, lerr)
						return
					}
					_ = d.Projects.RecordSession(id, restarted, sess.Harness)
					_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": restarted, "harness": sess.Harness})
					writeJSON(w, http.StatusOK, map[string]any{"name": restarted})
					return
				}
				// Harness gone from registry — clear stale metadata.
				_ = d.Projects.RemoveSession(id, body.Name)
			}
			createShellSession(d, w, r.Context(), id, body.Name, false)
			return
		}

		// harness-driven create
		h, err := d.Harnesses.Get(body.HarnessID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		// When a name is supplied, it is required and unique (409 on collision).
		// When absent, fall back to auto-naming <harnessID>-<n> for backward compatibility.
		if body.Name != "" {
			if !session.ValidName(body.Name) {
				writeErr(w, http.StatusBadRequest, "invalid session name")
				return
			}
			if exists, _ := d.Sessions.Exists(r.Context(), project.ContainerName(id), body.Name); exists {
				writeErr(w, http.StatusConflict, "session name already exists")
				return
			}
			name, lerr := d.Sessions.LaunchNamed(r.Context(), project.ContainerName(id), body.Name, h)
			if lerr != nil {
				if errors.Is(lerr, session.ErrDuplicate) {
					writeErr(w, http.StatusConflict, lerr.Error())
					return
				}
				writeLaunchErr(w, d, id, h.ID, lerr)
				return
			}
			_ = d.Projects.RecordSession(id, name, h.ID)
			_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": name, "harness": h.ID})
			_, _ = d.Events.Append("session.create", map[string]any{"id": id, "name": name, "harness": h.ID})
			writeJSON(w, http.StatusCreated, map[string]any{"name": name, "harness": h.ID})
			return
		}
		name, err := d.Sessions.Launch(r.Context(), project.ContainerName(id), h)
		if err != nil {
			if errors.Is(err, session.ErrDuplicate) {
				writeErr(w, http.StatusConflict, err.Error())
				return
			}
			writeLaunchErr(w, d, id, h.ID, err)
			return
		}
		_ = d.Projects.RecordSession(id, name, h.ID)
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
	// Record as a plain shell in state.json so restart/re-entry don't
	// fall back to the ParseBase heuristic and accidentally relaunch a
	// harness the user never chose.
	_ = d.Projects.RecordSession(id, name, "")
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

func handleRenameSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, oldName := r.PathValue("id"), r.PathValue("name")
		var body struct {
			Name string `json:"name"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if !session.ValidName(body.Name) {
			writeErr(w, http.StatusBadRequest, "invalid session name")
			return
		}
		if !session.ValidName(oldName) {
			writeErr(w, http.StatusBadRequest, "invalid session name")
			return
		}
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			return
		}
		container := project.ContainerName(id)
		if exists, err := d.Sessions.Exists(r.Context(), container, oldName); err != nil {
			writeInternalErr(w, "rename session", err)
			return
		} else if !exists {
			writeErr(w, http.StatusNotFound, "no such session")
			return
		}
		if exists, err := d.Sessions.Exists(r.Context(), container, body.Name); err != nil {
			writeInternalErr(w, "rename session", err)
			return
		} else if exists {
			writeErr(w, http.StatusConflict, "session name already exists")
			return
		}
		if err := d.Sessions.Rename(r.Context(), container, oldName, body.Name); err != nil {
			if errors.Is(err, session.ErrInvalidName) {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			if errors.Is(err, session.ErrDuplicate) {
				writeErr(w, http.StatusConflict, err.Error())
				return
			}
			if err.Error() == fmt.Sprintf("no such session %q", oldName) {
				writeErr(w, http.StatusNotFound, "no such session")
				return
			}
			writeInternalErr(w, "rename session", err)
			return
		}
		// Move session metadata under the new name.
		if sess, ok := d.Projects.GetSession(id, oldName); ok {
			_ = d.Projects.RecordSession(id, body.Name, sess.Harness)
			_ = d.Projects.RemoveSession(id, oldName)
		}
		_, _ = d.Events.Append("session.rename", map[string]any{"id": id, "from": oldName, "to": body.Name})
		writeJSON(w, http.StatusOK, map[string]any{"name": body.Name})
	}
}

func handleInjectSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("name")
		var body struct {
			Command string `json:"command"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if strings.TrimSpace(body.Command) == "" {
			writeErr(w, http.StatusBadRequest, "command is required")
			return
		}
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			return
		}
		if err := d.Sessions.Inject(r.Context(), project.ContainerName(id), name, body.Command); err != nil {
			if errors.Is(err, session.ErrInvalidName) || errors.Is(err, session.ErrEmptyCommand) {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			if strings.Contains(err.Error(), "no such session") {
				writeErr(w, http.StatusNotFound, "no such session")
				return
			}
			writeInternalErr(w, "inject session", err)
			return
		}
		_, _ = d.Events.Append("session.inject", map[string]any{"id": id, "session": name})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
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
		// Session metadata in state.json persists across kills so re-entry
		// can detect this was a harness session and relaunch it.
		_, _ = d.Events.Append("session.exit", map[string]any{"id": id, "name": name})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleDeleteSession deletes a session outright: the tmux session is killed
// AND its state.json metadata is removed, so it disappears from the picker
// for good (unlike kill, which keeps metadata to enable one-click relaunch).
// The last remaining session cannot be deleted: a terminal with zero
// sessions is not a state the UI can represent, and silently respawning a
// session the user asked to delete would be worse. Kill (metadata kept,
// one-click relaunch) remains the escape hatch for a lone bad session.
// Killing an already-gone session is idempotent; so is removing metadata.
func handleDeleteSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("name")
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			return
		}
		// Count sessions exactly like the picker renders them: live tmux
		// sessions merged with state.json entries (ghosts after a rebuild).
		sessions, err := d.Sessions.List(r.Context(), project.ContainerName(id))
		if err != nil {
			writeInternalErr(w, "delete session", err)
			return
		}
		known := make(map[string]bool, len(sessions)+1)
		for _, s := range sessions {
			known[s.Name] = true
		}
		var proj state.Project
		d.State.View(func(doc *state.Document) { proj = doc.Projects[id] })
		for sName := range proj.Sessions {
			known[sName] = true
		}
		if len(known) <= 1 {
			writeErr(w, http.StatusConflict, "cannot delete the last session")
			return
		}
		if err := d.Sessions.Kill(r.Context(), project.ContainerName(id), name); err != nil {
			writeInternalErr(w, "delete session", err)
			return
		}
		if err := d.Projects.RemoveSession(id, name); err != nil {
			writeInternalErr(w, "delete session metadata", err)
			return
		}
		_, _ = d.Events.Append("session.delete", map[string]any{"id": id, "name": name})
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

		// Resolve kind from state.json: the session's metadata records which
		// harness (if any) it was launched with. Falls back to ParseBase for
		// sessions created before state tracking was added.
		sess, hasMeta := d.Projects.GetSession(id, name)
		harnessID := sess.Harness
		if harnessID == "" && hasMeta {
			// Session was recorded as a plain shell — restart as shell.
			createShellSession(d, w, ctx, id, name, true)
			return
		}
		if harnessID == "" {
			// No metadata — guess from the name's harness-id prefix.
			harnessID = session.ParseBase(name)
		}

		h, herr := d.Harnesses.Get(harnessID)
		if herr != nil {
			// No matching harness — plain shell restart under the same name.
			createShellSession(d, w, ctx, id, name, true)
			return
		}

		restarted, err := d.Sessions.LaunchNamed(ctx, container, name, h)
		if err != nil {
			writeLaunchErr(w, d, id, harnessID, err)
			return
		}
		// Re-record the session with the same harness.
		_ = d.Projects.RecordSession(id, restarted, harnessID)
		_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": restarted, "harness": harnessID, "restart": true})
		writeJSON(w, http.StatusOK, map[string]any{"name": restarted})
	}
}
