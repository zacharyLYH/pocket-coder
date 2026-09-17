package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/codemapthreads"
	"pcoder/internal/docker"
)

// seedFailedLastTurn builds a thread with one good turn + one failed
// placeholder last turn, returning the thread id and the failed turn id.
func seedFailedLastTurn(t *testing.T, d Deps) (string, string) {
	t.Helper()
	tid, firstTurn, err := d.Codemaps.ReserveNewThread("abc", "first?", "s1")
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := json.Marshal([]map[string]any{{"title": "A", "summary": "s", "refs": []any{}}})
	if err := d.Codemaps.CompleteTurn("abc", tid, 1, codemapthreads.Turn{
		TurnID: firstTurn, SHA: "s1", Prompt: "first?", Sections: sec,
		Tools: json.RawMessage(`[]`), Time: time.Now(),
	}, []byte(`{"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	n, failedTurn, err := d.Codemaps.ReserveFollowup("abc", tid, "second?", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Codemaps.FailTurn("abc", tid, n, codemapthreads.Turn{
		TurnID: failedTurn, SHA: "s1", Prompt: "second?", Time: time.Now(),
	}, "boom", []byte(`{"error":"boom","events":[]}`)); err != nil {
		t.Fatal(err)
	}
	return tid, failedTurn
}

// Retry reruns the LAST failed turn only: same turnId/path, fresh
// time+sha, context 1..N-1, full rerun. 200 reloads+opens.
func TestCodemapRetrySuccess(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	mockHydrate(md, "func main() {\n")
	var mu sync.Mutex
	var bodies []map[string]any
	remember := func(w http.ResponseWriter, body map[string]any) {
		mu.Lock()
		raw, _ := json.Marshal(body)
		var cp map[string]any
		_ = json.Unmarshal(raw, &cp)
		bodies = append(bodies, cp)
		mu.Unlock()
		writeCompletion(w, "stop", finalCodemapJSON(), nil)
	}
	f := newFakeModel(t, remember, remember, remember)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	tid, failedTurn := seedFailedLastTurn(t, d)

	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap/threads/"+tid+"/retry", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: got %d %q, want 200", rec.Code, rec.Body)
	}
	var body struct {
		TurnID      string `json:"turnId"`
		ThreadID    string `json:"threadId"`
		ThreadTitle string `json:"threadTitle"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TurnID != failedTurn || body.ThreadID != tid {
		t.Fatalf("retry identity = %+v, want turn %q thread %q", body, failedTurn, tid)
	}
	// No N+1: still two turns, last one now successful with the same id.
	th, err := d.Codemaps.Get("abc", tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) != 2 {
		t.Fatalf("turns = %d, want 2 (rewrite, not append)", len(th.Turns))
	}
	if th.Turns[1].TurnID != failedTurn || th.Turns[1].Error != nil {
		t.Fatalf("retried turn = %+v, want same id, no error", th.Turns[1])
	}
	if len(th.Turns[1].Sections) == 0 {
		t.Fatalf("retried turn has no sections")
	}
	// Context was turns 1..N-1 only: history holds turn 1 (prompt +
	// transcript), then the retried prompt. The failed attempt contributes
	// no assistant/tool replay of its own.
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatalf("no model calls during retry")
	}
	msgs, _ := bodies[0]["messages"].([]any)
	var users []string
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] == "user" {
			if c, _ := mm["content"].(string); c != "" && !strings.Contains(c, "ground your answer") {
				users = append(users, c)
			}
		}
	}
	if len(users) != 2 || users[0] != "first?" || users[1] != "second?" {
		t.Fatalf("retry history users = %q, want [first? second?]", users)
	}
	raw0, _ := json.Marshal(bodies[0])
	if strings.Contains(string(raw0), "tool_calls") {
		t.Fatalf("retry history must not replay tool calls: %s", raw0)
	}
}

