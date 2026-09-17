package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/state"
	dockermocks "pcoder/mocks/docker"
)

func mustOpenState(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(t.TempDir(), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func readStateFileT(t *testing.T, st *state.Store) []byte {
	t.Helper()
	raw, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAIConfigGetEmpty(t *testing.T) {
	d, pinOut := newTestDeps(t)
	d.State = mustOpenState(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/ai/config")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d %q, want 200", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["configured"] != false {
		t.Fatalf("configured = %v, want false", body["configured"])
	}
}

func TestAISaveRejectsMissingFields(t *testing.T) {
	d, pinOut := newTestDeps(t)
	d.State = mustOpenState(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	for _, body := range []string{
		`{"baseURL":"http://x","model":"m"}`,
		`{"baseURL":"","apiKey":"k","model":"m"}`,
		`not json`,
	} {
		rec := authedPost(t, h, cookie, "/api/ai/config", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: got %d %q, want 400", body, rec.Code, rec.Body)
		}
	}
}

func TestAISaveTestsBeforeSaving(t *testing.T) {
	d, pinOut := newTestDeps(t)
	st := mustOpenState(t)
	d.State = st
	// Fake model refuses everything: save must fail AND store nothing.
	f := newFakeModel(t, func(w http.ResponseWriter, _ map[string]any) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	})
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/ai/config",
		`{"baseURL":`+strconv.Quote(f.srv.URL)+`,"apiKey":"k","model":"m"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("save: got %d %q, want 502", rec.Code, rec.Body)
	}
	var cfg struct {
		AI *struct{} `json:"ai"`
	}
	raw := readStateFileT(t, st)
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AI != nil {
		t.Fatalf("failed save persisted AI config")
	}
}

func TestAISaveSuccess(t *testing.T) {
	d, pinOut := newTestDeps(t)
	st := mustOpenState(t)
	d.State = st
	f := newFakeModel(t, func(w http.ResponseWriter, body map[string]any) {
		// Save's test call must declare tools and json_schema: the probe
		// catches weak models here, not at first codemap.
		tools, _ := body["tools"].([]any)
		if len(tools) == 0 {
			t.Errorf("test call declares no tools")
		}
		if _, ok := body["response_format"]; !ok {
			t.Errorf("test call has no response_format")
		}
		writeCompletion(w, "stop", `{"ok":true}`, nil)
	})
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/ai/config",
		`{"baseURL":`+strconv.Quote(f.srv.URL)+`,"apiKey":"k","model":"m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: got %d %q, want 200", rec.Code, rec.Body)
	}
	rec = authedGet(t, h, cookie, "/api/ai/config")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["configured"] != true || body["model"] != "m" {
		t.Fatalf("config after save = %v, want configured=true model=m", body)
	}
	if ev := lastEvent(t, d); ev.Type != "ai.configured" {
		t.Fatalf("last event = %q, want ai.configured", ev.Type)
	}
}

func TestAITestEndpointShapes(t *testing.T) {
	d, pinOut := newTestDeps(t)
	d.State = mustOpenState(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	// Missing fields fall back to empty store: 400, not a model call.
	rec := authedPost(t, h, cookie, "/api/ai/test", `{"baseURL":"http://x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("partial body: got %d, want 400", rec.Code)
	}
	// Provider failure maps to 502 with the detail inline. 400 on purpose:
	// the SDK retries 503s, which would triple-call the fake.
	f := newFakeModel(t, func(w http.ResponseWriter, _ map[string]any) {
		http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusBadRequest)
	})
	rec = authedPost(t, h, cookie, "/api/ai/test",
		`{"baseURL":`+strconv.Quote(f.srv.URL)+`,"apiKey":"k","model":"m"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("provider failure: got %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "overloaded") {
		t.Fatalf("provider detail lost: %q", rec.Body)
	}
}

func TestCodemapNoKey(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	_ = md
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"hi"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("no key: got %d %q, want 409", rec.Code, rec.Body)
	}
}

func TestCodemapBadPrompt(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	_ = md
	seedProject(t, st, "abc")
	seedAI(t, st, "http://127.0.0.1:1")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	for _, body := range []string{`{"prompt":""}`, `{"prompt":"   "}`, `{"prompt":"` + strings.Repeat("x", 4001) + `"}`} {
		rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body len %d: got %d %q, want 400", len(body), rec.Code, rec.Body)
		}
	}
}

func TestCodemapUnknownProject(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	_ = md
	seedAI(t, st, "http://127.0.0.1:1")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/nope/codemap", `{"prompt":"hi"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: got %d %q, want 404", rec.Code, rec.Body)
	}
}

// mockSearchRead wires grep + sed outputs for the two codemap tools.
func mockSearchRead(md *dockermocks.MockClient, grepOut, sedOut string) {
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && strings.Contains(argv[2], "grep -rn")
		}), false).
		Return(docker.ExecResult{ExitCode: 0, Output: grepOut}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && strings.Contains(argv[2], "sed -n")
		}), false).
		Return(docker.ExecResult{ExitCode: 0, Output: sedOut}, nil)
}

