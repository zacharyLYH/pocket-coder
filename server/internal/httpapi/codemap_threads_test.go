package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func authedMethodBody(t *testing.T, h http.Handler, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	return rec
}

// Thread routes minus create/rename (both deleted in v2): threads are
// created implicitly by POST /codemap, titles are immutable. This pins
// get/list/delete, the runningThreadId surface, and the gone routes.
func TestCodemapThreadsGetListDelete(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// Seed a thread via the store (POST /codemap would need a model).
	tid, _, err := d.Codemaps.ReserveNewThread("abc", "where is login?", "s")
	if err != nil {
		t.Fatal(err)
	}

	// Unknown thread 404s.
	rec := authedGet(t, h, cookie, "/api/projects/abc/codemap/threads/abcdef0123456789")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown thread: got %d %q, want 404", rec.Code, rec.Body.String())
	}

	// Get returns the folder-backed thread; turns carry error, never
	// extractorOutput.
	rec = authedGet(t, h, cookie, "/api/projects/abc/codemap/threads/"+tid)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d %q, want 200", rec.Code, rec.Body)
	}
	var got struct {
		Thread struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Turns []struct {
				Prompt string  `json:"prompt"`
				Error  *string `json:"error"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Thread.ID != tid || got.Thread.Title != "where is login?" {
		t.Fatalf("thread = %+v", got.Thread)
	}
	if len(got.Thread.Turns) != 1 || got.Thread.Turns[0].Error != nil {
		t.Fatalf("placeholder error must be null: %+v", got.Thread.Turns)
	}
	if strings.Contains(rec.Body.String(), "extractorOutput") {
		t.Fatalf("threadJSON must not carry extractorOutput: %s", rec.Body)
	}

	// List shows the chat with runningThreadId null when idle.
	rec = authedGet(t, h, cookie, "/api/projects/abc/codemap/threads")
	var listed struct {
		Threads []struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			TurnCount int    `json:"turnCount"`
		} `json:"threads"`
		RunningThreadId *string `json:"runningThreadId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Threads) != 1 || listed.Threads[0].ID != tid {
		t.Fatalf("listed = %+v", listed)
	}
	if listed.RunningThreadId != nil {
		t.Fatalf("idle runningThreadId = %q, want null", *listed.RunningThreadId)
	}

	// Deleted routes: create (POST) and rename (PATCH) are gone.
	rec = authedPost(t, h, cookie, "/api/projects/abc/codemap/threads", `{"title":"x"}`)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("deleted create route: got %d, want 405/404", rec.Code)
	}
	rec = authedMethodBody(t, h, cookie, http.MethodPatch, "/api/projects/abc/codemap/threads/"+tid, `{"title":"y"}`)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("deleted rename route: got %d, want 405/404", rec.Code)
	}

	// Delete is idempotent.
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/abc/codemap/threads/"+tid)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: got %d %q, want 200", rec.Code, rec.Body)
	}
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/abc/codemap/threads/"+tid)
	if rec.Code != http.StatusOK {
		t.Fatalf("second delete: got %d, want 200", rec.Code)
	}
	rec = authedGet(t, h, cookie, "/api/projects/abc/codemap/threads")
	listed.Threads = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Threads) != 0 {
		t.Fatalf("after delete listed = %+v", listed)
	}
}

// Posting a turn without a threadId implicitly creates the folder chat
// (all 200s, no 201 anywhere).
func TestCodemapPostAutoCreatesThread(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	mockHydrate(md, "func main() {\n")
	f := newFakeModel(t, respondCodemap, respondCodemap, respondCodemap)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"where is main?"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("codemap: got %d %q, want 200", rec.Code, rec.Body)
	}
	var body struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ThreadID == "" {
		t.Fatalf("implicit create missing threadId: %s", rec.Body)
	}
	rec = authedGet(t, h, cookie, "/api/projects/abc/codemap/threads")
	var listed struct {
		Threads []struct {
			ID string `json:"id"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Threads) != 1 || listed.Threads[0].ID != body.ThreadID {
		t.Fatalf("listed = %+v, want the implicit thread", listed)
	}
}

// runningThreadId is set while a POST is in flight, null when idle.
func TestCodemapRunningThreadId(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	mockHydrate(md, "func main() {\n")
	release := make(chan struct{})
	var once sync.Once
	f := newFakeModel(t,
		func(w http.ResponseWriter, _ map[string]any) {
			<-release
			writeCompletion(w, "stop", finalCodemapJSON(), nil)
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", finalCodemapJSON(), nil)
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", finalCodemapJSON(), nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"where is main?"}`)
	}()
	// Wait until the run holds the slot, then read runningThreadId off
	// GET threads (no new endpoint).
	var running string
	for i := 0; i < 200; i++ {
		rec := authedGet(t, h, cookie, "/api/projects/abc/codemap/threads")
		var list struct {
			RunningThreadId *string `json:"runningThreadId"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &list)
		if list.RunningThreadId != nil && *list.RunningThreadId != "" {
			running = *list.RunningThreadId
			break
		}
		select {
		case rec := <-done:
			t.Fatalf("run finished early: %d", rec.Code)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if running == "" {
		once.Do(func() { close(release) })
		t.Fatalf("runningThreadId never set during POST")
	}
	once.Do(func() { close(release) })
	first := <-done
	if first.Code != http.StatusOK {
		t.Fatalf("run: got %d %q, want 200", first.Code, first.Body)
	}
	var body struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &body)
	if body.ThreadID != running {
		t.Fatalf("runningThreadId = %q, want the new thread %q", running, body.ThreadID)
	}
	// Idle again: null.
	rec := authedGet(t, h, cookie, "/api/projects/abc/codemap/threads")
	var idle struct {
		RunningThreadId *string `json:"runningThreadId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &idle); err != nil {
		t.Fatal(err)
	}
	if idle.RunningThreadId != nil {
		t.Fatalf("idle runningThreadId = %q, want null", *idle.RunningThreadId)
	}
}
