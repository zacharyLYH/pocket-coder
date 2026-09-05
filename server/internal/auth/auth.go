// Package auth implements the system's only login: a one-time PIN emailed to
// the configured PCODER_LOGIN_EMAIL, exchanged for a signed JWT stored in an
// HttpOnly cookie. Deliberately not a SaaS identity system — one email, one
// person.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenTTL is how long an issued JWT stays valid.
const TokenTTL = 7 * 24 * time.Hour

// PIN lifetime and rate limits.
const (
	defaultPinTTL     = 10 * time.Minute
	maxPinRequests    = 5 // per rolling window
	rateWindow        = 15 * time.Minute
	maxVerifyAttempts = 5
	secretMinLength   = 32
)

// CookieName is the session cookie carrying the JWT.
const CookieName = "pcoder_session"

// Errors returned by the service. Handlers map these to HTTP statuses.
var (
	ErrNotConfiguredEmail = errors.New("email is not the configured login email")
	ErrInvalidPIN         = errors.New("invalid or expired pin")
	ErrRateLimited        = errors.New("too many attempts, try again later")
)

// Mailer delivers the PIN. ConsoleMailer prints to a writer (dev); SmtpMailer
// sends via Google SMTP. Defined as an interface so it is mockable.
type Mailer interface {
	SendPIN(ctx context.Context, email, pin string) error
}

// Service issues and verifies PINs and JWTs for the one configured email.
type Service struct {
	email  string
	secret []byte
	mailer Mailer
	// MailerName is the delivery channel in use ("console" or "smtp"), for
	// events and logs.
	MailerName string

	now    func() time.Time
	pinTTL time.Duration

	mu       sync.Mutex
	pins     map[string]pinRecord   // email -> current pin
	requests map[string][]time.Time // email -> request times (rate window)
}

type pinRecord struct {
	pin      string
	expires  time.Time
	attempts int
}

// New returns a Service that sends PINs to email (normally PCODER_LOGIN_EMAIL)
// and signs tokens with secret.
func New(email string, secret []byte, mailer Mailer) *Service {
	return &Service{
		email:    email,
		secret:   secret,
		mailer:   mailer,
		now:      time.Now,
		pinTTL:   defaultPinTTL,
		pins:     map[string]pinRecord{},
		requests: map[string][]time.Time{},
	}
}

// LoadOrCreateSecret returns the signing secret from path, generating a
// random one (crypto/rand, 0600) and persisting it if missing or too short.
// This is the PCODER_JWT_SECRET fallback: a hardcoded default would let anyone
// forge tokens, so the secret is a file, not a constant.
func LoadOrCreateSecret(path string) ([]byte, error) {
	if raw, err := os.ReadFile(path); err == nil && len(raw) >= secretMinLength {
		return raw, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create secret dir: %w", err)
	}
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, fmt.Errorf("persist secret: %w", err)
	}
	return secret, nil
}

// RequestPIN sends a fresh PIN to email, rate-limited per rolling window.
// A non-configured email gets ErrNotConfiguredEmail and nothing is sent — the
// caller must not reveal whether an address is configured.
func (s *Service) RequestPIN(ctx context.Context, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if email != s.email {
		return ErrNotConfiguredEmail
	}
	now := s.now()
	kept := s.requests[email][:0]
	for _, t := range s.requests[email] {
		if now.Sub(t) < rateWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= maxPinRequests {
		s.requests[email] = kept
		return ErrRateLimited
	}
	s.requests[email] = append(kept, now)

	pin, err := newPIN()
	if err != nil {
		return err
	}
	s.pins[email] = pinRecord{pin: pin, expires: now.Add(s.pinTTL)}
	return s.mailer.SendPIN(ctx, email, pin)
}

// Verify checks a PIN and, on success, returns a signed JWT for email.
func (s *Service) Verify(email, pin string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if email != s.email {
		return "", ErrInvalidPIN
	}
	rec, ok := s.pins[email]
	if !ok {
		return "", ErrInvalidPIN
	}
	if s.now().After(rec.expires) {
		delete(s.pins, email)
		return "", ErrInvalidPIN
	}
	if rec.attempts >= maxVerifyAttempts {
		return "", ErrRateLimited
	}
	if subtle.ConstantTimeCompare([]byte(rec.pin), []byte(pin)) != 1 {
		rec.attempts++
		s.pins[email] = rec
		return "", ErrInvalidPIN
	}
	delete(s.pins, email)
	return s.issueToken(email)
}

type claims struct {
	Email string `json:"sub"`
	jwt.RegisteredClaims
}

func (s *Service) issueToken(email string) (string, error) {
	now := s.now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		Email: email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   email,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TokenTTL)),
		},
	})
	return tok.SignedString(s.secret)
}

func (s *Service) verifyToken(token string) (string, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		return "", fmt.Errorf("invalid token: %w", err)
	}
	return c.Email, nil
}

type emailKey struct{}

// RequireAuth wraps next, rejecting requests without a valid session cookie.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		email, err := s.verifyToken(c.Value)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), emailKey{}, email)))
	})
}

// Email returns the authenticated email for a request inside RequireAuth.
func (s *Service) Email(r *http.Request) string {
	v, _ := r.Context().Value(emailKey{}).(string)
	return v
}

// sessionCookie builds the session cookie; maxAge < 0 expires it.
func sessionCookie(token string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
}

// SetCookie writes the session cookie for token. Secure is set when the
// request arrived over TLS, directly or behind a reverse proxy that reports
// https via X-Forwarded-Proto.
func (s *Service) SetCookie(w http.ResponseWriter, r *http.Request, token string) {
	c := sessionCookie(token, int(TokenTTL.Seconds()))
	c.Secure = r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, c)
}

// ClearCookie expires the session cookie.
func (s *Service) ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, sessionCookie("", -1))
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}

// newPIN returns a random 6-digit PIN.
func newPIN() (string, error) {
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("generate pin: %w", err)
		}
		n := binary.BigEndian.Uint32(b[:]) % 1_000_000
		if n >= 100_000 {
			return fmt.Sprintf("%06d", n), nil
		}
	}
}