func finalCodemapJSON() string {
	return `{"sections":[{"title":"Auth","summary":"Login lives here.","refs":[{"path":"main.go","startLine":10,"endLine":12,"snippet":"func main() {"}]}]}`
}

// mustReadTurnFile renders the stored Nth turn as on-disk JSON: the
// per-turn asserts verify the persisted shape carries no extractor keys.
func mustReadTurnFile(t *testing.T, d Deps, project, threadID string, n int) []byte {
	t.Helper()
	th, err := d.Codemaps.Get(project, threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) < n {
		t.Fatalf("thread %s has %d turns, want >= %d", threadID, len(th.Turns), n)
	}
	raw, err := json.Marshal(th.Turns[n-1])
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCodemapSuccess(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	mockSearchRead(md, "main.go:10:func main() {\n", "func main() {\n")
	f := newFakeModel(t,
		func(w http.ResponseWriter, body map[string]any) {
			// Tool loop stays schema-free: providers null out choices
			// or skip tool calls when json_schema rides with tools.
			if _, ok := body["response_format"]; ok {
				t.Errorf("tool-loop call must not carry json_schema")
			}
			writeCompletion(w, "tool_calls", "", []map[string]any{
				toolCall("c1", "search_code", `{"pattern":"main"}`),
				toolCall("c2", "search_code", `{"pattern":"App"}`),
			})
		},
		func(w http.ResponseWriter, body map[string]any) {
			if _, ok := body["response_format"]; ok {
				t.Errorf("tool-loop call must not carry json_schema")
			}
			writeCompletion(w, "tool_calls", "", []map[string]any{
				toolCall("c3", "read_file", `{"path":"main.go","start":10,"end":12}`),
				toolCall("c4", "read_file", `{"path":"app.go","start":1,"end":12}`),
			})
		},
		func(w http.ResponseWriter, body map[string]any) {
			if _, ok := body["response_format"]; ok {
				t.Errorf("tool-loop call must not carry json_schema")
			}
			writeCompletion(w, "tool_calls", "", []map[string]any{
				toolCall("c5", "search_code", `{"pattern":"hydrate"}`),
				toolCall("c6", "read_file", `{"path":"index.html","start":1,"end":12}`),
			})
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", finalCodemapJSON(), nil)
		},
		func(w http.ResponseWriter, body map[string]any) {
			// Formatting call: schema enforced, no tools attached.
			if _, ok := body["response_format"]; !ok {
				t.Errorf("format call drops json_schema")
			}
			if tools, _ := body["tools"].([]any); len(tools) != 0 {
				t.Errorf("format call must not carry tools")
			}
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
		TurnID   string `json:"turnId"`
		ThreadID string `json:"threadId"`
		SHA      string `json:"sha"`
		Sections []struct {
			Title string `json:"title"`
			Refs  []struct {
				Path string `json:"path"`
			} `json:"refs"`
		} `json:"sections"`
		Tools []struct {
			Tool   string `json:"tool"`
			Args   string `json:"args"`
			Output string `json:"output"`
			Err    string `json:"error"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TurnID == "" || body.SHA != "abc123" {
		t.Fatalf("turn meta = %+v, want turnId set and sha abc123", body)
	}
	if strings.Contains(rec.Body.String(), "extractorOutput") {
		t.Fatalf("POST response must not carry extractorOutput: %s", rec.Body)
	}
	if body.ThreadID == "" {
		t.Fatalf("missing threadId in response %+v", body)
	}
	if len(body.Sections) != 1 || body.Sections[0].Refs[0].Path != "main.go" {
		t.Fatalf("sections = %+v", body.Sections)
	}
	if len(body.Tools) != 6 || body.Tools[0].Tool != "search_code" || body.Tools[5].Tool != "read_file" {
		t.Fatalf("tools = %+v, want six calls with args", body.Tools)
	}
	if !strings.Contains(body.Tools[0].Args, "main") || !strings.Contains(body.Tools[2].Args, "main.go") {
		t.Fatalf("tool args lost: %+v", body.Tools)
	}
	// Tool outputs persist per step: this is what follow-ups replay.
	if !strings.Contains(body.Tools[0].Output, "func main()") {
		t.Fatalf("search output lost: %+v", body.Tools[0])
	}
	if !strings.Contains(body.Tools[1].Output, "func main()") {
		t.Fatalf("read output lost: %+v", body.Tools[1])
	}
	// The live project log carries the full trace for the Logs tab.
	gotTypes := map[string]int{}
	for _, e := range d.ProjectLogs.Read("abc", 0, 0) {
		gotTypes[e.Type]++
	}
	for _, want := range []string{"codemap.start", "codemap.tool", "codemap.tool_result", "codemap.done"} {
		if gotTypes[want] == 0 {
			t.Fatalf("project log missing %q (have %v)", want, gotTypes)
		}
	}
	if gotTypes["codemap.tool"] != 6 || gotTypes["codemap.tool_result"] != 6 {
		t.Fatalf("tool trace counts = %v, want 6 starts + 6 results", gotTypes)
	}
	// The turn persists in its thread file (one chat per file), and the
	// global log keeps only a lightweight audit line — never the sections.
	// The thread file is what the UI rebuilds off of, so assert its
	// integrity directly from the store: prompt, sections JSON, tool
	// steps with outputs, turnId/sha/time all present.
	rec = authedGet(t, h, cookie, "/api/projects/abc/codemap/threads/"+body.ThreadID)
	var got struct {
		Thread struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Turns []struct {
				TurnID   string `json:"turnId"`
				Prompt   string `json:"prompt"`
				Sections []struct {
					Title string `json:"title"`
				} `json:"sections"`
				Tools []struct {
					Tool   string `json:"tool"`
					Output string `json:"output"`
				} `json:"tools"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Thread.ID != body.ThreadID {
		t.Fatalf("thread id = %q, want %q", got.Thread.ID, body.ThreadID)
	}
	if len(got.Thread.Turns) != 1 || got.Thread.Turns[0].TurnID != body.TurnID || got.Thread.Turns[0].Prompt != "where is main?" {
		t.Fatalf("thread turns = %+v", got.Thread.Turns)
	}
	if len(got.Thread.Turns[0].Sections) != 1 {
		t.Fatalf("thread sections = %+v", got.Thread.Turns[0].Sections)
	}
	// The thread endpoint exposes the same flat step shape as POST.
	rounds := got.Thread.Turns[0].Tools
	if len(rounds) != 6 || rounds[0].Tool != "search_code" || rounds[5].Tool != "read_file" {
		t.Fatalf("thread tools = %+v, want six flat steps", got.Thread.Turns[0].Tools)
	}
	th, err := d.Codemaps.Get("abc", body.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) != 1 {
		t.Fatalf("store turns = %d, want 1", len(th.Turns))
	}
	stored := th.Turns[0]
	if stored.TurnID == "" || stored.SHA != "abc123" || stored.Prompt != "where is main?" || stored.Time.IsZero() {
		t.Fatalf("stored turn meta incomplete: %+v", stored)
	}
	var storedSections []map[string]any
	if err := json.Unmarshal(stored.Sections, &storedSections); err != nil || len(storedSections) != 1 {
		t.Fatalf("stored sections invalid: %s (%v)", string(stored.Sections), err)
	}
	var storedRounds []map[string]any
	if err := json.Unmarshal(stored.Tools, &storedRounds); err != nil || len(storedRounds) != 3 {
		t.Fatalf("stored tools invalid: %s (%v)", string(stored.Tools), err)
	}
	for i, r := range storedRounds {
		steps, _ := r["steps"].([]any)
		if len(steps) != 2 {
			t.Fatalf("stored round %d steps = %v, want 2", i, r)
		}
		sm, _ := steps[0].(map[string]any)
		out, _ := sm["output"].(string)
		if !strings.Contains(out, "func main()") {
			t.Fatalf("stored tool %d missing output: %v", i, sm)
		}
	}
	// N.json is the user-facing side only: no extractor keys. The
	// per-turn files live at <tid>/1.json + <tid>/1.lineage.json.
	if strings.Contains(string(mustReadTurnFile(t, d, "abc", body.ThreadID, 1)), "extractorOutput") {
		t.Fatalf("N.json must not carry extractorOutput")
	}
	lineageRaw, err := d.Codemaps.ReadTurnLineage("abc", body.ThreadID, 1)
	if err != nil {
		t.Fatalf("read lineage: %v", err)
	}
	var lineage map[string]any
	if err := json.Unmarshal(lineageRaw, &lineage); err != nil {
		t.Fatal(err)
	}
	if _, ok := lineage["prunedTier1Data"]; !ok {
		t.Fatalf("lineage missing prunedTier1Data: %s", lineageRaw)
	}
	if _, ok := lineage["extractorInput"]; ok {
		t.Fatalf("lineage must not own extractorInput: %s", lineageRaw)
	}
	if _, ok := lineage["extractorOutput"]; ok {
		t.Fatalf("lineage must not own extractorOutput: %s", lineageRaw)
	}
	// The pruned snapshot round-trips the tier-1 evidence with pinned
	// caps (answer 16k / output 6k / content 8k / args 2k / error 2k /
	// payloads 12k).
	snap, _ := lineage["prunedTier1Data"].(map[string]any)
	if _, ok := snap["answer"]; !ok {
		t.Fatalf("prunedTier1Data missing answer: %s", lineageRaw)
	}
	if _, ok := snap["events"]; !ok {
		t.Fatalf("prunedTier1Data missing events: %s", lineageRaw)
	}
	events, _ := lineage["events"].([]any)
	if len(events) < 20 {
		t.Fatalf("lineage events = %d, want a multi-round conversation graph", len(events))
	}
	toolStarts := 0
	for _, rawEvent := range events {
		if event, _ := rawEvent.(map[string]any); event["kind"] == "tool_start" {
			toolStarts++
		}
	}
	if toolStarts != 6 {
		t.Fatalf("lineage tool starts = %d, want 6", toolStarts)
	}
	if ev := lastEvent(t, d); ev.Type != "codemap.turn" {
		t.Fatalf("last event = %q, want codemap.turn audit", ev.Type)
	}
	if _, hasSections := lastEvent(t, d).Data["sections"]; hasSections {
		// sections count is fine; full section bodies must not land here
		if _, ok := lastEvent(t, d).Data["prompt"]; !ok {
			t.Fatalf("audit event missing prompt excerpt")
		}
	}
}

func TestCodemapModelBadJSON(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	f := newFakeModel(t,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "not json at all", nil)
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "not json at all", nil)
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "not json at all", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"hi"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("bad json: got %d %q, want 502", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "not json") {
		t.Fatalf("bad json error hides model output: %q", rec.Body)
	}
	var failBody struct {
		ThreadID    string `json:"threadId"`
		ThreadTitle string `json:"threadTitle"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &failBody); err != nil {
		t.Fatal(err)
	}
	if failBody.ThreadID == "" {
		t.Fatalf("failure dropped threadId: %s", rec.Body)
	}
	gotTypes := map[string]int{}
	for _, e := range d.ProjectLogs.Read("abc", 0, 0) {
		gotTypes[e.Type]++
	}
	if gotTypes["codemap.thread_initialized"] != 1 || gotTypes["codemap.turn_failed"] != 1 || gotTypes["codemap.error"] != 1 {
		t.Fatalf("failed turn boundary logs = %v, want initialization, fail persist, and run error", gotTypes)
	}
	threads, err := d.Codemaps.List("abc")
	if err != nil || len(threads) != 1 || threads[0].TurnCount != 1 {
		t.Fatalf("failed turn thread record = %+v, %v; expected one failed placeholder turn", threads, err)
	}
	th, err := d.Codemaps.Get("abc", failBody.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) != 1 || th.Turns[0].Error == nil {
		t.Fatalf("failed placeholder missing error: %+v", th.Turns)
	}
}

func TestCodemapBusy(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	release := make(chan struct{})
	mockHydrate(md, "func main() {\n")
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
		done <- authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"first"}`)
	}()
	// Wait until the first run holds the per-project slot.
	deadline := false
	for i := 0; i < 100 && !deadline; i++ {
		codemapBusy.mu.Lock()
		_, held := codemapBusy.m["abc"]
		_, deadline = codemapBusy.m["abc"], true
		codemapBusy.mu.Unlock()
		if held {
			break
		}
		select {
		case rec := <-done:
			t.Fatalf("first run finished early: %d", rec.Code)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"second"}`)
	if rec.Code != http.StatusConflict {
		once.Do(func() { close(release) })
		t.Fatalf("second run: got %d %q, want 409", rec.Code, rec.Body)
	}
	once.Do(func() { close(release) })
	if first := <-done; first.Code != http.StatusOK {
		t.Fatalf("first run: got %d %q, want 200", first.Code, first.Body)
	}
}

func TestCodemapFileValidation(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockEnsure(md)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	for _, path := range []string{
		"/api/projects/abc/file?path=/etc/passwd&start=1&end=2",
		"/api/projects/abc/file?path=../x&start=1&end=2",
		"/api/projects/abc/file?path=main.go&start=0&end=2",
		"/api/projects/abc/file?path=main.go&start=5&end=3",
		"/api/projects/abc/file?path=main.go&start=1&end=500",
		"/api/projects/abc/file?path=main.go&start=x&end=2",
	} {
		rec := authedGet(t, h, cookie, path)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d, want 400", path, rec.Code)
		}
	}
}

func TestCodemapFileBinaryAndMoved(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// Binary: sed output with NUL.
	mockRepoDir(md, "newsha")
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && strings.Contains(argv[2], "sed -n")
		}), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "a\x00b"}, nil).Once()
	rec := authedGet(t, h, cookie, "/api/projects/abc/file?path=img.png&start=1&end=5&sha=oldsha")
	var bin struct {
		Binary  bool   `json:"binary"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bin); err != nil {
		t.Fatal(err)
	}
	if !bin.Binary || bin.Content != "" {
		t.Fatalf("binary = %+v, want binary flag with empty content", bin)
	}

	// Moved: stored sha differs from HEAD.
	mockRepoDir(md, "newsha")
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && strings.Contains(argv[2], "sed -n")
		}), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "line1\n"}, nil).Once()
	rec = authedGet(t, h, cookie, "/api/projects/abc/file?path=main.go&start=1&end=1&sha=oldsha")
	var moved struct {
		Moved   bool   `json:"moved"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &moved); err != nil {
		t.Fatal(err)
	}
	if !moved.Moved || moved.Content == "" {
		t.Fatalf("moved = %+v, want moved=true with content", moved)
	}
}
