package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// Everything below runs at test Argon2id params: TestMain swaps them in
// for the whole package, so hashing is milliseconds, not seconds.
func TestMain(m *testing.M) {
	restore := TestParams()
	code := m.Run()
	restore()
	os.Exit(code)
}

func newService(t *testing.T, password string) (*Service, string) {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	return New("me@example.com", hash, 1, []byte(testSecret)), password
}

// The spec's core loop: setup hashes + persists, login verifies, wrong
// password fails, restart (new Service from the same stored values) works.
func TestLoginRoundTrip(t *testing.T) {
	svc, password := newService(t, "correct horse battery")

	tok, err := svc.Login("me@example.com", password, "1.2.3.4")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	cl, err := svc.verifyToken(tok)
	if err != nil || cl.Subject != "me@example.com" {
		t.Fatalf("verifyToken = %+v, %v", cl, err)
	}
	if cl.AuthVersion != 1 {
		t.Fatalf("token ver = %d, want 1", cl.AuthVersion)
	}

	if _, err := svc.Login("me@example.com", "wrong password", "1.2.3.4"); err == nil {
		t.Fatal("wrong password accepted")
	}

	// "restart": rebuild the service from the same persisted values
	restarted := New("me@example.com", svc.passHash, 1, []byte(testSecret))
	if _, err := restarted.Login("me@example.com", password, "1.2.3.4"); err != nil {
		t.Fatalf("login after restart: %v", err)
	}
}

// Hashing uses Argon2id with a unique salt per password: same plaintext,
// different bytes, and the encoding names argon2id (never a fast SHA).
func TestHashIsArgon2idWithUniqueSalt(t *testing.T) {
	a, err := HashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical — salt not unique")
	}
	for _, h := range []string{a, b} {
		if !strings.HasPrefix(h, "$argon2id$") {
			t.Fatalf("hash = %q, want argon2id", h)
		}
	}
}

// Unknown email and wrong password are indistinguishable: same error, and
// the unknown-email path still burns a hash comparison (timing parity).
func TestUnknownEmailAndWrongPasswordIdentical(t *testing.T) {
	svc, _ := newService(t, "correct horse battery")
	_, errA := svc.Login("nobody@example.com", "some password", "1.2.3.4")
	_, errB := svc.Login("me@example.com", "wrong password", "1.2.3.4")
	if errA == nil || errB == nil {
		t.Fatalf("expected both to fail: %v, %v", errA, errB)
	}
	if errA.Error() != errB.Error() {
		t.Fatalf("errors differ: %q vs %q", errA, errB)
	}
}

// Before setup runs, no password exists: every login fails closed.
func TestNoPasswordConfiguredFailsClosed(t *testing.T) {
	svc := New("me@example.com", "", 0, []byte(testSecret))
	if _, err := svc.Login("me@example.com", "anything", "1.2.3.4"); err == nil {
		t.Fatal("login with no configured password succeeded")
	}
}

// A stored malformed hash must fail closed, never panic or accept.
func TestMalformedStoredHashFailsClosed(t *testing.T) {
	svc := New("me@example.com", "not-a-hash", 1, []byte(testSecret))
	if _, err := svc.Login("me@example.com", "anything", "1.2.3.4"); err == nil {
		t.Fatal("malformed hash accepted")
	}
}

