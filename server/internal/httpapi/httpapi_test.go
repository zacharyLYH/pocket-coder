package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sps/internal/auth"
	"sps/internal/events"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func newTestDeps(t *testing.T) (Deps, *bytes.Buffer) {
	t.Helper()
	ev, err := events.Open(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { ev.Close() })
	var pinOut bytes.Buffer
	svc := auth.New("me@example.com", []byte(testSecret), auth.ConsoleMailer{Out: &pinOut})
	svc.MailerName = "console"
	return Deps{Events: ev, Version: "dev", Auth: svc}, &pinOut
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
	pin := regexp.MustCompile(`\d{6}`).FindString(pinOut.String())
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

func TestHealth(t *testing.T) {
	d, _ := newTestDeps(t)
	rec := get(t, New(d), "/health")
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
}

func TestMethodNotAllowed(t *testing.T) {
	d, _ := newTestDeps(t)
	rec := httptest.NewRecorder()
	New(d).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
