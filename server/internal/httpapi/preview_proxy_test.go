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
	proxy.Director(req)
	if req.URL.String() != "http://10.0.0.8:6080/websockify?autoconnect=true" {
		t.Fatalf("forwarded URL = %s", req.URL)
	}
	if req.Host != "10.0.0.8:6080" {
		t.Fatalf("forwarded host = %q", req.Host)
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
