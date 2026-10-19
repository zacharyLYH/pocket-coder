package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"pcoder/internal/auth"
)

const loginBody = `{"email":"me@example.com","password":"` + auth.TestPassword + `"}`

func TestLoginSuccessSetsCookieAndEvent(t *testing.T) {
	d := newTestDeps(t)
	h := New(d)
	rec := post(t, h, "/api/auth/login", loginBody)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["email"] != "me@example.com" {
		t.Fatalf("login body = %+v", body)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("no session cookie on successful login")
	}
	ev := lastEvent(t, d)
	if ev.Type != "login.success" || ev.Data["email"] != "me@example.com" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

// Unknown email, wrong password, and wrong-but-well-formed email must be
// indistinguishable: same status, same body. No account enumeration.
func TestLoginFailuresAreIdenticalAndGeneric(t *testing.T) {
	d := newTestDeps(t)
	h := New(d)
	cases := map[string]string{
		"unknown email":    `{"email":"nobody@example.com","password":"whatever-12345"}`,
		"wrong password":   `{"email":"me@example.com","password":"wrong-password"}`,
		"empty password":   `{"email":"me@example.com","password":""}`,
		"unconfigured any": `{"email":"not-even-an-email","password":"whatever-12345"}`,
	}
	var firstBody string
	first := true
	for name, body := range cases {
		rec := post(t, h, "/api/auth/login", body)
		if rec.Code != 401 {
			t.Fatalf("%s: status = %d, want 401", name, rec.Code)
		}
		if first {
			firstBody = rec.Body.String()
			first = false
			continue
		}
		if rec.Body.String() != firstBody {
			t.Fatalf("%s body = %q, want identical to %q", name, rec.Body.String(), firstBody)
		}
	}
	if strings.TrimRight(firstBody, "\n") != `{"error":"invalid email or password"}` {
		t.Fatalf("generic failure body = %q", firstBody)
	}
	evs, _ := d.Events.Read(0, 0)
	for _, ev := range evs {
		if ev.Type == "login.success" {
			t.Fatalf("success event on a failed login: %+v", ev)
		}
	}
}

// Email comparison trims + lowercases exactly like setup: a casually
// re-typed address still logs in, and casing can't be probed.
func TestLoginEmailNormalizationMatchesSetup(t *testing.T) {
	d := newTestDeps(t)
	h := New(d)
	rec := post(t, h, "/api/auth/login",
		`{"email":"  ME@Example.com ","password":"`+auth.TestPassword+`"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
}

// Same source IP failing repeatedly gets 429; a successful login clears
// its own budget; the failure events say rate_limited, nothing about the
// account.
func TestLoginRateLimit(t *testing.T) {
	d := newTestDeps(t)
	h := New(d)
	for i := 0; i < 10; i++ {
		rec := post(t, h, "/api/auth/login", `{"email":"me@example.com","password":"wrong-password"}`)
		if rec.Code != 401 {
			t.Fatalf("attempt %d: status = %d, want 401", i, rec.Code)
		}
	}
	rec := post(t, h, "/api/auth/login", `{"email":"me@example.com","password":"wrong-password"}`)
	if rec.Code != 429 {
		t.Fatalf("11th attempt: status = %d, want 429", rec.Code)
	}
	ev := lastEvent(t, d)
	if ev.Type != "login.failure" || ev.Data["reason"] != "rate_limited" {
		t.Fatalf("unexpected last event: %+v", ev)
	}
}

func TestLoginFlow(t *testing.T) {
	d := newTestDeps(t)
	h := New(d)
	cookie := loginCookie(t, h)

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

// Rotating the setup password bumps authVersion: every cookie minted
// under the old generation dies on every authenticated route, on every
// device — no cookie expiry roulette.
func TestPasswordRotationInvalidatesExistingSessions(t *testing.T) {
	d := newTestDeps(t)
	h := New(d)
	cookie := loginCookie(t, h) // generation 1
	if rec := authedGet(t, h, cookie, "/api/auth/me"); rec.Code != 200 {
		t.Fatalf("pre-rotation me: %d", rec.Code)
	}

	restore := auth.TestParams()
	defer restore()
	newHash, err := auth.HashPassword("a-brand-new-password")
	if err != nil {
		t.Fatal(err)
	}
	// Restart the process: the persisted hash + bumped generation build a
	// fresh auth service, exactly as newAuthService does at boot.
	d.Auth = auth.New("me@example.com", newHash, 2, []byte(testSecret))
	h = New(d)

	for _, path := range []string{"/api/auth/me", "/api/events", "/api/observe/meta"} {
		if rec := authedGet(t, h, cookie, path); rec.Code != 401 {
			t.Fatalf("%s with pre-rotation cookie: %d, want 401", path, rec.Code)
		}
	}
	// the NEW password logs in; the pre-rotation one is dead
	if rec := post(t, h, "/api/auth/login",
		`{"email":"me@example.com","password":"a-brand-new-password"}`); rec.Code != 200 {
		t.Fatalf("login with rotated password: %d", rec.Code)
	}
	if rec := post(t, h, "/api/auth/login", loginBody); rec.Code != 401 {
		t.Fatalf("old password after rotation: %d, want 401", rec.Code)
	}
}

func TestLogout(t *testing.T) {
	d := newTestDeps(t)
	h := New(d)
	loginCookie(t, h)
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

// Credentials never ride in query strings, and /me never echoes a token.
func TestLoginRejectsQueryCredentialsAndMeNeverLeaks(t *testing.T) {
	d := newTestDeps(t)
	h := New(d)
	if rec := get(t, h, "/api/auth/login?email=me@example.com&password=x"); rec.Code != 405 {
		t.Fatalf("GET login: %d, want 405 (POST only)", rec.Code)
	}
	cookie := loginCookie(t, h)
	rec := authedGet(t, h, cookie, "/api/auth/me")
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	for _, v := range body {
		if v == auth.TestPassword {
			t.Fatal("/me echoed the password")
		}
	}
}
