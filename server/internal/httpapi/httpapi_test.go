package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pcoder/internal/auth"
	"pcoder/internal/events"
	"pcoder/internal/harness"
	"pcoder/internal/obs"
	"pcoder/internal/preview"
	"pcoder/internal/project"
	"pcoder/internal/session"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
	"pcoder/internal/threads"
	dockermocks "pcoder/mocks/docker"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// Tests keep Argon2id fast for the whole package: TestMain lowers the
// params before any hash is minted or verified, restoring after.
func TestMain(m *testing.M) {
	restore := auth.TestParams()
	code := m.Run()
	restore()
	os.Exit(code)
}

// testHash is computed lazily after TestMain lowers the params —
// TestHashFor at package init would bake in production params.
var testHashOnce = sync.OnceValue(func() string { return auth.TestHashFor() })

// testAuthService returns the suite's auth service: the one configured
// email + the shared test hash, generation 1.
func testAuthService() *auth.Service {
	return auth.New("me@example.com", testHashOnce(), 1, []byte(testSecret))
}

// wantFakeHarnessEntry is the seeded "Fake" harness's state-file shape,
// shared by every AssertSection that includes the registry seed.
var wantFakeHarnessEntry = map[string]any{"id": "fake", "name": "Fake", "command": "fakecli", "install": "npm i -g fakecli"}

func newTestDeps(t *testing.T) Deps {
	t.Helper()
	d, _ := newTestDepsInDir(t)
	return d
}

// newTestDepsInDir is newTestDeps, also returning the data dir so tests
// can assert on the files the codemap store writes under it.
func newTestDepsInDir(t *testing.T) (Deps, string) {
	t.Helper()
	dataDir := t.TempDir()
	ev, err := events.Open(filepath.Join(dataDir, "events.log"))
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { ev.Close() })
	ob := installObs(t, dataDir, ev)
	return Deps{Events: ev, Version: "dev", Auth: testAuthService(), Obs: ob,
		Codemaps: threads.New(filepath.Join(dataDir, "codemaps"), false)}, dataDir
}

// installObs configures staged logging over dataDir, mirroring production:
// project lines go to the observe store + stderr only, never events.log
// (ev holds just the explicit global audit Appends). Restores the
// unconfigured state on cleanup. Tests are sequential, so the
// process-global config is safe.
func installObs(t *testing.T, dataDir string, ev EventLog) *obs.Store {
	t.Helper()
	ob := obs.NewStore(dataDir)
	t.Cleanup(ob.Close)
	obs.Configure(ob, nil)
	t.Cleanup(func() { obs.Configure(nil, nil) })
	return ob
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

// loginCookie runs the real flow end to end: POST the email + password
// to /api/auth/login and return the session cookie.
func loginCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := post(t, h, "/api/auth/login", `{"email":"me@example.com","password":"`+auth.TestPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d body=%s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatal("no session cookie in login response")
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
func newProjectDeps(t *testing.T) (Deps, *dockermocks.MockClient, *state.Store) {
	t.Helper()
	d, _ := newTestDepsInDir(t)
	md := dockermocks.NewMockClient(t)
	st, err := state.Open(t.TempDir(), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	// No git identity is seeded: identity lives per repo at commit time,
	// never in state.json.
	d.Projects = project.NewService(project.Open(st), md)
	d.Butler = threads.New(t.TempDir(), true)
	// Same store instance the handlers use, mirroring main.go's wiring:
	// project deletion must cascade into the codemap files.
	d.Projects.SetCodemaps(d.Codemaps)
	d.State = st
	return d, md, st
}

func newSessionDeps(t *testing.T) (Deps, *dockermocks.MockClient, *state.Store) {
	t.Helper()
	d, md, st := newProjectDeps(t)
	d.Sessions = session.New(md)

	// a plugin in the registry, as if added by the user
	hs := harness.New(st)
	if _, err := hs.Save(harness.Harness{Name: "Fake", Command: "fakecli", Install: "npm i -g fakecli"}); err != nil {
		t.Fatal(err)
	}
	d.Harnesses = hs
	d.SSHKeys = sshkeys.New(st)
	return d, md, st
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
type previewTestWorker struct {
	ep preview.Endpoint
}

func (w previewTestWorker) Endpoint() preview.Endpoint  { return w.ep }
func (w previewTestWorker) Close(context.Context) error { return nil }

type previewTestFactory struct {
	ep preview.Endpoint
}

func (f previewTestFactory) Start(context.Context, preview.Config) (preview.Worker, error) {
	return previewTestWorker(f), nil
}

// harnessPath mirrors the prelude session puts before every harness exec.
// Probes must look where CLI installers write; otherwise a present binary
// reads as "missing" and the UI offers Install for an already-installed CLI.
const harnessPath = `export PATH="$HOME/.local/bin:$HOME/bin:$PATH"; `

func TestHealth(t *testing.T) {
	d := newTestDeps(t)
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
