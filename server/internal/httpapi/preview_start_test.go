package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"pcoder/internal/preview"
)

// endpointWorker/Factory and authedPostCtx live in httpapi_test.go now:
// previewTestFactory{ep} and authedPostCtx.

// scriptedCDP is a fake Chromium: /json/list advertises one page target and
// the websocket answers Page.navigate / Runtime.evaluate from scripts.
type scriptedCDP struct {
	t              *testing.T
	mu             sync.Mutex
	navigateResult string
	eval           func(expr string) string
}

func (f *scriptedCDP) serveList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `[{"type":"page","webSocketDebuggerUrl":"ws://%s/cdp"}]`, r.Host)
}

func (f *scriptedCDP) serveWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	for {
		var msg struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := c.ReadJSON(&msg); err != nil {
			return
		}
		f.mu.Lock()
		var result string
		switch msg.Method {
		case "Page.navigate":
			result = f.navigateResult
		case "Runtime.evaluate":
			expr, _ := msg.Params["expression"].(string)
			result = f.eval(expr)
		default:
			result = `{}`
		}
		f.mu.Unlock()
		if err := c.WriteJSON(map[string]any{"id": msg.ID, "result": json.RawMessage(result)}); err != nil {
			return
		}
	}
}

func evalValue(inner string) string {
	return fmt.Sprintf(`{"result":{"value":%q}}`, inner)
}

func startTestDeps(t *testing.T, script *scriptedCDP, projectID string) (http.Handler, *http.Cookie) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", script.serveList)
	mux.HandleFunc("/cdp", script.serveWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	d, pinOut := newTestDeps(t)
	m := preview.NewManager(previewTestFactory{ep: preview.Endpoint{CDP: srv.URL}})
	if _, err := m.Ensure(context.Background(), preview.Config{ProjectID: projectID, ContainerID: "pcoder-" + projectID}); err != nil {
		t.Fatal(err)
	}
	d.Preview = m
	t.Cleanup(func() { evictCDP(projectID) })
	h := New(d)
	return h, loginCookie(t, h, pinOut)
}

// (authedPostCtx lives in httpapi_test.go; authedPost delegates to it.)

func TestPreviewStartHappy(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string {
		return evalValue(`{"state":"complete","href":"http://127.0.0.1:3000/"}`)
	}
	h, cookie := startTestDeps(t, script, "pstart1")
	rec := authedPost(t, h, cookie, "/api/projects/pstart1/preview/start", `{"port":3000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.OK || body.Status != "ready" {
		t.Fatalf("body = %q err=%v", rec.Body, err)
	}
}

func TestPreviewStartNavigateError(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{"errorText":"net::ERR_CONNECTION_REFUSED"}`}
	script.eval = func(string) string { return evalValue(`{}`) }
	h, cookie := startTestDeps(t, script, "pstart2")
	rec := authedPost(t, h, cookie, "/api/projects/pstart2/preview/start", `{"port":3000}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("start = %d body=%s, want 502 for a refused navigate", rec.Code, rec.Body)
	}
}

func TestPreviewStartErrorPage(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string {
		return evalValue(`{"state":"complete","href":"chrome-error://chromewebdata/"}`)
	}
	h, cookie := startTestDeps(t, script, "pstart3")
	rec := authedPost(t, h, cookie, "/api/projects/pstart3/preview/start", `{"port":3000}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("start = %d body=%s, want 502 for an error page", rec.Code, rec.Body)
	}
}

func TestPreviewStartTimeout(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string {
		return evalValue(`{"state":"loading","href":"http://127.0.0.1:3000/"}`)
	}
	h, cookie := startTestDeps(t, script, "pstart4")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rec := authedPostCtx(t, h, cookie, ctx, "/api/projects/pstart4/preview/start", `{"port":3000}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("start = %d body=%s, want 502 on readiness timeout", rec.Code, rec.Body)
	}
}
