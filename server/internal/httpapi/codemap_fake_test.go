package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/state"
	dockermocks "pcoder/mocks/docker"
)

// Codemap tests mock the model at the HTTP level: a fake OpenAI-compatible
// server speaks /chat/completions, so the SDK, the loop, the tools, and the
// handlers are all exercised, not stubbed.

// fakeModel is a scripted chat-completions server. Each call pops the next
// responder; unexpected extra calls fail the test.
type fakeModel struct {
	t     *testing.T
	mu    sync.Mutex
	calls int
	steps []func(w http.ResponseWriter, body map[string]any)
	srv   *httptest.Server
}

func newFakeModel(t *testing.T, steps ...func(w http.ResponseWriter, body map[string]any)) *fakeModel {
	t.Helper()
	f := &fakeModel{t: t, steps: steps}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		n := f.calls
		f.calls++
		f.mu.Unlock()
		if n >= len(f.steps) {
			t.Errorf("fake model: unexpected call %d (body=%v)", n+1, body)
			http.Error(w, "unexpected call", http.StatusInternalServerError)
			return
		}
		f.steps[n](w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func writeCompletion(w http.ResponseWriter, finish, content string, toolCalls []map[string]any) {
	msg := map[string]any{"role": "assistant", "content": content}
	if toolCalls != nil {
		msg["tool_calls"] = toolCalls
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
		"choices": []map[string]any{{"index": 0, "finish_reason": finish, "message": msg}},
	})
}

func toolCall(id, name, args string) map[string]any {
	return map[string]any{
		"id": id, "type": "function",
		"function": map[string]any{"name": name, "arguments": args},
	}
}

func seedAI(t *testing.T, st *state.Store, baseURL string) {
	t.Helper()
	if err := st.Mutate(func(doc *state.Document) error {
		doc.AIModels = []state.AIModel{{ID: "default", Label: "Default", BaseURL: baseURL, APIKey: "k", Model: "m"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// mockEnsure wires only Inspect + RepoTarget fallback (/workspace): enough
// for handlers that reject before touching the repo (validation tests).
func mockEnsure(md *dockermocks.MockClient) {
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
}

// mockOrientation stubs the repo-root listing Ask runs for prompt
// orientation (stack-appropriate first searches). Every codemap POST
// needs it alongside mockRepoDir.
func mockOrientation(md *dockermocks.MockClient, out string) {
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && strings.Contains(argv[2], "ls -1")
		}), false).
		Return(docker.ExecResult{ExitCode: 0, Output: out}, nil)
}

// respondCodemap is a fake-model responder that always answers with the
// final codemap JSON. Pass it N times for an N-attempt script.
func respondCodemap(w http.ResponseWriter, _ map[string]any) {
	writeCompletion(w, "stop", finalCodemapJSON(), nil)
}

// mockHydrate stubs the exact-line reads hydrateRef runs for every
// returned ref (sed -n). Any fake whose final JSON carries refs needs it.
func mockHydrate(md *dockermocks.MockClient, out string) {
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && strings.Contains(argv[2], "sed -n")
		}), false).
		Return(docker.ExecResult{ExitCode: 0, Output: out}, nil)
}

// mockRepoDir wires Inspect + RepoTarget fallback (/workspace) + HEAD sha
// for project "abc", shared by every codemap handler test.
func mockRepoDir(md *dockermocks.MockClient, sha string) {
	mockEnsure(md)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && strings.Contains(argv[2], "rev-parse HEAD")
		}), false).
		Return(docker.ExecResult{ExitCode: 0, Output: sha + "\n"}, nil)
}
