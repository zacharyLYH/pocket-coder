package httpapi

import (
	"strings"
	"testing"
)

// GET /api/state streams state.json verbatim — SMTP password, AI API keys,
// the SSH private key. No intermediary or browser cache may store it.
func TestGetStateSetsNoStore(t *testing.T) {
	d, _, pinOut, _ := newSessionDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	for _, path := range []string{"/api/state", "/api/state?download=true"} {
		rec := authedGet(t, h, cookie, path)
		if rec.Code != 200 {
			t.Fatalf("GET %s: got %d, want 200", path, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q, want no-store", path, cc)
		}
		if p := rec.Header().Get("Pragma"); p != "no-cache" {
			t.Fatalf("GET %s Pragma = %q, want no-cache", path, p)
		}
	}
	// The settings wipe flow still forces a download.
	rec := authedGet(t, h, cookie, "/api/state?download=true")
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("download Content-Disposition = %q, want attachment", cd)
	}
}
