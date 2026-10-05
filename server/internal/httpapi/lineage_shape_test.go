package httpapi

// Backend→LLM shape test for the lineage cleanup: one butler turn and one
// codemap turn against the scripted fake model, asserting the persisted
// data looks correct. N.json stays the UI source; lineage stays readable
// diagnostics (tools once at top, light per-step rows, ts on every event,
// no heavy payload blobs, no legacy tier keys).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/project"
)

func assertReadableLineage(t *testing.T, raw []byte, wantPrompt string) map[string]any {
	t.Helper()
	var lin map[string]any
	if err := json.Unmarshal(raw, &lin); err != nil {
		t.Fatalf("lineage not JSON: %v", err)
	}
	for _, legacy := range []string{"initialRequest", "prunedTier1Data", "extractorInput", "extractorOutput", "payload"} {
		if strings.Contains(string(raw), `"`+legacy+`"`) {
			t.Fatalf("lineage must not contain %q: %s", legacy, cut(string(raw), 2000))
		}
	}
	if lin["prompt"] != wantPrompt {
		t.Fatalf("lineage prompt = %v, want %q", lin["prompt"], wantPrompt)
	}
	if lin["model"] == "" {
		t.Fatalf("lineage missing model: %s", cut(string(raw), 500))
	}
	tools, _ := lin["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("lineage missing tools header: %s", cut(string(raw), 500))
	}
	for _, rt := range tools {
		m, _ := rt.(map[string]any)
		if m["name"] == "" {
			t.Fatalf("tool entry missing name: %v", rt)
		}
		if _, ok := m["schema"]; ok {
			t.Fatalf("tool entry must not carry schema: %v", rt)
		}
	}
	events, _ := lin["events"].([]any)
	if len(events) == 0 {
		t.Fatalf("lineage has no events")
	}
	seen := map[string]int{}
	for i, re := range events {
		m, _ := re.(map[string]any)
		if m["ts"] == "" || m["kind"] == "" {
			t.Fatalf("event %d missing ts/kind: %v", i, re)
		}
		seen[m["kind"].(string)]++
		if _, ok := m["payload"]; ok {
			t.Fatalf("event %d must not carry payload: %v", i, re)
		}
	}
	for _, k := range []string{"llm_request", "llm_response"} {
		if seen[k] == 0 {
			t.Fatalf("lineage missing %q (have %v)", k, seen)
		}
	}
	for i, re := range events {
		m, _ := re.(map[string]any)
		if m["kind"] == "llm_request" && m["content"] == "" {
			t.Fatalf("event %d llm_request missing content: %v", i, re)
		}
	}
	return lin
}

func TestLineageShapeButler(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Events.Append("project.ready", map[string]any{"id": "a/b"}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Inspect(mock.Anything, "pcoder-a-b").Return(docker.Container{Running: true, Status: "running"}, nil)
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("c1", butlerToolListProjects, "{}")})
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "All healthy.", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := butlerPost(t, h, cookie, `{"prompt":"brief me"}`, http.StatusOK)
	last := finalBody(t, rec)
	tid, _ := last["threadId"].(string)
	if tid == "" {
		t.Fatalf("missing threadId: %v", last)
	}
	waitThreadSettled(t, h, cookie, tid)
	th, err := d.Butler.Get(butlerScope, tid)
	if err != nil || len(th.Turns) != 1 {
		t.Fatalf("turns = %+v, err=%v", th.Turns, err)
	}
	var transcript butlerPayload
	if err := json.Unmarshal(th.Turns[0].Payload, &transcript); err != nil || transcript.Answer == "" {
		t.Fatalf("N.json payload = %s, err=%v", string(th.Turns[0].Payload), err)
	}
	if len(transcript.Steps) != 1 || transcript.Steps[0].Tool != butlerToolListProjects {
		t.Fatalf("N.json steps = %+v, want one list_projects", transcript.Steps)
	}
	raw, err := d.Butler.ReadTurnLineage(butlerScope, tid, 1)
	if err != nil {
		t.Fatal(err)
	}
	lin := assertReadableLineage(t, raw, "brief me")
	if lin["turnId"] == "" || lin["threadId"] != tid {
		t.Fatalf("lineage identity = %v, want turnId set + threadId %s", lin, tid)
	}
}

func TestLineageShapeCodemap(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	mockSearchRead(md, "main.go:10:func main() {\n", "func main() {\n")
	mockHydrate(md, "func main() {\n")
	f := newFakeModel(t,
		codeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("c1", "search_code", `{"pattern":"main"}`)})
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", finalCodemapJSON(), nil)
		},
		func(w http.ResponseWriter, body map[string]any) {
			if _, ok := body["response_format"]; !ok {
				t.Errorf("format call must carry json_schema")
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
		t.Fatalf("codemap: got %d %q", rec.Code, rec.Body.String())
	}
	var body struct {
		ThreadID string `json:"threadId"`
		Sections []struct {
			Title string `json:"title"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.ThreadID == "" {
		t.Fatalf("response = %s, err=%v", rec.Body.String(), err)
	}
	if len(body.Sections) != 1 {
		t.Fatalf("sections = %+v, want 1", body.Sections)
	}
	th, err := d.Codemaps.Get("abc", body.ThreadID)
	if err != nil || len(th.Turns) != 1 {
		t.Fatalf("turns = %+v, err=%v", th.Turns, err)
	}
	raw, err := d.Codemaps.ReadTurnLineage("abc", body.ThreadID, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertReadableLineage(t, raw, "where is main?")
}
