package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler marks pass-through: the guard let the request reach the handler.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func guardedRequest(method, target, host, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	requireSameOriginForWrites(okHandler).ServeHTTP(rec, req)
	return rec
}

// --- attacks blocked ---

func TestOrigin_BlocksCrossSiteWrites(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		rec := guardedRequest(method, "https://app.example.com/api/projects/x/sessions/y/inject",
			"app.example.com", "https://evil.example.net")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s evil origin: got %d, want 403", method, rec.Code)
		}
	}
}

func TestOrigin_BlocksForeignWebSocketHandshake(t *testing.T) {
	req := httptest.NewRequest("GET", "https://app.example.com/ws/projects/p/sessions/main", nil)
	req.Host = "app.example.com"
	req.Header.Set("Origin", "https://evil.example.net")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	requireSameOriginForWrites(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("evil WS handshake: got %d, want 403", rec.Code)
	}
	// The upgrader's own check agrees, so a route that skips the middleware
	// is still guarded at upgrade time.
	if wsOriginAllowed(req) {
		t.Fatal("wsOriginAllowed(evil) = true, want false")
	}
}

func TestOrigin_BlocksMalformedOrigin(t *testing.T) {
	rec := guardedRequest("POST", "https://app.example.com/api/projects",
		"app.example.com", "not-a-url:%zz")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("malformed origin: got %d, want 403", rec.Code)
	}
}

// --- happy paths still pass ---

func TestOrigin_AllowsSameOrigin(t *testing.T) {
	// Scheme and port are ignored: the Tunnel serves https while nginx sees
	// http, and dev runs :5173 against :8080.
	for _, origin := range []string{"https://app.example.com", "http://app.example.com:8080"} {
		rec := guardedRequest("POST", "https://app.example.com/api/projects/x/stop",
			"app.example.com", origin)
		if rec.Code != http.StatusOK {
			t.Fatalf("same origin %q: got %d, want 200", origin, rec.Code)
		}
	}
}

func TestOrigin_AllowsMissingOrigin(t *testing.T) {
	// curl, health probes, native clients send no Origin.
	rec := guardedRequest("POST", "https://app.example.com/api/auth/verify",
		"app.example.com", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("missing origin: got %d, want 200", rec.Code)
	}
}

func TestOrigin_AllowsDevLoopbackOrigin(t *testing.T) {
	// Vite dev: browser on :5173, Host rewritten to the backend by
	// changeOrigin, so hosts textually differ.
	rec := guardedRequest("POST", "http://server:8080/api/projects",
		"server:8080", "http://localhost:5173")
	if rec.Code != http.StatusOK {
		t.Fatalf("dev loopback origin: got %d, want 200", rec.Code)
	}
	if !wsOriginAllowed(httptest.NewRequest("GET", "http://localhost:8080/ws/x", nil)) {
		t.Fatal("wsOriginAllowed(missing origin) = false, want true")
	}
}

func TestOrigin_LeavesReadsAndOtherPathsAlone(t *testing.T) {
	// Cross-site reads change no state and are still covered by SameSite.
	rec := guardedRequest("GET", "https://app.example.com/api/projects",
		"app.example.com", "https://evil.example.net")
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-site GET: got %d, want 200 (out of scope)", rec.Code)
	}
	// Non-API paths are out of scope.
	rec = guardedRequest("POST", "https://app.example.com/other", "app.example.com", "https://evil.example.net")
	if rec.Code != http.StatusOK {
		t.Fatalf("non-API POST: got %d, want 200 (out of scope)", rec.Code)
	}
}
