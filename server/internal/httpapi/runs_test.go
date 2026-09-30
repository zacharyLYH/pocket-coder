package httpapi

// Pinning tests for the review fixes: unified run tracker semantics,
// butler runningThreadId exposure, rune-based prompt limits, the shared
// turn-error shape, and the enriched butler.turn audit event.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunTrackerSemantics(t *testing.T) {
	tr := newRunTracker()
	if _, busy := tr.running("a"); busy {
		t.Fatal("fresh tracker reports busy")
	}
	if !tr.take("a", "") {
		t.Fatal("first take must hold")
	}
	if tr.take("a", "t2") {
		t.Fatal("second take must 409")
	}
	// Empty id while taken-before-reserve still counts as busy.
	if tid, busy := tr.running("a"); !busy || tid != "" {
		t.Fatalf("running = %q,%v, want '',true", tid, busy)
	}
	tr.set("a", "t1")
	if tid, busy := tr.running("a"); !busy || tid != "t1" {
		t.Fatalf("running = %q,%v, want 't1',true", tid, busy)
	}
	// Set on an unheld key is a no-op (never resurrect a released slot).
	tr.done("a")
	tr.set("a", "t9")
	if _, busy := tr.running("a"); busy {
		t.Fatal("set after done must stay released")
	}
	// Keys are independent (codemap per-project vs butler global).
	if !tr.take("a", "x") || !tr.take("b", "y") {
		t.Fatal("independent keys must both hold")
	}
}

// The client-visible turn-error contract is one shape: {error} plus
// thread identity when one exists. Reserve failures use it too — no
// bespoke stage field for clients to branch on.
func TestWriteTurnErrShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeTurnErr(rec, http.StatusInternalServerError, "create thread: boom", "", "")
	var bare map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &bare); err != nil {
		t.Fatal(err)
	}
	if bare["error"] != "create thread: boom" || len(bare) != 1 {
		t.Fatalf("reserve failure shape = %v, want error only", bare)
	}
	rec = httptest.NewRecorder()
	writeTurnErr(rec, http.StatusConflict, "busy", "tid", "title")
	var full map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if full["error"] != "busy" || full["threadId"] != "tid" || full["threadTitle"] != "title" || len(full) != 3 {
		t.Fatalf("identified shape = %v, want error+threadId+threadTitle", full)
	}
}

// Prompt limits count runes, not bytes: 2000 emoji (8000 bytes) are a
// legal prompt; 2001 ASCII chars are not.
func TestButlerPromptRuneLimit(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "noted", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	butlerPost(t, h, cookie, `{"prompt":"`+strings.Repeat("x", 2001)+`"}`, http.StatusBadRequest)
	butlerPost(t, h, cookie, `{"prompt":"`+strings.Repeat("🙂", 2001)+`"}`, http.StatusBadRequest)
	// 2000 runes over 2000 bytes would 400 on a byte count; must run.
	rec := butlerPost(t, h, cookie, `{"prompt":"`+strings.Repeat("🙂", 2000)+`"}`, http.StatusOK)
	if last := finalBody(t, rec); last["answer"] != "noted" {
		t.Fatalf("rune prompt final = %v", last)
	}
}

// GET threads reports the in-flight thread as a running row while a turn
// runs, and ready once it completes — the remount path the sheet relies on.
func TestButlerThreadsReportsRunningThread(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	release := make(chan struct{})
	var once sync.Once
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			<-release
			writeCompletion(w, "stop", "done", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- authedPost(t, h, cookie, "/api/butler/turn", `{"prompt":"hi"}`)
	}()
	var tid string
	for i := 0; i < 200; i++ {
		rec := authedGet(t, h, cookie, "/api/butler/threads")
		var body struct {
			Threads []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"threads"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Threads) == 1 && body.Threads[0].Status == "running" {
			tid = body.Threads[0].ID
			break
		}
		select {
		case rec := <-done:
			t.Fatalf("turn finished early: %d", rec.Code)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tid == "" {
		once.Do(func() { close(release) })
		t.Fatal("running thread never appeared in list")
	}
	once.Do(func() { close(release) })
	if rec := <-done; rec.Code != http.StatusOK {
		t.Fatalf("turn: got %d %q, want 200", rec.Code, rec.Body.String())
	}
	rec := authedGet(t, h, cookie, "/api/butler/threads")
	var body struct {
		Threads []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Threads) != 1 || body.Threads[0].Status != "ready" {
		t.Fatalf("rows after completion = %+v, want one ready row", body.Threads)
	}
}

// butler.turn audits the completed turn like codemap.turn does: thread,
// turn, prompt excerpt, and step count — not just the thread id.
func TestButlerTurnAuditEvent(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	_ = md
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "ok", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := butlerPost(t, h, cookie, `{"prompt":"audit me"}`, http.StatusOK)
	last := finalBody(t, rec)
	tid, _ := last["threadId"].(string)
	turnID, _ := last["turnId"].(string)

	evs, err := d.Events.Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type != "butler.turn" {
			continue
		}
		found = true
		if ev.Data["threadId"] != tid || ev.Data["turnId"] != turnID {
			t.Fatalf("audit identity = %v, want %s/%s", ev.Data, tid, turnID)
		}
		if p, _ := ev.Data["prompt"].(string); !strings.Contains(p, "audit me") {
			t.Fatalf("audit prompt = %v, want excerpt", ev.Data)
		}
		if n, _ := ev.Data["steps"].(float64); n != 0 {
			t.Fatalf("audit steps = %v, want 0 on a tools-free turn", ev.Data)
		}
	}
	if !found {
		t.Fatal("no butler.turn audit event")
	}
}
