// Package auth implements the system's only login: the per-instance email +
// password account, verified against an Argon2id hash, exchanged for a
// signed JWT stored in an HttpOnly cookie. Deliberately not a SaaS identity
// system — one email, one password, one instance.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
)

// TokenTTL is how long an issued JWT stays valid.
const TokenTTL = 7 * 24 * time.Hour

// Login rate limits: per-source and per-instance rolling windows.
const (
	maxFailsPerSource   = 10
	maxFailsPerInstance = 50
	rateWindow          = 15 * time.Minute
	secretMinLength     = 32
)

// Argon2id parameters (OWASP-ish: 64 MiB, 3 passes). Tests lower them via
// TestParams so the suite doesn't burn seconds per hash.
var argonParams = params{time: 3, memory: 64 * 1024, threads: 4, keyLen: 32}

// TestParams swaps the Argon2id parameters for fast tests, returning a
// restore func. Hashes minted under test params verify under test params;
// production and test hashes never mix because every suite sets this
// before hashing.
func TestParams() func() {
	old := argonParams
	argonParams = params{time: 1, memory: 1024, threads: 1, keyLen: 32}
	return func() { argonParams = old }
}

// TestHashFor returns the Argon2id hash of TestPassword under TestParams
// with a fixed salt, for state.json seeds and unit-test wiring that must
// not pay a hash per construction. Never use in production.
const TestPassword = "test-password-12345"

func testSalt() []byte { return []byte("pcoder-test-salt!")[:16] }

// TestHashFor returns the deterministic test hash. Callers must have set
// TestParams first so the embedded params match.
func TestHashFor() string {
	return encodeHash(TestPassword, testSalt(), argonParams)
}

type params struct {
	time    uint32
	memory  uint32
	threads uint8
	keyLen  uint32
}

// CookieName is the session cookie carrying the JWT.
const CookieName = "pcoder_session"

// Errors returned by the service. Handlers map these to HTTP statuses.
var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrRateLimited        = errors.New("too many attempts, try again later")
)

// Service verifies the one configured email + Argon2id password hash and
// issues versioned JWTs. AuthVersion is baked into every token: applying a
// new setup password bumps it, so tokens from before stop working.
type Service struct {
	// email, passHash, authVersion, and secret are fixed at construction;
	// only the failure ledger below needs the lock.
	mu          sync.Mutex
	email       string
	passHash    string
	authVersion int64
	secret      []byte
	fails       []failRecord
	now         func() time.Time
}

type failRecord struct {
	at     time.Time
	source string
}

// New returns a Service for email (already normalized) + PHC passHash.
func New(email, passHash string, authVersion int64, secret []byte) *Service {
	return &Service{
		email: email, passHash: passHash, authVersion: authVersion,
		secret: secret, now: time.Now,
	}
}

// NormalizeEmail is the single email normalization: trim + lowercase.
// Setup and login must agree, so both go through here.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Login verifies email + password and, on success, returns a signed JWT.
// Unknown email and wrong password return the identical generic failure;
// password comparison always runs against a hash (a fixed dummy when the
// email is unknown or no password is set) so failures take the same path.
func (s *Service) Login(email, password, source string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limitedLocked(source) {
		return "", ErrRateLimited
	}
	fail := func() (string, error) {
		s.fails = append(s.fails, failRecord{at: s.now(), source: source})
		return "", ErrInvalidCredentials
	}
	if subtle.ConstantTimeCompare(
		[]byte(NormalizeEmail(email)), []byte(s.email)) != 1 {
		_ = verifyHash(dummyHash(), password)
		return fail()
	}
	if s.passHash == "" {
		_ = verifyHash(dummyHash(), password)
		return fail()
	}
	if err := verifyHash(s.passHash, password); err != nil {
		return fail()
	}
	s.clearSourceLocked(source)
	return s.issueToken(s.email, s.authVersion)
}

func (s *Service) limitedLocked(source string) bool {
	now := s.now()
	kept := s.fails[:0]
	var bySource, total int
	for _, f := range s.fails {
		if now.Sub(f.at) >= rateWindow {
			continue
		}
		kept = append(kept, f)
		total++
		if f.source == source {
			bySource++
		}
	}
	s.fails = kept
	return bySource >= maxFailsPerSource || total >= maxFailsPerInstance
}

func (s *Service) clearSourceLocked(source string) {
	kept := s.fails[:0]
	for _, f := range s.fails {
		if f.source != source {
			kept = append(kept, f)
		}
	}
	s.fails = kept
}

// HashPassword hashes password with Argon2id and a fresh random salt,
// encoded as PHC string. Plaintext never leaves the caller.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	return encodeHash(password, salt, argonParams), nil
}

func encodeHash(password string, salt []byte, p params) string {
	hash := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash))
}

func verifyHash(encoded, password string) error {
	p, salt, want, ok := decodeHash(encoded)
	if !ok {
		return ErrInvalidCredentials
	}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrInvalidCredentials
	}
	return nil
}

func decodeHash(encoded string) (params, []byte, []byte, bool) {
	var p params
	var v int
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, false
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return p, nil, nil, false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return p, nil, nil, false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	hash, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) == 0 || len(hash) == 0 {
		return p, nil, nil, false
	}
	p.keyLen = uint32(len(hash))
	return p, salt, hash, true
}

func dummyHash() string {
	return encodeHash("pcoder-dummy-password", bytes16(), argonParams)
}

func bytes16() []byte {
	return []byte("pcoder-dummy-salt")[:16]
}

// LoadOrCreateSecret returns the signing secret from path, generating a
// random one (crypto/rand, 0600) and persisting it if missing or too short.
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

type claims struct {
	AuthVersion int64 `json:"ver"`
	jwt.RegisteredClaims
}

func (s *Service) issueToken(email string, version int64) (string, error) {
	now := s.now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		AuthVersion: version,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   email,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TokenTTL)),
		},
	})
	return tok.SignedString(s.secret)
}

// IssueToken mints a token for the configured credential (tests).
func (s *Service) IssueToken() (string, error) {
	return s.issueToken(s.email, s.authVersion)
}

func (s *Service) verifyToken(token string) (claims, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		return c, fmt.Errorf("invalid token: %w", err)
	}
	return c, nil
}

type emailKey struct{}

// RequireAuth wraps next, rejecting requests without a valid session cookie.
// Tokens must carry the current auth version and the configured email:
// rotating the setup password bumps the version, so cookies from every
// device stop working immediately.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		cl, verr := s.verifyToken(c.Value)
		ok := verr == nil && cl.AuthVersion == s.authVersion &&
			subtle.ConstantTimeCompare([]byte(cl.Subject), []byte(s.email)) == 1
		if !ok {
			writeUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), emailKey{}, s.email)))
	})
}

// Email returns the authenticated email for a request inside RequireAuth.
func (s *Service) Email(r *http.Request) string {
	v, _ := r.Context().Value(emailKey{}).(string)
	return v
}

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

// ClearCookie expires the session cookie. Secure must mirror SetCookie: a
// non-Secure Set-Cookie cannot overwrite/delete a Secure cookie, so
// without this logout on HTTPS leaves the session alive in the browser.
func (s *Service) ClearCookie(w http.ResponseWriter, r *http.Request) {
	c := sessionCookie("", -1)
	c.Secure = r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, c)
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("{\"error\":\"unauthorized\"}"))
}
