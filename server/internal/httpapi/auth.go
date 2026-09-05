// Authentication handlers: PIN request, verify, logout, and the /me
// identity endpoint. Login routes are public; /me requires a valid session.
package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"pcoder/internal/auth"
)

func handleRequestPIN(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email string `json:"email"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		body.Email = strings.TrimSpace(body.Email)
		err := d.Auth.RequestPIN(r.Context(), body.Email)
		switch {
		case errors.Is(err, auth.ErrNotConfiguredEmail):
			// Deliberately identical to success: don't reveal whether an
			// address is configured.
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case errors.Is(err, auth.ErrRateLimited):
			writeErr(w, http.StatusTooManyRequests, "too many requests, try again later")
		case err != nil:
			writeInternalErr(w, "request pin", err)
		default:
			d.Events.Append("login.pin.sent", map[string]any{"email": body.Email, "delivery": d.Auth.MailerName})
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}
	}
}

func handleVerify(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email string `json:"email"`
			Pin   string `json:"pin"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		token, err := d.Auth.Verify(strings.TrimSpace(body.Email), strings.TrimSpace(body.Pin))
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			d.Events.Append("login.failure", map[string]any{"email": body.Email, "reason": "rate_limited"})
			writeErr(w, http.StatusTooManyRequests, "too many attempts, try again later")
		case errors.Is(err, auth.ErrInvalidPIN):
			d.Events.Append("login.failure", map[string]any{"email": body.Email, "reason": "invalid_pin"})
			writeErr(w, http.StatusUnauthorized, "invalid pin")
		case err != nil:
			writeInternalErr(w, "verify pin", err)
		default:
			d.Events.Append("login.success", map[string]any{"email": body.Email})
			d.Auth.SetCookie(w, r, token)
			writeJSON(w, http.StatusOK, map[string]any{"email": body.Email})
		}
	}
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
