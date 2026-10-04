package httpapi

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Same-origin guard against cross-site request forgery.
//
// The session cookie is SameSite=Strict, which already keeps browsers from
// attaching it to cross-site requests. This is the second lock for the day
// SameSite has a gap (new browser behavior, non-browser client, or a
// same-site attacker page): any API write (POST/PUT/PATCH/DELETE) or
// WebSocket handshake carrying a foreign Origin is rejected with 403,
// before auth or any state change runs.
//
// Rules, in order:
//  1. No Origin header → allow. curl, health probes, and native clients
//     send none; only browsers add Origin, and browsers always add it on
//     POSTs and WebSocket handshakes.
//  2. Origin host == request host (ports/scheme ignored) → allow. Normal
//     same-origin app use, including the Tunnel (browser and Host are both
//     the public domain).
//  3. Origin host is loopback (localhost/127.0.0.1/::1) → allow. Dev runs
//     the browser on :5173 while Vite rewrites Host to the backend, so the
//     hosts textually differ. An attacker cannot mint a localhost Origin
//     without already running code on the victim's machine.
//  4. Otherwise → deny. Evil-site pages land here.
//
// GETs without an Upgrade header are never checked: top-level navigations
// and subresources (<img>, preview assets) legitimately carry foreign or
// missing Origins and change no state.
func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	oh, ok := originHostname(origin)
	if !ok {
		return false
	}
	rh := requestHostname(r)
	if strings.EqualFold(oh, rh) {
		return true
	}
	if isLoopbackHost(oh) {
		return true
	}
	return false
}

// needsOriginCheck reports whether the request is in scope: a state-changing
// method or a WebSocket handshake under /api or /ws.
func needsOriginCheck(r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api") && !strings.HasPrefix(p, "/ws") {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	// WebSocket handshakes are GETs with an Upgrade header — this covers the
	// terminal socket (/ws/...) and the noVNC websockify socket that lives
	// under /api/.../preview/websockify.
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return true
	}
	return false
}

// requireSameOriginForWrites rejects cross-site API writes and foreign
// WebSocket handshakes before they reach auth or any handler.
func requireSameOriginForWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if needsOriginCheck(r) && !originAllowed(r) {
			writeErr(w, http.StatusForbidden, "cross-site request blocked")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// wsOriginAllowed is the gorilla upgrader's CheckOrigin: a second layer on
// the terminal socket itself, so a future route that skips the middleware
// is still guarded at upgrade time.
func wsOriginAllowed(r *http.Request) bool {
	return originAllowed(r)
}

// originHostname extracts the lowercased hostname from an Origin header
// value like "https://app.example.com:8443".
func originHostname(origin string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	return strings.ToLower(u.Hostname()), true
}

// requestHostname extracts the lowercased hostname the client addressed,
// ignoring any port. Bracketed IPv6 ("[::1]:8080") is handled.
func requestHostname(r *http.Request) string {
	h := r.Host
	if h == "" {
		h = r.URL.Host
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	return strings.ToLower(h)
}

// isLoopbackHost reports dev-machine names. Only the Origin side is tested:
// a remote attacker cannot make a victim's browser emit these.
func isLoopbackHost(h string) bool {
	switch h {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
