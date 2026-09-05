package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"sps/internal/preview"
)

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

func TestPreviewStatusStoppedWithoutWorker(t *testing.T) {
	d, pinOut := newTestDeps(t)
	d.Preview = preview.NewManager(previewTestFactory{})
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
