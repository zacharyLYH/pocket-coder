// Terminal transport: a WebSocket bridging browser keystrokes to
// `tmux attach` inside a project container. Pre-flight failures (unknown
// project, stopped container, missing session) are plain HTTP errors before
// the upgrade.
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

	"pcoder/internal/obs"
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

// ensureProject requires a project's container to exist (recreating it from
// persisted volumes if Docker lost track) and be running. Shared by all
// session/harness routes so they behave identically. Returns the project id
// and whether to continue.
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
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.TerminalAttach, "terminal attach failed", err, map[string]any{"session": name})
			}
		}()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// Pre-flight while we can still answer with real HTTP statuses.
		if !session.ValidName(name) {
			err = errors.New("invalid session name")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			err = errors.New("project container not running")
			return
		}
		container := project.ContainerName(id)
		exists, xerr := d.Sessions.Exists(ctx, container, name)
		if xerr != nil {
			err = xerr
			writeInternalErr(w, "terminal", xerr)
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
								err = lerr
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
				err = errors.New("no such session")
				writeErr(w, http.StatusNotFound, err.Error())
				return
			}
		}

		conn, uerr := upgrader.Upgrade(w, r, nil)
		if uerr != nil {
			err = uerr
			return // upgrade response already written
		}
		defer conn.Close()
		obs.Info(r.Context(), obs.TerminalAttach, "terminal attached", map[string]any{"session": name})
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

	// The pty emits raw bytes that can end mid multi-byte character, but JSON
	// frames must carry valid UTF-8. The writer holds back a trailing
	// incomplete sequence (at most 3 bytes) until the next chunk completes
	// it. carry is only touched under wmu.
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

	// exitFrame sends the terminal-closing frame; shares wmu with pty output
	// because gorilla writes are not concurrent-safe.
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

	// The pipe is synchronous: a write parks until the pty drains it, and
	// once the exec ends nothing reads anymore. The ws reader therefore only
	// enqueues and this pump owns the pipe, draining on stdin death so the
	// reader always reaches its ReadMessage instead of wedging the handler.
	// 64 frames is ample typing headroom.
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

