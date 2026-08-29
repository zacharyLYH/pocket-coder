package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"sps/internal/auth"
	"sps/internal/events"
)

func TestUnauthorized(t *testing.T) {
	d, _ := newTestDeps(t)
	h := New(d)
	if rec := get(t, h, "/api/events"); rec.Code != 401 {
		t.Fatalf("/api/events: %d, want 401", rec.Code)
	}
	if rec := get(t, h, "/api/auth/me"); rec.Code != 401 {
		t.Fatalf("/api/auth/me: %d, want 401", rec.Code)
	}
	if rec := get(t, h, "/health"); rec.Code != 200 {
		t.Fatalf("/health: %d, want 200 (public)", rec.Code)
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

	if rec := authedGet(t, h, cookie, "/api/events?after=abc"); rec.Code != 400 {
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
	if rec.Code != 200 {
		t.Fatalf("status = %d, want %d", rec.Code, 200)
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
