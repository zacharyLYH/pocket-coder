package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pcoder/internal/auth"
	"pcoder/internal/codemapthreads"
	"pcoder/internal/events"
	"pcoder/internal/harness"
	"pcoder/internal/preview"
	"pcoder/internal/project"
	"pcoder/internal/projectlog"
	"pcoder/internal/session"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
	dockermocks "pcoder/mocks/docker"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// wantFakeHarnessEntry is the seeded "Fake" harness's state-file shape,
// shared by every AssertSection that includes the registry seed.
var wantFakeHarnessEntry = map[string]any{"id": "fake", "name": "Fake", "command": "fakecli", "install": "npm i -g fakecli"}

// pinRe extracts the 6-digit PIN from console-mailer output. Hoisted so the
// per-login regexp isn't recompiled dozens of times per suite run.
var pinRe = regexp.MustCompile(`\d{6}`)

func newTestDeps(t *testing.T) (Deps, *bytes.Buffer) {
	t.Helper()
	d, pinOut, _ := newTestDepsInDir(t) //nolint:dogsled
	return d, pinOut
}

// newTestDepsInDir is newTestDeps, also returning the data dir so tests
// can assert on the files the codemap store writes under it.
func newTestDepsInDir(t *testing.T) (Deps, *bytes.Buffer, string) {
	t.Helper()
	dataDir := t.TempDir()
	ev, err := events.Open(filepath.Join(dataDir, "events.log"))
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { ev.Close() })
	var pinOut bytes.Buffer
	svc := auth.New("me@example.com", []byte(testSecret), auth.ConsoleMailer{Out: &pinOut})
	svc.MailerName = "console"
	return Deps{Events: ev, Version: "dev", Auth: svc, ProjectLogs: projectlog.NewManager(0),
		Codemaps: codemapthreads.New(filepath.Join(dataDir, "codemaps"))}, &pinOut, dataDir
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func authedGet(t *testing.T, h http.Handler, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	return rec
}

// loginCookie runs the real flow end to end: request a PIN, read it from the
// console mailer's buffer, verify it, and return the session cookie.
func loginCookie(t *testing.T, h http.Handler, pinOut *bytes.Buffer) *http.Cookie {
	t.Helper()
	rec := post(t, h, "/api/auth/request-pin", `{"email":"me@example.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("request pin: %d body=%s", rec.Code, rec.Body)
	}
	pin := pinRe.FindString(pinOut.String())
	if pin == "" {
		t.Fatalf("no pin in mailer output: %q", pinOut.String())
	}
	rec = post(t, h, "/api/auth/verify", `{"email":"me@example.com","pin":"`+pin+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: %d body=%s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatal("no session cookie in verify response")
	return nil
}

func lastEvent(t *testing.T, d Deps) events.Event {
	t.Helper()
	evs, err := d.Events.Read(0, 0)
	if err != nil || len(evs) == 0 {
		t.Fatalf("no events (err=%v)", err)
	}
	return evs[len(evs)-1]
}

// ── shared fixtures (used across feature test files) ─────────────────

// newProjectDeps wires the real project.Service over a mocked Docker client
// into the handler, so the HTTP layer is tested against the actual pipeline.
// Returns the shared state store so tests can seed it directly.
func newProjectDeps(t *testing.T) (Deps, *dockermocks.MockClient, *bytes.Buffer, *state.Store) {
	t.Helper()
	d, pinOut, _ := newTestDepsInDir(t)
	md := dockermocks.NewMockClient(t)
	st, err := state.Open(t.TempDir(), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	d.Projects = project.NewService(project.Open(st), md, d.Events)
	// Same store instance the handlers use, mirroring main.go's wiring:
	// project deletion must cascade into the codemap files.
	d.Projects.SetCodemaps(d.Codemaps)
	d.State = st
	return d, md, pinOut, st
}

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
	if err := project.Open(st).Create(id, project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
}

func authedPostCtx(t *testing.T, h http.Handler, cookie *http.Cookie, ctx context.Context, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	return rec
}

func authedPost(t *testing.T, h http.Handler, cookie *http.Cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return authedPostCtx(t, h, cookie, context.Background(), path, body)
}

func authedRequest(t *testing.T, h http.Handler, cookie *http.Cookie, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	return rec
}

// previewTestWorker/Factory are a no-op Docker-like runtime for manager-level
// tests: they report a fixed private endpoint and Close immediately.
type previewTestWorker struct{ ep preview.Endpoint }

func (w previewTestWorker) Endpoint() preview.Endpoint  { return w.ep }
func (w previewTestWorker) Close(context.Context) error { return nil }

type previewTestFactory struct{ ep preview.Endpoint }

func (f previewTestFactory) Start(context.Context, preview.Config) (preview.Worker, error) {
	return previewTestWorker{ep: f.ep}, nil
}

func TestHealth(t *testing.T) {
	d, _ := newTestDeps(t)
	h := New(d)
	rec := get(t, h, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field = %q, want %q", body["status"], "ok")
	}
	// the mux rejects wrong methods on known routes (stdlib behavior pin)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
