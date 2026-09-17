package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/codemapthreads"
	"pcoder/internal/docker"
)

// messagesOf extracts the messages array from a captured chat-completions body.
func messagesOf(body map[string]any) []any {
	msgs, _ := body["messages"].([]any)
	return msgs
}

func messageText(m any) string {
	mm, _ := m.(map[string]any)
	role, _ := mm["role"].(string)
	if role == "tool" {
		c, _ := mm["content"].(string)
		return "tool:" + c
	}
	switch c := mm["content"].(type) {
	case string:
		return role + ":" + c
	case []any:
		var sb strings.Builder
		sb.WriteString(role + ":")
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok {
				if t, _ := pm["text"].(string); t != "" {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	default:
		return role + ":"
	}
}

func bodiesContain(bodies []map[string]any, sub string) bool {
	for _, b := range bodies {
		for _, m := range messagesOf(b) {
			if strings.Contains(messageText(m), sub) {
				return true
			}
		}
		raw, _ := json.Marshal(b)
		if strings.Contains(string(raw), sub) {
			return true
		}
	}
	return false
}

// Follow-up replays the persisted thread: prior prompt, reconstructed
// tool_calls + tool results with outputs, prior sections, then the new
// prompt. Client-supplied history is ignored (unknown field).
func TestCodemapFollowupReplaysThread(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	mockHydrate(md, "func main() {\n")
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && strings.Contains(argv[2], "grep -rn")
		}), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "main.go:10:func main() {\n"}, nil)
	var mu sync.Mutex
	var seen []map[string]any
	capture := func(_ http.ResponseWriter, body map[string]any) {
		mu.Lock()
		raw, _ := json.Marshal(body)
		var cp map[string]any
		_ = json.Unmarshal(raw, &cp)
		seen = append(seen, cp)
		mu.Unlock()
	}
	f := newFakeModel(t,
		func(w http.ResponseWriter, body map[string]any) {
			capture(w, body)
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("c1", "search_code", `{"pattern":"main"}`)})
		},
		func(w http.ResponseWriter, body map[string]any) {
			capture(w, body)
			writeCompletion(w, "stop", finalCodemapJSON(), nil)
		},
		// Format call for turn 1: schema on, tools off.
		func(w http.ResponseWriter, body map[string]any) {
			capture(w, body)
			writeCompletion(w, "stop", finalCodemapJSON(), nil)
		},
		func(w http.ResponseWriter, body map[string]any) {
			capture(w, body)
			writeCompletion(w, "stop", `{"sections":[{"title":"Follow","summary":"Tests live nearby.","refs":[]}]}`+"\n", nil)
		},
		// Grounding nudge for turn 2 (its loop answered with no tools).
		func(w http.ResponseWriter, body map[string]any) {
			capture(w, body)
			writeCompletion(w, "stop", `{"sections":[{"title":"Follow","summary":"Tests live nearby.","refs":[]}]}`+"\n", nil)
		},
		// Format call for turn 2.
		func(w http.ResponseWriter, body map[string]any) {
			capture(w, body)
			writeCompletion(w, "stop", `{"sections":[{"title":"Follow","summary":"Tests live nearby.","refs":[]}]}`+"\n", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"where is main?"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first turn: got %d %q, want 200", rec.Code, rec.Body)
	}
	var first struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.ThreadID == "" {
		t.Fatalf("missing threadId: %s", rec.Body)
	}

	// Second turn needs its own HEAD lookup; the mock tolerates repeats
	// but re-arm explicitly for clarity.
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	bogus := `{"prompt":"and the tests?","threadId":` + strconv.Quote(first.ThreadID) + `,"history":[{"role":"user","content":"BOGUS CLIENT HISTORY"}]}`
	rec = authedPost(t, h, cookie, "/api/projects/abc/codemap", bogus)
	if rec.Code != http.StatusOK {
		t.Fatalf("follow-up: got %d %q, want 200", rec.Code, rec.Body)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 6 {
		t.Fatalf("model calls = %d, want 6 (turn1: 2 loop + format, turn2: loop + nudge-loop + format)", len(seen))
	}
	// Loop calls carry no schema; format calls do. The follow-up loop
	// call is index 3 (turn1: 0 loop, 1 loop-final, 2 format).
	for i, wantSchema := range []bool{false, false, true, false, false, true} {
		_, has := seen[i]["response_format"]
		if has != wantSchema {
			t.Fatalf("call %d schema present = %v, want %v", i, has, wantSchema)
		}
	}
	follow := seen[3]
	var flat []string
	for _, m := range messagesOf(follow) {
		flat = append(flat, messageText(m))
	}
	joined := strings.Join(flat, "\n")
	for _, want := range []string{"where is main?", "func main()", "and the tests?"} {
		if !strings.Contains(joined, want) && !bodiesContain([]map[string]any{follow}, want) {
			t.Fatalf("follow-up context missing %q:\n%s", want, joined)
		}
	}
	if bodiesContain([]map[string]any{follow}, "BOGUS CLIENT HISTORY") {
		t.Fatalf("client history leaked into model request:\n%s", joined)
	}
	// Reconstructed tool round-trip: assistant tool_calls + tool result.
	raw, _ := json.Marshal(follow)
	if !strings.Contains(string(raw), "tool_calls") || !strings.Contains(string(raw), "\"role\":\"tool\"") {
		t.Fatalf("follow-up missing replayed tool_calls/tool messages: %s", string(raw))
	}
	// Prior answer sections replay as assistant content.
	if !strings.Contains(string(raw), "Auth") {
		t.Fatalf("prior sections missing from follow-up: %s", string(raw))
	}
}

// Fresh chats send only the current prompt: no user/tool history.
// The loop call carries no schema; the format call does.
func TestCodemapFreshChatHasNoHistory(t *testing.T) {
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
	// Loop final, grounding-nudge loop (zero tool rounds), format.
	f := newFakeModel(t, remember, remember, remember)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"where is main?"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("codemap: got %d %q, want 200", rec.Code, rec.Body)
	}
	if len(bodies) != 3 {
		t.Fatalf("model calls = %d, want 3 (loop + nudge-loop + format)", len(bodies))
	}
	for i := 0; i < 2; i++ {
		if _, has := bodies[i]["response_format"]; has {
			t.Fatalf("loop call %d must not carry json_schema", i)
		}
	}
	if _, has := bodies[2]["response_format"]; !has {
		t.Fatalf("format call must carry json_schema")
	}
	msgs := messagesOf(bodies[0])
	// developer system + single user prompt.
	if len(msgs) != 2 {
		raw, _ := json.Marshal(msgs)
		t.Fatalf("fresh chat messages = %d, want 2: %s", len(msgs), string(raw))
	}
	if messageText(msgs[1]) != "user:where is main?" {
		raw, _ := json.Marshal(msgs)
		t.Fatalf("fresh chat prompt wrong: %s", string(raw))
	}
}