// splitUTF8 returns the length of the longest prefix of b that ends on a
// character boundary, plus the number of trailing bytes forming an
// incomplete sequence to carry into the next chunk. Invalid or orphan
// continuation bytes pass through rather than being held forever.
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
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.SessionList, "list sessions failed", err, nil)
			}
		}()
		id, ok := ensureProject(d, w, r)
		if !ok {
			err = errors.New("project container not running")
			return
		}
		sessions, lerr := d.Sessions.List(r.Context(), project.ContainerName(id))
		if lerr != nil {
			err = lerr
			writeInternalErr(w, "terminal", lerr)
			return
		}
		// Merge state.json sessions that aren't in tmux yet (e.g. after a
		// container rebuild) so the picker shows them and re-entry relaunches them.
		seen := make(map[string]bool, len(sessions))
		for _, s := range sessions {
			seen[s.Name] = true
		}
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
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.SessionCreate, "create session failed", err, nil)
			}
		}()
		// created writes the success response. The obs detail line is
		// emitted before the explicit event Appends at each site (not here),
		// so the Append stays the last events.log entry — tests and readers
		// treat the explicit event as the completion marker.
		created := func(status int, name, harness string) {
			if harness != "" {
				writeJSON(w, status, map[string]any{"name": name, "harness": harness})
			} else {
				writeJSON(w, status, map[string]any{"name": name})
			}
		}
		// observed emits the per-project detail line under the use-case key.
		observed := func(name, harness string) {
			obs.Info(r.Context(), obs.SessionCreate, "session created: "+name,
				map[string]any{"session": name, "harness": harness})
		}
		var body struct {
			Name      string `json:"name"`      // plain-shell session or harness session with explicit name
			HarnessID string `json:"harnessId"` // harness-driven session
			// Create marks explicit intent to make a new session (the New
			// Session dialog). Without it, an unknown name may only be
			// resurrected from recorded metadata — never materialized from a
			// typo or a hand-edited URL.
			Create bool `json:"create"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			err = errors.New("project container not running")
			return
		}

		if body.HarnessID == "" {
			// plain-shell create (the terminal tab's ensure call)
			if !session.ValidName(body.Name) {
				err = errors.New("invalid session name")
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			container := project.ContainerName(id)
			exists, _ := d.Sessions.Exists(r.Context(), container, body.Name)
			if exists {
				// A dead session (command exited, remain-on-exit) is killed and
				// relaunched as the harness it was.
				alive, _ := d.Sessions.IsAlive(r.Context(), container, body.Name)
				if alive {
					observed(body.Name, "")
					created(http.StatusOK, body.Name, "")
					return
				}
				// Dead session — kill it, then relaunch.
				_ = d.Sessions.Kill(r.Context(), container, body.Name)
				sess, hasMeta := d.Projects.GetSession(id, body.Name)
				harnessID := sess.Harness
				if harnessID == "" && hasMeta {
					// Recorded as a plain shell — recreate as shell.
					if cerr := createShellSession(d, w, r.Context(), id, body.Name, false); cerr != nil {
						err = cerr
					}
					return
				}
				if harnessID == "" {
					// No metadata — guess from the name's harness-id prefix.
					harnessID = session.ParseBase(body.Name)
				}
				if h, herr := d.Harnesses.Get(harnessID); herr == nil {
					restarted, lerr := d.Sessions.LaunchNamed(r.Context(), container, body.Name, h)
					if lerr != nil {
						err = lerr
						writeLaunchErr(w, d, id, harnessID, lerr)
						return
					}
					_ = d.Projects.RecordSession(id, restarted, harnessID)
					observed(restarted, harnessID)
					_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": restarted, "harness": harnessID})
					created(http.StatusOK, restarted, harnessID)
					return
				}
				// Harness gone — fall back to plain shell and clear stale metadata.
				_ = d.Projects.RemoveSession(id, body.Name)
				if cerr := createShellSession(d, w, r.Context(), id, body.Name, false); cerr != nil {
					err = cerr
				}
				return
			}
			// Not in tmux. Creating from nothing requires explicit intent,
			// recorded metadata, or the default "main" name; anything else
			// is a typo — 404, not a surprise session.
			sess, hasMeta := d.Projects.GetSession(id, body.Name)
			if !body.Create && !hasMeta && body.Name != "main" {
				err = errors.New("no such session")
				writeErr(w, http.StatusNotFound, err.Error())
				return
			}
			if hasMeta && sess.Harness != "" {
				if h, herr := d.Harnesses.Get(sess.Harness); herr == nil {
					container := project.ContainerName(id)
					restarted, lerr := d.Sessions.LaunchNamed(r.Context(), container, body.Name, h)
					if lerr != nil {
						if errors.Is(lerr, session.ErrDuplicate) {
							// Session exists despite tmux check — attach to it.
							observed(body.Name, sess.Harness)
							created(http.StatusOK, body.Name, sess.Harness)
							return
						}
						err = lerr
						writeLaunchErr(w, d, id, sess.Harness, lerr)
						return
					}
					_ = d.Projects.RecordSession(id, restarted, sess.Harness)
					observed(restarted, sess.Harness)
					_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": restarted, "harness": sess.Harness})
					created(http.StatusOK, restarted, sess.Harness)
					return
				}
				// Harness gone from registry — clear stale metadata.
				_ = d.Projects.RemoveSession(id, body.Name)
			}
			if cerr := createShellSession(d, w, r.Context(), id, body.Name, false); cerr != nil {
				err = cerr
			}
			return
		}

		// harness-driven create
		h, herr := d.Harnesses.Get(body.HarnessID)
		if herr != nil {
			err = herr
			writeErr(w, http.StatusNotFound, herr.Error())
			return
		}
		// When a name is supplied, it is required and unique (409 on collision).
		// When absent, fall back to auto-naming <harnessID>-<n> for backward compatibility.
		if body.Name != "" {
			if !session.ValidName(body.Name) {
				err = errors.New("invalid session name")
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			if exists, _ := d.Sessions.Exists(r.Context(), project.ContainerName(id), body.Name); exists {
				err = errors.New("session name already exists")
				writeErr(w, http.StatusConflict, err.Error())
				return
			}
			name, lerr := d.Sessions.LaunchNamed(r.Context(), project.ContainerName(id), body.Name, h)
			if lerr != nil {
				err = lerr
				if errors.Is(lerr, session.ErrDuplicate) {
					writeErr(w, http.StatusConflict, lerr.Error())
					return
				}
				writeLaunchErr(w, d, id, h.ID, lerr)
				return
			}
			_ = d.Projects.RecordSession(id, name, h.ID)
			observed(name, h.ID)
			_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": name, "harness": h.ID})
			_, _ = d.Events.Append("session.create", map[string]any{"id": id, "name": name, "harness": h.ID})
			created(http.StatusCreated, name, h.ID)
			return
		}
		name, lerr := d.Sessions.Launch(r.Context(), project.ContainerName(id), h)
		if lerr != nil {
			err = lerr
			if errors.Is(lerr, session.ErrDuplicate) {
				writeErr(w, http.StatusConflict, lerr.Error())
				return
			}
			writeLaunchErr(w, d, id, h.ID, lerr)
			return
		}
		_ = d.Projects.RecordSession(id, name, h.ID)
		observed(name, h.ID)
		_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": name, "harness": h.ID})
		_, _ = d.Events.Append("session.create", map[string]any{"id": id, "name": name, "harness": h.ID})
		created(http.StatusCreated, name, h.ID)
	}
}

// createShellSession creates a plain shell session and writes the response:
// 201 for a fresh create, 200 for a restart under the same name. A duplicate
// name is success either way (create doubles as the ensure call). It
// returns the cause so callers can feed their deferred obs error log.
func createShellSession(d Deps, w http.ResponseWriter, ctx context.Context, id, name string, restart bool) error {
	if cerr := d.Sessions.Create(ctx, project.ContainerName(id), name); cerr != nil {
		writeSessionErr(w, d, id, name, cerr)
		// Duplicate is success (create doubles as ensure): the session is
		// already there, so the deferred error log must not fire.
		if errors.Is(cerr, session.ErrDuplicate) {
			return nil
		}
		return cerr
	}
	// Record as a plain shell so restart doesn't fall back to the ParseBase
	// heuristic and relaunch a harness the user never chose.
	_ = d.Projects.RecordSession(id, name, "")
	obs.Info(ctx, obs.SessionCreate, "session created: "+name, map[string]any{"session": name})
	event := map[string]any{"id": id, "name": name}
	status := http.StatusCreated
	if restart {
		event["restart"] = true
		status = http.StatusOK
	}
	_, _ = d.Events.Append("session.create", event)
	writeJSON(w, status, map[string]any{"name": name})
	return nil
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
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.SessionRename, "rename session failed", err, map[string]any{"from": oldName})
			}
		}()
		var body struct {
			Name string `json:"name"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if !session.ValidName(body.Name) {
			err = errors.New("invalid session name")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if !session.ValidName(oldName) {
			err = errors.New("invalid session name")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			err = errors.New("project container not running")
			return
		}
		container := project.ContainerName(id)
		if exists, xerr := d.Sessions.Exists(r.Context(), container, oldName); xerr != nil {
			err = xerr
			writeInternalErr(w, "rename session", xerr)
			return
		} else if !exists {
			err = errors.New("no such session")
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		if exists, xerr := d.Sessions.Exists(r.Context(), container, body.Name); xerr != nil {
			err = xerr
			writeInternalErr(w, "rename session", xerr)
			return
		} else if exists {
			err = errors.New("session name already exists")
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		if rerr := d.Sessions.Rename(r.Context(), container, oldName, body.Name); rerr != nil {
			err = rerr
			if errors.Is(rerr, session.ErrInvalidName) {
				writeErr(w, http.StatusBadRequest, rerr.Error())
				return
			}
			if errors.Is(rerr, session.ErrDuplicate) {
				writeErr(w, http.StatusConflict, rerr.Error())
				return
			}
			if rerr.Error() == fmt.Sprintf("no such session %q", oldName) {
				writeErr(w, http.StatusNotFound, "no such session")
				return
			}
			writeInternalErr(w, "rename session", rerr)
			return
		}
		// Move session metadata under the new name.
		if sess, ok := d.Projects.GetSession(id, oldName); ok {
			_ = d.Projects.RecordSession(id, body.Name, sess.Harness)
			_ = d.Projects.RemoveSession(id, oldName)
		}
		obs.Info(r.Context(), obs.SessionRename, "session renamed to "+body.Name,
			map[string]any{"from": oldName, "to": body.Name})
		_, _ = d.Events.Append("session.rename", map[string]any{"id": id, "from": oldName, "to": body.Name})
		writeJSON(w, http.StatusOK, map[string]any{"name": body.Name})
	}
}

func handleInjectSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("name")
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.SessionInject, "inject session failed", err, map[string]any{"session": name})
			}
		}()
		var body struct {
			Command string `json:"command"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if strings.TrimSpace(body.Command) == "" {
			err = errors.New("command is required")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			err = errors.New("project container not running")
			return
		}
		if ierr := d.Sessions.Inject(r.Context(), project.ContainerName(id), name, body.Command); ierr != nil {
			err = ierr
			if errors.Is(ierr, session.ErrInvalidName) || errors.Is(ierr, session.ErrEmptyCommand) {
				writeErr(w, http.StatusBadRequest, ierr.Error())
				return
			}
			if strings.Contains(ierr.Error(), "no such session") {
				writeErr(w, http.StatusNotFound, "no such session")
				return
			}
			writeInternalErr(w, "inject session", ierr)
			return
		}
		obs.Info(r.Context(), obs.SessionInject, "command injected into "+name, map[string]any{"session": name})
		_, _ = d.Events.Append("session.inject", map[string]any{"id": id, "session": name})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleKillSession removes a tmux session, idempotently. Metadata in
// state.json is kept so re-entry can relaunch the right kind of session.
func handleKillSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("name")
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.SessionKill, "kill session failed", err, map[string]any{"session": name})
			}
		}()
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			err = errors.New("project container not running")
			return
		}
		if kerr := d.Sessions.Kill(r.Context(), project.ContainerName(id), name); kerr != nil {
			err = kerr
			writeInternalErr(w, "kill session", kerr)
			return
		}
		obs.Info(r.Context(), obs.SessionKill, "session killed: "+name, map[string]any{"session": name})
		// Session metadata in state.json persists across kills so re-entry
		// can relaunch it as the right kind of session.
		_, _ = d.Events.Append("session.exit", map[string]any{"id": id, "name": name})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleDeleteSession deletes a session outright: the tmux session is killed
// and its state.json metadata removed, so it disappears from the picker for
// good (unlike kill, which keeps metadata for one-click relaunch). The last
// remaining session cannot be deleted: zero sessions is not a state the UI
// can represent.
func handleDeleteSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("name")
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.SessionDelete, "delete session failed", err, map[string]any{"session": name})
			}
		}()
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			err = errors.New("project container not running")
			return
		}
		// Count sessions exactly like the picker renders them: live tmux
		// sessions merged with state.json ghosts.
		sessions, lerr := d.Sessions.List(r.Context(), project.ContainerName(id))
		if lerr != nil {
			err = lerr
			writeInternalErr(w, "delete session", lerr)
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
			err = errors.New("cannot delete the last session")
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		if kerr := d.Sessions.Kill(r.Context(), project.ContainerName(id), name); kerr != nil {
			err = kerr
			writeInternalErr(w, "delete session", kerr)
			return
		}
		if rerr := d.Projects.RemoveSession(id, name); rerr != nil {
			err = rerr
			writeInternalErr(w, "delete session metadata", rerr)
			return
		}
		obs.Info(r.Context(), obs.SessionDelete, "session deleted: "+name, map[string]any{"session": name})
		_, _ = d.Events.Append("session.delete", map[string]any{"id": id, "name": name})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleRestartSession kills a live session (if any) and relaunches the same
// thing: a harness session by its <id>-<n> prefix, or a plain shell.
func handleRestartSession(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("name")
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.SessionRestart, "restart session failed", err, map[string]any{"session": name})
			}
		}()
		ctx := r.Context()
		var ok bool
		if id, ok = ensureProject(d, w, r); !ok {
			err = errors.New("project container not running")
			return
		}
		container := project.ContainerName(id)

		_ = d.Sessions.Kill(ctx, container, name) // already-gone is fine

		// Resolve kind from state.json metadata; fall back to ParseBase for
		// sessions with no recorded harness.
		sess, hasMeta := d.Projects.GetSession(id, name)
		harnessID := sess.Harness
		if harnessID == "" && hasMeta {
			// Session was recorded as a plain shell — restart as shell.
			if cerr := createShellSession(d, w, ctx, id, name, true); cerr != nil {
				err = cerr
				return
			}
			obs.Info(r.Context(), obs.SessionRestart, "session restarted: "+name,
				map[string]any{"session": name, "mode": "shell"})
			return
		}
		if harnessID == "" {
			harnessID = session.ParseBase(name)
		}

		h, herr := d.Harnesses.Get(harnessID)
		if herr != nil {
			// No matching harness — plain shell restart under the same name.
			if cerr := createShellSession(d, w, ctx, id, name, true); cerr != nil {
				err = cerr
				return
			}
			obs.Info(r.Context(), obs.SessionRestart, "session restarted: "+name,
				map[string]any{"session": name, "mode": "shell-fallback"})
			return
		}

		restarted, lerr := d.Sessions.LaunchNamed(ctx, container, name, h)
		if lerr != nil {
			err = lerr
			writeLaunchErr(w, d, id, harnessID, lerr)
			return
		}
		obs.Info(r.Context(), obs.SessionRestart, "session restarted: "+restarted,
			map[string]any{"session": restarted, "harness": harnessID, "mode": "harness"})
		_ = d.Projects.RecordSession(id, restarted, harnessID)
		_, _ = d.Events.Append("harness.launch", map[string]any{"id": id, "session": restarted, "harness": harnessID, "restart": true})
		writeJSON(w, http.StatusOK, map[string]any{"name": restarted})
	}
}
