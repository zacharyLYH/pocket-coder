package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"sps/internal/auth"
)

func TestRequestPIN(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	rec := post(t, h, "/api/auth/request-pin", `{"email":"me@example.com"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !pinRe.MatchString(pinOut.String()) {
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
	if rec.Code != 200 {
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
		if rec := post(t, h, "/api/auth/request-pin", `{"email":"me@example.com"}`); rec.Code != 200 {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	if rec := post(t, h, "/api/auth/request-pin", `{"email":"me@example.com"}`); rec.Code != 429 {
		t.Fatalf("6th request: %d, want 429", rec.Code)
	}
}

func TestVerifyWrongPIN(t *testing.T) {
	d, _ := newTestDeps(t)
	rec := post(t, New(d), "/api/auth/verify", `{"email":"me@example.com","pin":"000000"}`)
	if rec.Code != 401 {
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
	if rec.Code != 200 {
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

func TestLogout(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	loginCookie(t, h, pinOut)
	rec := post(t, h, "/api/auth/logout", "")
	if rec.Code != 200 {
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
