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
	"time"

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

func TestRequestPIN(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	rec := post(t, h, "/api/auth/request-pin", `{"email":"me@example.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !regexp.MustCompile(`\d{6}`).MatchString(pinOut.String()) {
		t.Fatalf("no pin printed: %q", pinOut.String())
	}
	ev := lastEvent(t, d)
	if ev.Type != "login.pin.sent" || ev.Data["email"] != "me@example.com" || ev.Data["delivery"] != "console" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestRequestPINWrongEmailIsSilent(t *testing.T) {
	d, pinOut := newTestDeps(t)
	rec := post(t, New(d), "/api/auth/request-pin", `{"email":"other@example.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no enumeration)", rec.Code)
	}
	if pinOut.Len() != 0 {
		t.Fatalf("pin sent to unconfigured email: %q", pinOut.String())
	}
	evs, _ := d.Events.Read(0, 0)
	for _, ev := range evs {
		if ev.Type == "login.pin.sent" {
			t.Fatalf("pin.sent event for unconfigured email: %+v", ev)
		}
	}
}

func TestRequestPINRateLimited(t *testing.T) {
	d, _ := newTestDeps(t)
	h := New(d)
	for i := 0; i < 5; i++ {
		if rec := post(t, h, "/api/auth/request-pin", `{"email":"me@example.com"}`); rec.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	if rec := post(t, h, "/api/auth/request-pin", `{"email":"me@example.com"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request: %d, want 429", rec.Code)
	}
}

func TestVerifyWrongPIN(t *testing.T) {
	d, _ := newTestDeps(t)
	rec := post(t, New(d), "/api/auth/verify", `{"email":"me@example.com","pin":"000000"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	ev := lastEvent(t, d)
	if ev.Type != "login.failure" || ev.Data["reason"] != "invalid_pin" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestLoginFlow(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := authedGet(t, h, cookie, "/api/auth/me")
	if rec.Code != http.StatusOK {
		t.Fatalf("me: %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["email"] != "me@example.com" {
		t.Fatalf("me = %+v", body)
	}

	if ev := lastEvent(t, d); ev.Type != "login.success" {
		t.Fatalf("unexpected last event: %+v", ev)
	}
}

func TestUnauthorized(t *testing.T) {
	d, _ := newTestDeps(t)
	h := New(d)
	if rec := get(t, h, "/api/events"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/events: %d, want 401", rec.Code)
	}
	if rec := get(t, h, "/api/auth/me"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/auth/me: %d, want 401", rec.Code)
	}
	if rec := get(t, h, "/health"); rec.Code != http.StatusOK {
		t.Fatalf("/health: %d, want 200 (public)", rec.Code)
	}
}

func TestLogout(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	loginCookie(t, h, pinOut)
	rec := post(t, h, "/api/auth/logout", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	// logout expires the browser cookie (stateless JWTs can't be revoked)
	var expired *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			expired = c
		}
	}
	if expired == nil || expired.MaxAge >= 0 {
		t.Fatalf("logout did not expire the session cookie: %+v", expired)
	}
}

func TestEventsAPI(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	for i := 0; i < 5; i++ {
		if _, err := d.Events.Append("test", map[string]any{"n": i}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	decode := func(rec *httptest.ResponseRecorder) []events.Event {
		t.Helper()
		var body struct {
			Events []events.Event `json:"events"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		return body.Events
	}

	// login already wrote login.pin.sent (1) and login.success (2)
	all := decode(authedGet(t, h, cookie, "/api/events"))
	if len(all) != 7 {
		t.Fatalf("got %d events, want 7: %+v", len(all), all)
	}
	if all[0].Type != "login.pin.sent" || all[1].Type != "login.success" {
		t.Fatalf("unexpected head: %+v", all[:2])
	}
	for i := 0; i < 5; i++ {
		if all[2+i].Type != "test" {
			t.Fatalf("unexpected event: %+v", all[2+i])
		}
	}

	page := decode(authedGet(t, h, cookie, "/api/events?after=2&limit=2"))
	if len(page) != 2 || page[0].ID != 3 || page[1].ID != 4 {
		t.Fatalf("unexpected page: %+v", page)
	}

	if rec := authedGet(t, h, cookie, "/api/events?after=abc"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad after: %d, want 400", rec.Code)
	}
}

// fakeEventLog is a minimal in-memory EventLog for handler tests.
type fakeEventLog struct {
	gotAfter int64
	gotLimit int
	out      []events.Event
}

func (f *fakeEventLog) Read(after int64, limit int) ([]events.Event, error) {
	f.gotAfter, f.gotLimit = after, limit
	return f.out, nil
}

func (f *fakeEventLog) Append(typ string, data map[string]any) (events.Event, error) {
	return events.Event{}, nil
}

// TestEventsAPIWithFakeEventLog proves the handler reads events from the
// injected EventLog, not from disk.
func TestEventsAPIWithFakeEventLog(t *testing.T) {
	d, pinOut := newTestDeps(t)

	// mint a session cookie through the service directly (no HTTP, no events)
	if err := d.Auth.RequestPIN(context.Background(), "me@example.com"); err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`\d{6}`).FindString(pinOut.String())
	token, err := d.Auth.Verify("me@example.com", pin)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.CookieName, Value: token}

	want := []events.Event{{ID: 4, Type: "test", Time: time.Now().UTC()}}
	fake := &fakeEventLog{out: want}
	d.Events = fake

	rec := authedGet(t, New(d), cookie, "/api/events?after=3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if fake.gotAfter != 3 || fake.gotLimit != 100 {
		t.Fatalf("handler read(after=%d, limit=%d), want (3, 100)", fake.gotAfter, fake.gotLimit)
	}
	var body struct {
		Events []events.Event `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(body.Events) != 1 || body.Events[0].ID != 4 {
		t.Fatalf("unexpected events: %+v", body.Events)
	}
}
