package httpapi

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Edge cases drawn from similar stacks (ttyd/xterm.js, coder/codespaces port
// detection, noVNC-behind-reverse-proxy, CDP automation). Each test pins the
// behavior this app needs; failures below are real bugs, not aspirations.

// Port detection must only report LISTENING sockets. ss without -l (or a
// future caller passing full output) includes ESTABLISHED flows whose remote
// ports are not servers. Reporting them makes the Preview tab offer dead
// ports and can auto-start the sidecar against a port nothing serves.
func TestEdge_PortsIgnoreNonListening(t *testing.T) {
	output := "State Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n" +
		"ESTAB 0 0 10.0.0.5:22 1.2.3.4:54321 users:((\"sshd\",pid=1,fd=3))\n" +
		"ESTAB 0 0 10.0.0.5:3000 1.2.3.4:60000 users:((\"node\",pid=2,fd=4))"
	got := parseListeningPorts(output)
	if len(got) != 0 {
		t.Fatalf("ESTABLISHED flows reported as live servers: %v", got)
	}
}

// A process name containing a colon-port must not become a phantom server.
// ss appends users:(("name",pid=..,fd=..)); a name like "app:9999" currently
// trips the scan-every-field logic.
func TestEdge_PortsIgnoreProcessNameFalsePositive(t *testing.T) {
	output := "LISTEN 0 128 127.0.0.1:3000 0.0.0.0:* users:((\"app:9999\",pid=1,fd=3))"
	got := parseListeningPorts(output)
	if len(got) != 1 || got[0]["port"].(int) != 3000 {
		t.Fatalf("got %v, want exactly [3000]", got)
	}
}

// samePreviewOrigin must handle IPv6 loopback. Dev servers on ::1 are the
// same machine as 127.0.0.1, and an IPv6 URL must at least equal itself.
// The current strings.Cut(host, ":") normalization breaks on bracketed
// IPv6 hosts.
func TestEdge_SameOriginIPv6(t *testing.T) {
	if !samePreviewOrigin("http://[::1]:3000/", "http://[::1]:3000/") {
		t.Fatal("identical IPv6 origins not recognized")
	}
	if !samePreviewOrigin("http://127.0.0.1:3000", "http://[::1]:3000/") {
		t.Fatal("IPv4 and IPv6 loopback not treated as the same origin")
	}
	if samePreviewOrigin("http://127.0.0.1:3000", "http://127.0.0.1:4000") {
		t.Fatal("different ports must not match")
	}
}

// The noVNC reverse proxy must not let path traversal escape the sidecar's
// web root. handlePreviewSurface derives `path` from the request URL and the
// Director splices it onto the upstream; ".." segments must be neutralized,
// never forwarded.
func TestEdge_PreviewProxyRejectsTraversal(t *testing.T) {
	target, err := url.Parse("http://10.0.0.8:6080")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../etc/passwd", "a/../../etc/passwd", ".."} {
		proxy := newPreviewProxy(target, p)
		req := httptest.NewRequest("GET", "/api/projects/p1/preview/"+p, nil)
		proxy.Director(req)
		if containsDotDot(req.URL.Path) {
			t.Fatalf("traversal segment reaches upstream: %q -> %q", p, req.URL.Path)
		}
		if !hasDotDotSegment(p) {
			t.Fatalf("handler guard misses traversal: %q", p)
		}
	}
}

// Encoded traversal ("%2e%2e", "%2f") is decoded into URL.Path by the HTTP
// server before the handler runs, so the decoded-path guard catches it.
// This pins that boundary end to end: raw request target -> extracted path
// -> rejection.
func TestEdge_PreviewPathRejectsEncodedTraversal(t *testing.T) {
	prefix := "/api/projects/p1/preview/"
	for _, target := range []string{
		"/api/projects/p1/preview/..%2f..%2fetc%2fpasswd",
		"/api/projects/p1/preview/%2e%2e/x",
		"/api/projects/p1/preview/a/%2e%2e/%2e%2e/y",
	} {
		req := httptest.NewRequest("GET", target, nil)
		path := strings.TrimPrefix(req.URL.Path, prefix)
		if !hasDotDotSegment(path) {
			t.Fatalf("encoded traversal slips through: %q -> path %q", target, path)
		}
	}
}

func containsDotDot(s string) bool {
	for i := 0; i+2 <= len(s); i++ {
		if s[i] == '.' && s[i+1] == '.' {
			return true
		}
	}
	return false
}

// UTF-8 framing must never stall the terminal bridge: invalid start bytes
// (F5-FF), overlong C0/C1, and empty input must pass through or be empty —
// never be held forever waiting for bytes that complete nothing.
func TestEdge_SplitUTF8NeverStalls(t *testing.T) {
	// Lone invalid/overlong bytes flush immediately: there is no valid
	// completion for them, so holding would stall terminal output.
	for _, in := range []string{"ab\xff", "ab\xf5", "ab\xc0", "ab\xc1", ""} {
		n, hold := splitUTF8([]byte(in))
		if n != len(in) || hold != 0 {
			t.Fatalf("splitUTF8(%q) = (%d,%d), want (%d,0): invalid bytes must flush", in, n, hold, len(in))
		}
	}
	// A lone 3-byte start is legitimately held (its continuation may arrive
	// in the next pty chunk), but bounded to what it already holds.
	if n, hold := splitUTF8([]byte("\xe2")); n != 0 || hold != 1 {
		t.Fatalf("splitUTF8(E2) = (%d,%d), want (0,1)", n, hold)
	}
}

// The navigate tool drives a browser that shares the project's network
// namespace: only loopback http(s) targets are allowed, so the tool cannot
// be used as SSRF against cloud metadata, sibling containers, or the host.
func TestEdge_NavigateAllowsOnlyLoopback(t *testing.T) {
	allowed := []string{
		"http://127.0.0.1:3000",
		"http://127.0.0.1:3000/app",
		"http://localhost:5173/",
		"https://localhost:8443/x",
		"http://[::1]:3000/",
	}
	for _, u := range allowed {
		if !allowedNavigateURL(u) {
			t.Fatalf("loopback target rejected: %s", u)
		}
	}
	denied := []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.8:3000/",
		"http://pcoder-abc:3000/",
		"http://example.com/",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"",
		"http://127.0.0.1.evil.com/",
		"http://[::ffff:127.0.0.1]:3000/",
	}
	for _, u := range denied {
		if allowedNavigateURL(u) {
			t.Fatalf("non-loopback target allowed: %q", u)
		}
	}
}

// Preview asset paths must reject directory climbing, raw or encoded.
func TestEdge_PreviewPathRejectsDotDot(t *testing.T) {
	for _, p := range []string{"../etc/passwd", "a/../../x", "vnc.html"} {
		want := p != "vnc.html"
		if got := hasDotDotSegment(p); got != want {
			t.Fatalf("hasDotDotSegment(%q) = %v, want %v", p, got, want)
		}
	}
	// Single ".." and unparseable escaping are never legit asset names.
	for _, p := range []string{"..", "a/%zz/b"} {
		if !hasDotDotSegment(p) {
			t.Fatalf("hasDotDotSegment(%q) = false, want true", p)
		}
	}
}