// Rate limits: per-source budget and a whole-instance budget, both in a
// rolling window; success clears the source's budget.
func TestLoginRateLimits(t *testing.T) {
	svc, _ := newService(t, "correct horse battery")
	for i := 0; i < maxFailsPerSource; i++ {
		if _, err := svc.Login("me@example.com", "wrong", "10.0.0.1"); err != ErrInvalidCredentials {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := svc.Login("me@example.com", "wrong", "10.0.0.1"); err != ErrRateLimited {
		t.Fatalf("per-source limit: %v, want ErrRateLimited", err)
	}
	// a different source still works within its own budget
	if _, err := svc.Login("me@example.com", "wrong", "10.0.0.2"); err != ErrInvalidCredentials {
		t.Fatalf("other source: %v", err)
	}

	// instance-wide budget: many sources can't dodge the limit either
	for i := 0; i < maxFailsPerInstance; i++ {
		svc.Login("me@example.com", "wrong", "10.0.0."+string(rune('a'+i%26)))
	}
	if _, err := svc.Login("me@example.com", "wrong", "10.9.9.9"); err != ErrRateLimited {
		t.Fatalf("instance limit: %v, want ErrRateLimited", err)
	}

	// success clears that source
	svc2, _ := newService(t, "correct horse battery")
	for i := 0; i < 3; i++ {
		svc2.Login("me@example.com", "wrong", "10.0.0.5")
	}
	if _, err := svc2.Login("me@example.com", "correct horse battery", "10.0.0.5"); err != nil {
		t.Fatalf("success under budget: %v", err)
	}
}

// The window rolls: old failures expire instead of permanently locking
// out a mistyping owner.
func TestRateLimitWindowExpires(t *testing.T) {
	svc, _ := newService(t, "correct horse battery")
	now := time.Unix(1_000_000, 0)
	svc.now = func() time.Time { return now }
	for i := 0; i < maxFailsPerSource; i++ {
		svc.Login("me@example.com", "wrong", "10.0.0.1")
	}
	if _, err := svc.Login("me@example.com", "wrong", "10.0.0.1"); err != ErrRateLimited {
		t.Fatalf("want limited, got %v", err)
	}
	now = now.Add(rateWindow + time.Minute)
	if _, err := svc.Login("me@example.com", "wrong", "10.0.0.1"); err != ErrInvalidCredentials {
		t.Fatalf("after window: %v, want ErrInvalidCredentials", err)
	}
}

// Applying a new password bumps authVersion: old tokens are rejected on
// every authed route, immediately, without waiting for expiry.
func TestAuthVersionInvalidatesTokens(t *testing.T) {
	svc, _ := newService(t, "correct horse battery")
	tok, err := svc.Login("me@example.com", "correct horse battery", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}

	handled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handled = true
		w.WriteHeader(http.StatusOK)
	})
	serve := func(s *Service, token string) int {
		handled = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		s.RequireAuth(next).ServeHTTP(rec, req)
		if handled && rec.Code != 200 {
			t.Fatal("handler ran but status is not 200")
		}
		return rec.Code
	}
	if code := serve(svc, tok); code != 200 {
		t.Fatalf("valid token: %d, want 200", code)
	}

	// rotation: SetPassword persists a new hash + bumped generation, then
	// the server restarts and rebuilds the service from those values.
	newHash, err := HashPassword("a-brand-new-password")
	if err != nil {
		t.Fatal(err)
	}
	restarted := New("me@example.com", newHash, 2, []byte(testSecret))
	if code := serve(restarted, tok); code != 401 {
		t.Fatalf("pre-rotation token after restart: %d, want 401", code)
	}
	// the old password is dead; the new credential issues a live token
	if _, err := restarted.Login("me@example.com", "correct horse battery", "1.2.3.4"); err == nil {
		t.Fatal("old password accepted after rotation")
	}
	fresh, err := restarted.Login("me@example.com", "a-brand-new-password", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if code := serve(restarted, fresh); code != 200 {
		t.Fatalf("fresh token: %d, want 200", code)
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got := NormalizeEmail("  Owner@Example.COM "); got != "owner@example.com" {
		t.Fatalf("NormalizeEmail = %q", got)
	}
}

// LoadOrCreateSecret: generated once, 0600, reused across "restarts".
func TestLoadOrCreateSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "jwt-secret")
	first, err := LoadOrCreateSecret(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 32 {
		t.Fatalf("secret too short: %d", len(first))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret perms = %o, want 600", info.Mode().Perm())
	}
	second, err := LoadOrCreateSecret(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("secret rotated on reload — every session would die")
	}
}

// Cookie flags: Secure mirrors the request (TLS or proxy-reported https),
// HttpOnly always, logout flips MaxAge negative with the same Secure bit
// (a non-Secure delete cannot clear a Secure cookie).
func TestCookieFlags(t *testing.T) {
	svc, _ := newService(t, "pw")
	cases := []struct {
		name      string
		tls       bool
		forwarded string
		want      bool
	}{
		{"plain http dev", false, "", false},
		{"direct tls", true, "", true},
		{"behind https proxy", false, "https", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			rec := httptest.NewRecorder()
			svc.SetCookie(rec, req, "tok")
			set := findCookie(t, rec, false)
			if set.Secure != tc.want || !set.HttpOnly || set.SameSite != http.SameSiteStrictMode {
				t.Fatalf("set cookie = %+v", set)
			}

			rec2 := httptest.NewRecorder()
			svc.ClearCookie(rec2, req)
			clear := findCookie(t, rec2, true)
			if clear.Secure != tc.want || clear.MaxAge >= 0 || clear.Value != "" {
				t.Fatalf("clear cookie = %+v", clear)
			}
		})
	}
}

func findCookie(t *testing.T, rec *httptest.ResponseRecorder, expired bool) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			if expired && c.MaxAge >= 0 {
				t.Fatalf("clearing cookie not expired: %+v", c)
			}
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}
