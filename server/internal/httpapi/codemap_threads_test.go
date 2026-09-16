package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pcoder/internal/codemapthreads"
)

func authedMethodBody(t *testing.T, h http.Handler, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	return rec
}

// Thread CRUD: create → list → get → rename → second turn appends →
// delete. Each chat is its own file; the list is how past chats stay
// discoverable.
func TestCodemapThreadsCRUD(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// Create.
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap/threads", `{"title":"auth dive"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d %q, want 201", rec.Code, rec.Body)
	}
	var created struct {
		Thread struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Thread.ID == "" || created.Thread.Title != "auth dive" {
		t.Fatalf("created = %+v", created)
	}
	tid := created.Thread.ID

	// Unknown thread 404s.
	rec = authedGet(t, h, cookie, "/api/projects/abc/codemap/threads/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown thread: got %d, want 404", rec.Code)
	}

	// Rename validation.
	rec = authedMethodBody(t, h, cookie, http.MethodPatch, "/api/projects/abc/codemap/threads/"+tid, `{"title":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty rename: got %d, want 400", rec.Code)
	}
	rec = authedMethodBody(t, h, cookie, http.MethodPatch, "/api/projects/abc/codemap/threads/"+tid, `{"title":"login flow"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: got %d %q, want 200", rec.Code, rec.Body)
	}

	// List shows the chat.
	rec = authedGet(t, h, cookie, "/api/projects/abc/codemap/threads")
	var listed struct {
		Threads []struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			TurnCount int    `json:"turnCount"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Threads) != 1 || listed.Threads[0].ID != tid {
		t.Fatalf("listed = %+v", listed)
	}

	// A turn posted with threadId lands in that thread file.
	d2 := d
	_ = d2
	th, err := d.Codemaps.AppendTurn("abc", tid, codemapthreads.Turn{Prompt: "where is login?"})
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) != 1 || th.Title == "New chat" {
		t.Fatalf("title should derive from first prompt, got %q", th.Title)
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

// Posting a turn without a threadId auto-creates the chat so the first
// message never needs a two-step dance.
func TestCodemapPostAutoCreatesThread(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	mockHydrate(md, "func main() {\n")
	f := newFakeModel(t,
		func(w http.ResponseWriter, _ map[string]any) {
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
		t.Fatalf("auto-create missing threadId: %s", rec.Body)
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
		t.Fatalf("listed = %+v, want the auto-created thread", listed)
	}
}
