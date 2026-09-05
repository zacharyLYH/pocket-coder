package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"sps/internal/preview"
)

// previewTestWorker/Factory live in httpapi_test.go (shared fixtures).

var privatePreviewEndpoint = preview.Endpoint{CDP: "http://private:9223", Display: "http://private:6080"}

func statusOf(t *testing.T, h http.Handler, cookie *http.Cookie, projectID string) string {
	t.Helper()
	rec := authedGet(t, h, cookie, "/api/projects/"+projectID+"/preview")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad status body %q: %v", rec.Body, err)
	}
	return body.Status
}

// The status endpoint must require auth AND never leak the private worker
// endpoints (CDP/display) that are server-side only.
func TestPreviewStatusRequiresAuthAndNeverReturnsPrivateEndpoint(t *testing.T) {
	d, pinOut := newTestDeps(t)
	m := preview.NewManager(previewTestFactory{ep: privatePreviewEndpoint})
	if _, err := m.Ensure(context.Background(), preview.Config{ProjectID: "p1", ContainerID: "sps-p1"}); err != nil {
		t.Fatal(err)
	}
	d.Preview = m
	h := New(d)
	if rec := get(t, h, "/api/projects/p1/preview"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", rec.Code)
	}
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/p1/preview")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, secret := range []string{"private:9223", "private:6080"} {
		if strings.Contains(body, secret) {
			t.Fatalf("private endpoint %q leaked in response: %s", secret, body)
		}
	}
}

func TestPreviewStatusStoppedWithoutWorker(t *testing.T) {
	d, pinOut := newTestDeps(t)
	d.Preview = preview.NewManager(previewTestFactory{ep: privatePreviewEndpoint})
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	if got := statusOf(t, h, cookie, "ghost"); got != "stopped" {
		t.Fatalf("status = %q, want stopped", got)
	}
}

func TestPreviewStatusReadyWhenCDPAnswers(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string { return evalValue("1") }
	h, cookie := startTestDeps(t, script, "pstatus1")
	if got := statusOf(t, h, cookie, "pstatus1"); got != "ready" {
		t.Fatalf("status = %q, want ready", got)
	}
}

func TestPreviewStatusDegradedWhenCDPDead(t *testing.T) {
	script := &scriptedCDP{navigateResult: `{}`}
	script.eval = func(string) string {
		time.Sleep(30 * time.Second)
		return evalValue("1")
	}
	h, cookie := startTestDeps(t, script, "pstatus2")
	if got := statusOf(t, h, cookie, "pstatus2"); got != "degraded" {
		t.Fatalf("status = %q, want degraded", got)
	}
}