// Retry guards: 409 on successful last turn, 409 on non-terminal failed
// turn, 404 on unknown thread.
func TestCodemapRetryGuards(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockEnsure(md)
	f := newFakeModel(t)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// Successful last turn: never retried.
	tid, _, err := d.Codemaps.ReserveNewThread("abc", "first?", "s")
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := json.Marshal([]map[string]any{{"title": "A"}})
	if err := d.Codemaps.CompleteTurn("abc", tid, 1, codemapthreads.Turn{
		TurnID: "t1", Prompt: "first?", Sections: sec, Time: time.Now(),
	}, nil); err != nil {
		t.Fatal(err)
	}
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap/threads/"+tid+"/retry", ``)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry success: got %d %q, want 409", rec.Code, rec.Body)
	}

	// Non-terminal failed turn (a good turn follows it): never retried.
	tid2, _, err := d.Codemaps.ReserveNewThread("abc", "first?", "s")
	if err != nil {
		t.Fatal(err)
	}
	n, _, err := d.Codemaps.ReserveFollowup("abc", tid2, "bad?", "s")
	if err != nil {
		t.Fatal(err)
	}
	// Complete turn 1 first so the thread is well-formed, fail turn 2,
	// then append a successful turn 3 via CompleteTurn on a new reserve.
	if err := d.Codemaps.CompleteTurn("abc", tid2, 1, codemapthreads.Turn{TurnID: "t1", Prompt: "first?", Sections: sec, Time: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	th, _ := d.Codemaps.Get("abc", tid2)
	_ = th
	// Fail turn n (== 2).
	turn2, _ := d.Codemaps.Get("abc", tid2)
	_ = turn2
	if err := d.Codemaps.FailTurn("abc", tid2, n, codemapthreads.Turn{TurnID: "t2", Prompt: "bad?", Time: time.Now()}, "boom", nil); err != nil {
		t.Fatal(err)
	}
	n3, _, err := d.Codemaps.ReserveFollowup("abc", tid2, "third?", "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Codemaps.CompleteTurn("abc", tid2, n3, codemapthreads.Turn{TurnID: "t3", Prompt: "third?", Sections: sec, Time: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	// Last turn is successful → 409 (the failed turn 2 is non-terminal).
	rec = authedPost(t, h, cookie, "/api/projects/abc/codemap/threads/"+tid2+"/retry", ``)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry non-terminal: got %d %q, want 409", rec.Code, rec.Body)
	}

	// Unknown thread → 404 before burning a model call.
	rec = authedPost(t, h, cookie, "/api/projects/abc/codemap/threads/abcdef0123456789/retry", ``)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("retry unknown: got %d %q, want 404", rec.Code, rec.Body)
	}
	if f.calls != 0 {
		t.Fatalf("model calls = %d, want 0", f.calls)
	}
}

// Retry contends with POST on the same project-wide busy lock.
func TestCodemapRetryBusy(t *testing.T) {
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
	tid, _ := seedFailedLastTurn(t, d)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && (strings.Contains(argv[2], "grep -rn") || strings.Contains(argv[2], "sed -n"))
		}), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "x\n"}, nil).Maybe()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- authedPost(t, h, cookie, "/api/projects/abc/codemap",
			`{"prompt":"follow up","threadId":"`+tid+`"}`)
	}()
	for i := 0; i < 200; i++ {
		if _, busy := codemapRunning("abc"); busy {
			break
		}
		select {
		case rec := <-done:
			t.Fatalf("run finished early: %d", rec.Code)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap/threads/"+tid+"/retry", ``)
	if rec.Code != http.StatusConflict {
		once.Do(func() { close(release) })
		t.Fatalf("retry during run: got %d %q, want 409", rec.Code, rec.Body)
	}
	once.Do(func() { close(release) })
	if first := <-done; first.Code != http.StatusBadGateway && first.Code != http.StatusOK {
		t.Fatalf("run: got %d %q", first.Code, first.Body)
	}
}

// Rerun failure answers 502/504 WITH threadId so FE adopts the id and
// opens the failed placeholder.
func TestCodemapRetryFailureKeepsThread(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	nullChoices := func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen-test","object":"chat.completion","created":0,"model":"","choices":null}`))
	}
	f := newFakeModel(t, nullChoices, nullChoices, nullChoices)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	tid, failedTurn := seedFailedLastTurn(t, d)
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap/threads/"+tid+"/retry", ``)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("retry fail: got %d %q, want 502", rec.Code, rec.Body)
	}
	var body struct {
		ThreadID    string `json:"threadId"`
		ThreadTitle string `json:"threadTitle"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ThreadID != tid {
		t.Fatalf("retry failure dropped threadId: %s", rec.Body)
	}
	th, err := d.Codemaps.Get("abc", tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) != 2 || th.Turns[1].TurnID != failedTurn || th.Turns[1].Error == nil {
		t.Fatalf("rerun failure not a failed placeholder: %+v", th.Turns)
	}
}

// Status-code pins: 404 unknown thread on POST, 409 busy is covered
// elsewhere; pre-manifest failures are store-level (ReserveNewThread
// always succeeds on disk in tests) so this pins the unknown-thread
// branch returning 404 with no model call.
func TestCodemapPostUnknownThread(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockEnsure(md)
	f := newFakeModel(t)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"hi","threadId":"abcdef0123456789"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown thread: got %d %q, want 404", rec.Code, rec.Body)
	}
	if f.calls != 0 {
		t.Fatalf("model calls = %d, want 0", f.calls)
	}
}
