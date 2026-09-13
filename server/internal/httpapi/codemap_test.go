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

func TestCodemapSuccess(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockSearchRead(md, "main.go:10:func main() {\n", "func main() {\n")
	f := newFakeModel(t,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("c1", "search_code", `{"pattern":"main"}`)})
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("c2", "read_file", `{"path":"main.go","start":10,"end":12}`)})
		},
		func(w http.ResponseWriter, body map[string]any) {
			if _, ok := body["response_format"]; !ok {
				t.Errorf("codemap call drops json_schema")
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
		SHA      string `json:"sha"`
		Sections []struct {
			Title string `json:"title"`
			Refs  []struct {
				Path string `json:"path"`
			} `json:"refs"`
		} `json:"sections"`
		Tools []struct {
			Tool string `json:"tool"`
			Args string `json:"args"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TurnID == "" || body.SHA != "abc123" {
		t.Fatalf("turn meta = %+v, want turnId set and sha abc123", body)
	}
	if len(body.Sections) != 1 || body.Sections[0].Refs[0].Path != "main.go" {
		t.Fatalf("sections = %+v", body.Sections)
	}
	if len(body.Tools) != 2 || body.Tools[0].Tool != "search_code" || body.Tools[1].Tool != "read_file" {
		t.Fatalf("tools = %+v, want the two calls with args", body.Tools)
	}
	if !strings.Contains(body.Tools[0].Args, "main") || !strings.Contains(body.Tools[1].Args, "main.go") {
		t.Fatalf("tool args lost: %+v", body.Tools)
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
	if gotTypes["codemap.tool"] != 2 || gotTypes["codemap.tool_result"] != 2 {
		t.Fatalf("tool trace counts = %v, want 2 starts + 2 results", gotTypes)
	}
	// History joins the pair on turn id.
	rec = authedGet(t, h, cookie, "/api/projects/abc/codemap/history")
	var hist struct {
		Turns []struct {
			TurnID   string `json:"turnId"`
			Prompt   string `json:"prompt"`
			Sections []struct {
				Title string `json:"title"`
			} `json:"sections"`
			Tools []struct {
				Tool string `json:"tool"`
			} `json:"tools"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if len(hist.Turns) != 1 || hist.Turns[0].TurnID != body.TurnID || hist.Turns[0].Prompt != "where is main?" {
		t.Fatalf("history = %+v", hist)
	}
	if len(hist.Turns[0].Sections) != 1 {
		t.Fatalf("history sections = %+v", hist.Turns[0].Sections)
	}
	if len(hist.Turns[0].Tools) != 2 {
		t.Fatalf("history tools = %+v, want persisted tool summary", hist.Turns[0].Tools)
	}
}

func TestCodemapModelBadJSON(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	f := newFakeModel(t, func(w http.ResponseWriter, _ map[string]any) {
		writeCompletion(w, "stop", "not json at all", nil)
	})
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
}

func TestCodemapBusy(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	release := make(chan struct{})
	var once sync.Once
	f := newFakeModel(t, func(w http.ResponseWriter, _ map[string]any) {
		<-release
		writeCompletion(w, "stop", finalCodemapJSON(), nil)
	})
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
		_, deadline = codemapBusy.m["abc"], true
		held := codemapBusy.m["abc"]
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
