package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"pcoder/internal/projectlog"
)

func TestProjectLogsRoundTrip(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// empty project tails empty (not null) — the Logs tab iterates directly
	rec := authedGet(t, h, cookie, "/api/projects/abc/logs")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty logs: got %d, want 200", rec.Code)
	}
	var empty struct {
		Logs []projectlog.Entry `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil || empty.Logs == nil || len(empty.Logs) != 0 {
		t.Fatalf("empty logs body = %q, want empty list", rec.Body.String())
	}

	// append via the dedicated write API
	rec = authedPost(t, h, cookie, "/api/projects/abc/logs", `{"type":"preview.start","message":"Preview started on :3000","data":{"port":3000}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("append: got %d %q, want 201", rec.Code, rec.Body.String())
	}

	// tail shows it
	rec = authedGet(t, h, cookie, "/api/projects/abc/logs")
	var body struct {
		Logs []projectlog.Entry `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Logs) != 1 || body.Logs[0].ID != 1 || body.Logs[0].Message != "Preview started on :3000" {
		t.Fatalf("logs = %+v, want the appended entry", body.Logs)
	}

	// cursor: nothing newer
	rec = authedGet(t, h, cookie, "/api/projects/abc/logs?after=1")
	var cursor struct {
		Logs []projectlog.Entry `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cursor); err != nil || len(cursor.Logs) != 0 {
		t.Fatalf("after=1 body = %q, want empty list", rec.Body.String())
	}

	// projects are isolated
	rec = authedGet(t, h, cookie, "/api/projects/def/logs")
	var other struct {
		Logs []projectlog.Entry `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &other); err != nil || len(other.Logs) != 0 {
		t.Fatalf("other project body = %q, want empty list", rec.Body.String())
	}

	// validation
	if rec := authedPost(t, h, cookie, "/api/projects/abc/logs", `{"type":"","message":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty type: got %d, want 400", rec.Code)
	}
	if rec := authedPost(t, h, cookie, "/api/projects/abc/logs", `{"type":"t","message":"  "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank message: got %d, want 400", rec.Code)
	}
	if rec := authedGet(t, h, cookie, "/api/projects/abc/logs?after=nope"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad after: got %d, want 400", rec.Code)
	}

	// unauthed
	if rec := get(t, h, "/api/projects/abc/logs"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed read: got %d, want 401", rec.Code)
	}
	if rec := post(t, h, "/api/projects/abc/logs", `{"type":"t","message":"m"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed append: got %d, want 401", rec.Code)
	}
}