// Unknown thread IDs 404 before burning a model call.
func TestCodemapUnknownThread(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockEnsure(md)
	f := newFakeModel(t)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"hi","threadId":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown thread: got %d %q, want 404", rec.Code, rec.Body)
	}
	if f.calls != 0 {
		t.Fatalf("model calls = %d, want 0", f.calls)
	}
}

// DELETE refuses the in-flight thread (its log file) with 409 while an
// unrelated thread stays deletable.
func TestCodemapDeleteBusy(t *testing.T) {
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

	// Seed two threads directly in the store (folder per thread).
	thA, _, err := d.Codemaps.ReserveNewThread("abc", "a?", "s")
	if err != nil {
		t.Fatal(err)
	}
	thB, _, err := d.Codemaps.ReserveNewThread("abc", "b?", "s")
	if err != nil {
		t.Fatal(err)
	}
	// Rev-parse for the run plus the tool path is stubbed broadly.
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && (strings.Contains(argv[2], "grep -rn") || strings.Contains(argv[2], "sed -n"))
		}), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "x\n"}, nil).Maybe()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- authedPost(t, h, cookie, "/api/projects/abc/codemap",
			`{"prompt":"where is main?","threadId":"`+thA+`"}`)
	}()
	// Wait until the run holds the slot for thA.
	for i := 0; i < 200; i++ {
		if running, busy := codemapRunning("abc"); busy && running == thA {
			break
		}
		select {
		case rec := <-done:
			t.Fatalf("run finished early: %d", rec.Code)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/abc/codemap/threads/"+thA)
	if rec.Code != http.StatusConflict {
		once.Do(func() { close(release) })
		t.Fatalf("delete in-flight thread: got %d %q, want 409", rec.Code, rec.Body)
	}
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/abc/codemap/threads/"+thB)
	if rec.Code != http.StatusOK {
		once.Do(func() { close(release) })
		t.Fatalf("delete unrelated thread during run: got %d %q, want 200", rec.Code, rec.Body)
	}
	once.Do(func() { close(release) })
	if first := <-done; first.Code != http.StatusOK {
		t.Fatalf("run: got %d %q, want 200", first.Code, first.Body)
	}
	_ = thA
	_ = thB
}

// threadHistory lays rounds out as made (one assistant entry per round,
// in order) and renders prior answers as a natural-language transcript,
// not raw JSON.
func TestThreadHistoryRoundsAndTranscript(t *testing.T) {
	sections, _ := json.Marshal([]map[string]any{{
		"title": "Auth flow", "summary": "handleLogin() calls SessionService.create().",
		"refs": []map[string]any{{
			"path": "auth.go", "startLine": 10, "endLine": 12, "snippet": "func handleLogin() {", "function": "handleLogin",
		}},
	}})
	tools, _ := json.Marshal([]map[string]any{
		{"thought": "find entry", "steps": []map[string]any{
			{"tool": "search_code", "args": `{"pattern":"login"}`, "output": "auth.go:10:func handleLogin"},
		}},
		{"thought": "read it", "steps": []map[string]any{
			{"tool": "read_file", "args": `{"path":"auth.go"}`, "output": "func handleLogin() {"},
		}},
	})
	th := codemapthreads.Thread{ID: "t", Project: "abc", Turns: []codemapthreads.Turn{
		{TurnID: "r1", Prompt: "where is login?", Sections: sections, Tools: tools},
	}}
	h := threadHistory(th)
	if len(h) != 4 {
		raw, _ := json.Marshal(h)
		t.Fatalf("history entries = %d, want 4 (user+2 rounds+transcript): %s", len(h), string(raw))
	}
	if h[0]["role"] != "user" || h[0]["content"] != "where is login?" {
		t.Fatalf("entry 0 = %v, want user prompt", h[0])
	}
	for i := 1; i <= 2; i++ {
		if h[i]["role"] != "assistant" || h[i]["toolSteps"] == nil {
			t.Fatalf("entry %d = %v, want assistant tool round", i, h[i])
		}
	}
	last, _ := h[3]["content"].(string)
	if !strings.Contains(last, "Auth flow") || !strings.Contains(last, "handleLogin") || !strings.Contains(last, "auth.go:10-12 handleLogin()") {
		t.Fatalf("transcript missing flow content: %q", last)
	}
	if strings.Contains(last, `"title"`) || strings.Contains(last, `"sections"`) {
		t.Fatalf("transcript leaks raw JSON: %q", last)
	}
}

// The POST response carries the server timestamp so the optimistic turn
// renders its time without a reload.
func TestCodemapResponseHasTime(t *testing.T) {
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
		Time string `json:"time"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Time == "" {
		t.Fatalf("response missing time: %s", rec.Body)
	}
	if _, err := time.Parse(time.RFC3339, body.Time); err != nil {
		t.Fatalf("time not RFC3339: %q (%v)", body.Time, err)
	}
}

// A provider 200 with choices:null (free-tier gateways do this) fails the
// turn but keeps the thread: new chats still get a threadId and a 1.json
// failed placeholder (error set, no sections); the error names the cause.
func TestCodemapNullChoicesKeepsNewThread(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	nullChoices := func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen-test","object":"chat.completion","created":0,"model":"","choices":null}`))
	}
	// Three nulls: initial call plus step retries, so the turn fails
	// with the no-choices cause instead of a fake-exhaustion 500.
	f := newFakeModel(t, nullChoices, nullChoices, nullChoices)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"map this repo"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("null choices: got %d %q, want 502", rec.Code, rec.Body)
	}
	var body struct {
		Error       string `json:"error"`
		ThreadID    string `json:"threadId"`
		ThreadTitle string `json:"threadTitle"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, "no choices") {
		t.Fatalf("error hides cause: %q", body.Error)
	}
	if body.ThreadID == "" {
		t.Fatalf("failure dropped threadId: %s", rec.Body)
	}
	th, err := d.Codemaps.Get("abc", body.ThreadID)
	if err != nil {
		t.Fatalf("thread folder missing after failure: %v", err)
	}
	if len(th.Turns) != 1 {
		t.Fatalf("failed new chat turns = %d, want 1 failed placeholder", len(th.Turns))
	}
	if th.Turns[0].Error == nil || !strings.Contains(*th.Turns[0].Error, "no choices") {
		t.Fatalf("placeholder error missing cause: %+v", th.Turns[0])
	}
	if len(th.Turns[0].Sections) > 0 && string(th.Turns[0].Sections) != "null" {
		t.Fatalf("failed placeholder must have no sections: %s", th.Turns[0].Sections)
	}
	// The failure graph lands beside the turn, never in N.json.
	lin, err := d.Codemaps.ReadTurnLineage("abc", body.ThreadID, 1)
	if err != nil || !strings.Contains(string(lin), "no choices") {
		t.Fatalf("lineage = %q, %v", lin, err)
	}
}

// Same failure on an existing thread: prior turns survive, error carries
// the same threadId back.
func TestCodemapNullChoicesKeepsExistingThread(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	nullChoices := func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen-test","object":"chat.completion","created":0,"model":"","choices":null}`))
	}
	// Three nulls: initial call plus step retries, so the turn fails
	// with the no-choices cause instead of a fake-exhaustion 500.
	f := newFakeModel(t, nullChoices, nullChoices, nullChoices)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	seeded, _, err := d.Codemaps.ReserveNewThread("abc", "first?", "s")
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := json.Marshal([]map[string]any{{"title": "A"}})
	if err := d.Codemaps.CompleteTurn("abc", seeded, 1, codemapthreads.Turn{TurnID: "t1", Prompt: "first?", Sections: sec, Time: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap",
		`{"prompt":"follow up","threadId":"`+seeded+`"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("null choices: got %d %q, want 502", rec.Code, rec.Body)
	}
	var body struct {
		Error    string `json:"error"`
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ThreadID != seeded {
		t.Fatalf("threadId = %q, want %q", body.ThreadID, seeded)
	}
	th, err := d.Codemaps.Get("abc", seeded)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) != 2 || th.Turns[0].Prompt != "first?" {
		t.Fatalf("prior turns lost: %+v", th.Turns)
	}
	if th.Turns[1].Error == nil || th.Turns[1].Prompt != "follow up" {
		t.Fatalf("failed follow-up not a failed placeholder: %+v", th.Turns[1])
	}
}
