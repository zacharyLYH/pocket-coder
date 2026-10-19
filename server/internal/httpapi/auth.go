// Authentication handlers: password login, logout, and the /me identity
// endpoint. Login is public; /me requires a valid session.
package httpapi

import (
	"errors"
	"net"
	"net/http"
	"strings"

	"pcoder/internal/auth"
)

func handleLogin(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		token, err := d.Auth.Login(body.Email, body.Password, clientIP(r))
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			d.Events.Append("login.failure", map[string]any{"reason": "rate_limited"})
			writeErr(w, http.StatusTooManyRequests, "too many attempts, try again later")
		case err != nil:
			d.Events.Append("login.failure", map[string]any{"reason": "invalid_credentials"})
			// Identical generic failure for unknown email, wrong password,
			// and missing credential: nothing reveals which half failed.
			writeErr(w, http.StatusUnauthorized, "invalid email or password")
		default:
			email := auth.NormalizeEmail(body.Email)
			d.Events.Append("login.success", map[string]any{"email": email})
			d.Auth.SetCookie(w, r, token)
			writeJSON(w, http.StatusOK, map[string]any{"email": email})
		}
	}
}

// clientIP is the rate-limit key: X-Forwarded-For's leftmost entry behind
// the nginx proxy, else the direct peer. Not security-critical (limits are
// defense-in-depth over the generic failures), so no trusted-proxy list.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(ip)
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func handleLogout(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.Auth.ClearCookie(w, r)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleMe(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"email": d.Auth.Email(r)})
	}
}
