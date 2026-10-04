package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"pcoder/internal/preview"
	"pcoder/internal/session"
	dockermocks "pcoder/mocks/docker"
)

func TestPreviewProxyStripsTokenQuery(t *testing.T) {
	target, err := url.Parse("http://10.0.0.8:6080")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newPreviewProxy(target, "websockify")
	req := httptest.NewRequest("GET", "/api/projects/p1/preview/websockify?token=x&autoconnect=true", nil)
	req.AddCookie(&http.Cookie{Name: "pcoder_session", Value: "jwt"})
	req.AddCookie(&http.Cookie{Name: "pcoder_preview_p1", Value: "tok"})
	proxy.Director(req)
	if req.URL.String() != "http://10.0.0.8:6080/websockify?autoconnect=true" {
		t.Fatalf("forwarded URL = %s", req.URL)
	}
	if req.Host != "10.0.0.8:6080" {
		t.Fatalf("forwarded host = %q", req.Host)
	}
	// The sidecar needs neither the session JWT nor the capability: both
	// must stop at the Go gate, never ride upstream to websockify logs.
	if ck := req.Header.Get("Cookie"); ck != "" {
		t.Fatalf("Cookie forwarded upstream: %q", ck)
	}
}

func previewTokenFor(t *testing.T, h http.Handler, cookie *http.Cookie, projectID string) string {
	t.Helper()
	rec := authedGet(t, h, cookie, "/api/projects/"+url.PathEscape(projectID)+"/preview")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Token == "" {
		t.Fatalf("status body = %q err=%v", rec.Body, err)
	}
	return body.Token
}

func authedGetToken(t *testing.T, h http.Handler, cookie *http.Cookie, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(cookie)
	if token != "" {
		req.Header.Set("X-Preview-Token", token)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// tokenDeps wires Preview + Sessions(nil-safe) and a fake CDP + display
// upstream, returning the handler, cookie, token, and display server URL.
func tokenDeps(t *testing.T, script *scriptedCDP, projectID string) (http.Handler, *http.Cookie, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", script.serveList)
	mux.HandleFunc("/cdp", script.serveWS)
	dispMux := http.NewServeMux()
	dispMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "" {
			t.Errorf("token leaked upstream: %s", r.URL)
		}
		w.WriteHeader(http.StatusOK)
	})
	disp := httptest.NewServer(dispMux)
	t.Cleanup(disp.Close)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	d, pinOut := newTestDeps(t)
	m := preview.NewManager(previewTestFactory{ep: preview.Endpoint{CDP: srv.URL, Display: disp.URL}})
	d.Sessions = session.New(dockermocks.NewMockClient(t))
	if _, err := m.Ensure(t.Context(), preview.Config{ProjectID: projectID, ContainerID: "pcoder-" + projectID}); err != nil {
		t.Fatal(err)
	}
	d.Preview = m
	t.Cleanup(func() { evictCDP(projectID) })
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	tok := previewTokenFor(t, h, cookie, projectID)
	return h, cookie, tok
}

func TestPreviewSurfaceRequiresToken(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string { return evalValue(`{"state":"complete","href":"http://127.0.0.1:3000/"}`) }
	h, cookie, tok := tokenDeps(t, script, "ptok-surface")
	base := "/api/projects/ptok-surface/preview/vnc_lite.html"
	if rec := authedGetToken(t, h, cookie, http.MethodGet, base, ""); rec.Code != http.StatusNotFound || rec.Body.String() != "{\"error\":\"not found\"}\n" {
		t.Fatalf("missing token = %d body=%s", rec.Code, rec.Body)
	}
	if rec := authedGetToken(t, h, cookie, http.MethodGet, base, "wrong"); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong token = %d body=%s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodGet, base+"?token="+tok, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token surface = %d body=%s", rec.Code, rec.Body)
	}
}

// The entry document loads with ?token=, but noVNC's relative module imports
// (./core/rfb.js, …) drop the query when they resolve against the document
// URL. Without a cookie hand-off every asset 404s and the canvas never
// renders (docs/preview-surface-token-gate.md), so: tokenless assets stay
// gated, the entry document hands the capability over as a cookie, and that
// cookie then admits the assets it was minted for.
func TestPreviewSurfaceCookieCarriesAssetImports(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string { return evalValue(`{"state":"complete","href":"http://127.0.0.1:3000/"}`) }
	h, cookie, tok := tokenDeps(t, script, "ptok-cookie")
	base := "/api/projects/ptok-cookie/preview/"

	// Nothing to carry the capability yet: the gate still 404s.
	if rec := authedGetToken(t, h, cookie, http.MethodGet, base+"core/rfb.js", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("tokenless asset = %d body=%s", rec.Code, rec.Body)
	}

	// The entry document, opened with a valid ?token=, answers 200 and hands
	// the same capability to a cookie the asset requests can inherit.
	entry := httptest.NewRequest(http.MethodGet, base+"vnc_lite.html?token="+tok, nil)
	entry.AddCookie(cookie)
	erec := httptest.NewRecorder()
	h.ServeHTTP(erec, entry)
	if erec.Code != http.StatusOK {
		t.Fatalf("entry = %d body=%s", erec.Code, erec.Body)
	}
	var capability *http.Cookie
	for _, c := range erec.Result().Cookies() {
		if c.Name == previewSurfaceCookieName("ptok-cookie") {
			capability = c
		}
	}
	if capability == nil || capability.Value != tok {
		t.Fatalf("capability cookie = %+v, want value %q", capability, tok)
	}
	if !capability.HttpOnly || capability.Path != surfaceCookiePath {
		t.Fatalf("capability cookie attrs = httpOnly=%v path=%q, want httpOnly over %q", capability.HttpOnly, capability.Path, surfaceCookiePath)
	}

	// A relative module import: same request, no query token, cookie only.
	asset := httptest.NewRequest(http.MethodGet, base+"core/rfb.js", nil)
	asset.AddCookie(cookie)
	asset.AddCookie(capability)
	arec := httptest.NewRecorder()
	h.ServeHTTP(arec, asset)
	if arec.Code != http.StatusOK {
		t.Fatalf("cookie-carried asset = %d body=%s", arec.Code, arec.Body)
	}

	// An explicit token still wins over a live cookie: a wrong or rotated one
	// must 404 rather than be papered over by the cookie fallback.
	stale := httptest.NewRequest(http.MethodGet, base+"core/rfb.js?token=wrong", nil)
	stale.AddCookie(cookie)
	stale.AddCookie(capability)
	srec := httptest.NewRecorder()
	h.ServeHTTP(srec, stale)
	if srec.Code != http.StatusNotFound {
		t.Fatalf("wrong query token + live cookie = %d body=%s", srec.Code, srec.Body)
	}
}

