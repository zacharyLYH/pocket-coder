package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	authmocks "pcoder/mocks/auth"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func newService(t *testing.T) (*Service, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	return New("me@example.com", []byte(testSecret), ConsoleMailer{Out: &out}), &out
}

func TestPINFlowRoundTrip(t *testing.T) {
	svc, out := newService(t)
	ctx := context.Background()

	if err := svc.RequestPIN(ctx, "me@example.com"); err != nil {
		t.Fatalf("request pin: %v", err)
	}
	pin := regexp.MustCompile(`\d{6}`).FindString(out.String())
	if pin == "" {
		t.Fatalf("no pin printed: %q", out.String())
	}

	token, err := svc.Verify("me@example.com", pin)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	// the token verifies and carries the email
	email, err := svc.verifyToken(token)
	if err != nil || email != "me@example.com" {
		t.Fatalf("verifyToken = %q, %v", email, err)
	}

	// a used pin is gone
	if _, err := svc.Verify("me@example.com", pin); !errors.Is(err, ErrInvalidPIN) {
		t.Fatalf("reuse of consumed pin: %v", err)
	}
}

func TestWrongEmailIsSilent(t *testing.T) {
	svc, out := newService(t)
	err := svc.RequestPIN(context.Background(), "other@example.com")
	if !errors.Is(err, ErrNotConfiguredEmail) {
		t.Fatalf("err = %v, want ErrNotConfiguredEmail", err)
	}
	if out.Len() != 0 {
		t.Fatalf("pin printed for unconfigured email: %q", out.String())
	}
	if _, err := svc.Verify("other@example.com", "123456"); !errors.Is(err, ErrInvalidPIN) {
		t.Fatalf("verify wrong email: %v", err)
	}
}

func TestWrongPinBurnsAttempts(t *testing.T) {
	svc, out := newService(t)
	if err := svc.RequestPIN(context.Background(), "me@example.com"); err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`\d{6}`).FindString(out.String())

	for i := 0; i < maxVerifyAttempts; i++ {
		if _, err := svc.Verify("me@example.com", "000000"); !errors.Is(err, ErrInvalidPIN) {
			t.Fatalf("attempt %d: err = %v, want ErrInvalidPIN", i, err)
		}
	}
	// the correct pin is now locked out too
	if _, err := svc.Verify("me@example.com", pin); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("after lockout: %v, want ErrRateLimited", err)
	}
}

func TestRequestRateLimit(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	for i := 0; i < maxPinRequests; i++ {
		if err := svc.RequestPIN(ctx, "me@example.com"); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if err := svc.RequestPIN(ctx, "me@example.com"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("6th request: %v, want ErrRateLimited", err)
	}
}

func TestPinExpiry(t *testing.T) {
	svc, out := newService(t)
	svc.pinTTL = time.Minute
	svc.now = func() time.Time { return time.Unix(1_000_000, 0) }
	if err := svc.RequestPIN(context.Background(), "me@example.com"); err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`\d{6}`).FindString(out.String())

	svc.now = func() time.Time { return time.Unix(1_000_000, 0).Add(time.Minute + time.Second) }
	if _, err := svc.Verify("me@example.com", pin); !errors.Is(err, ErrInvalidPIN) {
		t.Fatalf("expired pin: %v, want ErrInvalidPIN", err)
	}
}

func TestTokenExpiryAndTamper(t *testing.T) {
	svc, _ := newService(t)

	tok, err := svc.issueToken("me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.verifyToken(tok); err != nil {
		t.Fatalf("fresh token should verify: %v", err)
	}

	// tampered payload
	parts := strings.Split(tok, ".")
	parts[1] = "AAAA"
	if _, err := svc.verifyToken(strings.Join(parts, ".")); err == nil {
		t.Fatal("tampered token verified")
	}

	// wrong secret
	other := New("me@example.com", []byte("fedcba9876543210fedcba9876543210"), ConsoleMailer{Out: &bytes.Buffer{}})
	if _, err := other.verifyToken(tok); err == nil {
		t.Fatal("token verified with wrong secret")
	}

	// expired
	expired := New("me@example.com", []byte(testSecret), ConsoleMailer{Out: &bytes.Buffer{}})
	expired.now = func() time.Time { return time.Unix(1_000_000, 0) }
	oldTok, err := expired.issueToken("me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Unix(1_000_000, 0).Add(TokenTTL + time.Second) }
	if _, err := svc.verifyToken(oldTok); err == nil {
		t.Fatal("expired token verified")
	}
}

func TestLoadOrCreateSecret(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/jwt-secret"
	s1, err := LoadOrCreateSecret(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s1) < secretMinLength {
		t.Fatalf("secret only %d bytes", len(s1))
	}
	info, err := statPerms(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if info != 0o600 {
		t.Fatalf("secret perms = %o, want 600", info)
	}
	s2, err := LoadOrCreateSecret(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s1, s2) {
		t.Fatal("secret changed between loads")
	}
}

func TestMiddleware(t *testing.T) {
	svc, _ := newService(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc.Email(r) != "me@example.com" {
			http.Error(w, "no email in context", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := svc.RequireAuth(next)

	tok, err := svc.issueToken("me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		cookie *http.Cookie
		want   int
	}{
		{"no cookie", nil, http.StatusUnauthorized},
		{"garbage cookie", &http.Cookie{Name: CookieName, Value: "garbage"}, http.StatusUnauthorized},
		{"valid cookie", &http.Cookie{Name: CookieName, Value: tok}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s: %d, want %d", tc.name, rec.Code, tc.want)
			}
		})
	}
}

// TestMailerMocked proves the mockery mock substitutes for a real mailer.
func TestMailerMocked(t *testing.T) {
	m := authmocks.NewMockMailer(t)
	m.EXPECT().SendPIN(context.Background(), "me@example.com", mock.Anything).Return(nil)
	svc := New("me@example.com", []byte(testSecret), m)
	if err := svc.RequestPIN(context.Background(), "me@example.com"); err != nil {
		t.Fatalf("request pin: %v", err)
	}
}

// SetCookie must mark the session cookie Secure whenever the request arrived
// encrypted — directly over TLS or via a reverse proxy reporting https — so
// the cookie never rides plaintext HTTP in production.
func TestSetCookieSecureFlag(t *testing.T) {
	svc, _ := newService(t)
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

			var got *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == CookieName {
					got = c
				}
			}
			if got == nil {
				t.Fatal("no session cookie set")
			}
			if got.Secure != tc.want {
				t.Fatalf("Secure = %v, want %v", got.Secure, tc.want)
			}
		})
	}
}

func statPerms(t *testing.T, path string) (os.FileMode, error) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Mode().Perm(), nil
}