// The capability cookie follows the session cookie's Secure rule: marked
// Secure when the browser arrived over TLS (direct or via the Tunnel's
// X-Forwarded-Proto), unset on plain HTTP so dev keeps working.
func TestPreviewSurfaceCookieSecureMirrorsOriginScheme(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string { return evalValue(`{"state":"complete","href":"http://127.0.0.1:3000/"}`) }
	for _, tc := range []struct {
		name      string
		forwarded string
		want      bool
	}{
		{"plain http dev", "", false},
		{"behind https proxy", "https", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cookie, tok := tokenDeps(t, script, "ptok-secure")
			entry := httptest.NewRequest(http.MethodGet,
				"/api/projects/ptok-secure/preview/vnc_lite.html?token="+tok, nil)
			entry.AddCookie(cookie)
			if tc.forwarded != "" {
				entry.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			erec := httptest.NewRecorder()
			h.ServeHTTP(erec, entry)
			if erec.Code != http.StatusOK {
				t.Fatalf("entry = %d body=%s", erec.Code, erec.Body)
			}
			var capability *http.Cookie
			for _, c := range erec.Result().Cookies() {
				if c.Name == previewSurfaceCookieName("ptok-secure") {
					capability = c
				}
			}
			if capability == nil {
				t.Fatal("no capability cookie set")
			}
			if capability.Secure != tc.want {
				t.Fatalf("capability Secure = %v, want %v", capability.Secure, tc.want)
			}
		})
	}
}

func TestPreviewHeartbeatTouchesAndRejects(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string { return evalValue(`{"state":"complete","href":"http://127.0.0.1:3000/"}`) }
	h, cookie, tok := tokenDeps(t, script, "ptok-beat")
	rec := authedGetToken(t, h, cookie, http.MethodPost, "/api/projects/ptok-beat/preview/heartbeat", tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d body=%s", rec.Code, rec.Body)
	}
	if rec := authedGetToken(t, h, cookie, http.MethodPost, "/api/projects/ptok-beat/preview/heartbeat", "bad"); rec.Code != http.StatusNotFound {
		t.Fatalf("bad heartbeat = %d body=%s", rec.Code, rec.Body)
	}
}

func TestPreviewToolsGateTokensPortsUngated(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string {
		return evalValue(`{"state":"complete","href":"http://127.0.0.1:3000/"}`)
	}
	h, cookie, tok := tokenDeps(t, script, "ptok-tools")
	tool := "/api/projects/ptok-tools/preview/tools/inspect"
	if rec := authedGetToken(t, h, cookie, http.MethodGet, tool, ""); rec.Code != http.StatusNotFound || rec.Body.String() != "{\"error\":\"not found\"}\n" {
		t.Fatalf("missing token = %d body=%s", rec.Code, rec.Body)
	}
	if rec := authedGetToken(t, h, cookie, http.MethodGet, tool, "wrong"); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong token = %d body=%s", rec.Code, rec.Body)
	}
	if rec := authedGetToken(t, h, cookie, http.MethodGet, tool, tok); rec.Code != http.StatusOK {
		t.Fatalf("valid token = %d body=%s", rec.Code, rec.Body)
	}
}

func TestPreviewStartAndStatusReturnTokenCloseTokenless(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string {
		return evalValue(`{"state":"complete","href":"http://127.0.0.1:3000/"}`)
	}
	h, cookie, tok := tokenDeps(t, script, "ptok-lifecycle")
	rec := authedPost(t, h, cookie, "/api/projects/ptok-lifecycle/preview/start", `{"port":3000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d body=%s", rec.Code, rec.Body)
	}
	var started struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.Token != tok {
		t.Fatalf("start body = %q want token %q err=%v", rec.Body, tok, err)
	}
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/ptok-lifecycle/preview")
	if rec.Code != http.StatusOK {
		t.Fatalf("close = %d body=%s", rec.Code, rec.Body)
	}
}
