package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"pcoder/internal/docker"
)

// Handler tests for the gitops endpoints (commit/log/identity/push/
// switch). All run against the mocked docker exec used by the diff
// tests: seedProject + newSessionDeps, no live engine.

// gitCmd matches a mocked Exec whose argv is [bash -lc <cmd>] with cmd
// containing sub (the shape Sessions.ExecCommand produces).
func gitCmd(sub string) any {
	return mock.MatchedBy(func(argv []string) bool {
		return len(argv) == 3 && argv[0] == "bash" && argv[1] == "-lc" && strings.Contains(argv[2], sub)
	})
}

func gitopsSetup(t *testing.T) (Deps, *http.Cookie, http.Handler) {
	t.Helper()
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	// RepoTarget: no clone at /workspace/repo, so the repo dir is /workspace.
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil).Maybe()
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	return d, cookie, h
}

func TestGitCommitNothingToCommit(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	// Merge-conflict probe: empty → no conflict.
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("--diff-filter=U"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "\n"}, nil)
	// Commit fails with nothing staged.
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("commit --no-verify --file -"), false).
		Return(docker.ExecResult{ExitCode: 1, Output: "nothing staged to commit"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/commit", `{"message":"test"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("commit with nothing staged: got %d %q, want 400", rec.Code, rec.Body)
	}
}

func TestGitCommitIdentityUnset(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("--diff-filter=U"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("commit --no-verify --file -"), false).
		Return(docker.ExecResult{ExitCode: 128, Output: "Author identity unknown\n*** Please tell me who you are."}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/commit", `{"message":"test"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("commit without identity: got %d %q, want 409", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "identity") {
		t.Fatalf("identity error must surface: %q", rec.Body)
	}
}

func TestGitCommitSuccess(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("--diff-filter=U"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "\n"}, nil)
	// The commit command must carry the base64-encoded message.
	msg := "feat: multi\nline body"
	b64 := base64.StdEncoding.EncodeToString([]byte(msg))
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("'"+b64+"'"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "abc1234"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("rev-parse --abbrev-ref HEAD"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "main\n"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/commit", `{"message":`+quoteJSON(t, msg)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: got %d %q, want 200", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"commit":"abc1234"`) {
		t.Fatalf("sha missing: %q", rec.Body)
	}
}

func TestGitIdentityEndpoints(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("config user.name; true"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "Zac\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("config user.email; true"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "z@x.io\n"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/abc/git/identity")
	if rec.Code != http.StatusOK {
		t.Fatalf("identity get: got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"name":"Zac"`) {
		t.Fatalf("name missing: %q", rec.Body)
	}

	// POST validation: leading-dash rejected without any exec.
	rec = authedPost(t, h, cookie, "/api/projects/abc/git/identity", `{"name":"-evil","email":"z@x.io"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("identity post -evil: got %d %q, want 400", rec.Code, rec.Body)
	}
	rec = authedPost(t, h, cookie, "/api/projects/abc/git/identity", `{"name":"Zac","email":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("identity post empty email: got %d, want 400", rec.Code)
	}
}

func TestGitPushSurfacesErrors(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("rev-parse --abbrev-ref HEAD"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "main\n"}, nil)
	// The push command must carry GIT_TERMINAL_PROMPT=0.
	pushCalled := false
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		mock.MatchedBy(func(argv []string) bool {
			pushCalled = len(argv) == 3 && strings.Contains(argv[2], "GIT_TERMINAL_PROMPT=0") && strings.Contains(argv[2], "push -u origin")
			return pushCalled
		}), false).
		Return(docker.ExecResult{ExitCode: 128, Output: "fatal: could not read Username"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/push", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("push failure: got %d %q, want 502", rec.Code, rec.Body)
	}
	if !pushCalled {
		t.Fatalf("push command missing GIT_TERMINAL_PROMPT=0 / push -u origin")
	}
	// Auth-shaped failures point at Git setup, not the terminal.
	if !strings.Contains(rec.Body.String(), "Git credentials rejected") {
		t.Fatalf("push auth error must point at Git setup: %q", rec.Body)
	}
}

func TestGitSwitchDirtyTree(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("check-ref-format"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil)
	// Dirty tracked tree (M in X column). Output includes the sentinel
	// line the handler prepends (mock replays the full exec output).
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("--untracked-files=no"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "DIRTY-BEGIN\n M notes.txt\n"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/switch", `{"branch":"other"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("dirty switch: got %d %q, want 409", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "notes.txt") {
		t.Fatalf("dirty file list must surface: %q", rec.Body)
	}
}

func TestGitSwitchUntrackedOnlySucceeds(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("check-ref-format"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("--untracked-files=no"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "DIRTY-BEGIN\n?? untracked.txt\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("--diff-filter=U"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("git -C '/workspace' switch "), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "Switched to branch 'other'\n"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/switch", `{"branch":"other"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("untracked-only switch: got %d %q, want 200", rec.Code, rec.Body)
	}
}

func TestGitSwitchInvalidName(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		gitCmd("check-ref-format"), false).
		Return(docker.ExecResult{ExitCode: 1, Output: "fatal: not a valid ref"}, nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/switch", `{"branch":"-evil"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid branch name: got %d, want 400", rec.Code)
	}
}

func quoteJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestGitExplainFireAndForget drives the fire-and-forget explainer with
// the fake model: 202 returns immediately with a thread id, the busy
// slot reports the running thread, and the detached run completes the
// turn (persisted via the store) after the HTTP response is long gone.
func TestGitExplainFireAndForget(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	seedProject(t, st, "abc")
	// The explainer runs the full codemap loop: one grounded tool round,
	// a final text answer, then the schema-enforcing format call.
	fm := newFakeModel(t,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "tool_calls", "", []map[string]any{
				toolCall("c1", "git_status", `{}`),
			})
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "The change appends a line to hello.txt.", nil)
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", `{"sections":[{"title":"It changes hello.txt","summary":"A line was appended.","refs":[]}]}`, nil)
		},
	)
	seedAI(t, d.State, fm.srv.URL)
	mockRepoDir(md, "abc1234")
	// cappedDiff: staged diff + numstat probes (empty → falls through).
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("diff --cached -U3"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "diff --git a/hello.txt b/hello.txt"}, nil).Maybe()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("diff -U3; true"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "diff --git a/hello.txt b/hello.txt\n+world"}, nil).Maybe()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("ls-files --others"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil).Maybe()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("numstat"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "1\t0\thello.txt\n"}, nil).Maybe()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("status --short --branch"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "## main\n M hello.txt\n"}, nil).Maybe()
	mockOrientation(md, "hello.txt")

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/explain", `{"mode":"working-tree"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("explain: got %d %q, want 202", rec.Code, rec.Body)
	}
	var started struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.ThreadID == "" {
		t.Fatalf("202 body must carry threadId: %q", rec.Body)
	}

	// Busy slot points at the new thread: threads list reports it running.
	rec = authedGet(t, h, cookie, "/api/projects/abc/codemap/threads")
	var list struct {
		RunningThreadId *string `json:"runningThreadId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.RunningThreadId == nil || *list.RunningThreadId != started.ThreadID {
		t.Fatalf("runningThreadId = %v, want %q", list.RunningThreadId, started.ThreadID)
	}

	// The detached run finishes on its own: poll the thread until the
	// turn resolves (sections persisted via CompleteTurn).
	require.Eventually(t, func() bool {
		rec := authedGet(t, h, cookie, "/api/projects/abc/codemap/threads/"+started.ThreadID)
		var th struct {
			Thread struct {
				Turns []struct {
					Error    *string         `json:"error"`
					Sections json.RawMessage `json:"sections"`
				} `json:"turns"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &th); err != nil {
			return false
		}
		if len(th.Thread.Turns) == 0 {
			return false
		}
		last := th.Thread.Turns[len(th.Thread.Turns)-1]
		return last.Error == nil && len(last.Sections) > 0 && string(last.Sections) != "null"
	}, 10*time.Second, 100*time.Millisecond)

	// Busy slot released shortly after the turn persists (the defer runs
	// one line behind CompleteTurn — poll, don't race).
	require.Eventually(t, func() bool {
		rec := authedGet(t, h, cookie, "/api/projects/abc/codemap/threads")
		var list struct {
			RunningThreadId *string `json:"runningThreadId"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			return false
		}
		return list.RunningThreadId == nil
	}, 5*time.Second, 100*time.Millisecond)
}

// TestGitExplainValidation pins the 400 paths: mode=commit without a sha
// and an unknown mode both reject before any thread reservation (zero
// fake-model steps — any model call fails the test).
func TestGitExplainValidation(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	fm := newFakeModel(t) // zero steps: any model call fails the test
	seedAI(t, d.State, fm.srv.URL)
	mockEnsure(md)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// mode=commit without a sha → 400 before any thread is reserved.
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/explain", `{"mode":"commit"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("explain commit w/o sha: got %d, want 400", rec.Code)
	}
	// Invalid mode → 400.
	rec = authedPost(t, h, cookie, "/api/projects/abc/git/explain", `{"mode":"bogus"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("explain bogus mode: got %d, want 400", rec.Code)
	}
}

// TestGitExplainBusy pins the second-fire contract: while one explain run
// holds the busy slot, a second POST gets 409 and starts no thread. The
// model is a plain always-500 server: the detached run fails and releases
// the slot, which also proves a failed fire-and-forget turn stays
// retryable and cannot wedge the busy slot forever.
func TestGitExplainBusy(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model down", http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	seedAI(t, d.State, failing.URL)
	mockRepoDir(md, "abc1234")
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("diff --cached -U3"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "diff --git a/hello.txt b/hello.txt"}, nil).Maybe()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("diff -U3; true"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil).Maybe()
	mockOrientation(md, "hello.txt")

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects/abc/git/explain", `{"mode":"working-tree"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first explain: got %d %q, want 202", rec.Code, rec.Body)
	}

	// Busy slot is taken synchronously, so the second fire 409s
	// immediately — even before the first run finishes.
	rec = authedPost(t, h, cookie, "/api/projects/abc/git/explain", `{"mode":"working-tree"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second explain: got %d %q, want 409", rec.Code, rec.Body)
	}

	// The failing run releases the slot after persisting the failed turn
	// (retry backoff ~8s), proving the slot cannot wedge.
	require.Eventually(t, func() bool {
		rec := authedGet(t, h, cookie, "/api/projects/abc/codemap/threads")
		return !strings.Contains(rec.Body.String(), `"runningThreadId":"`)
	}, 30*time.Second, 250*time.Millisecond)
}

// TestGitStatusUpstream pins the upstream + unborn fields Push gating
// reads: upstream present with counts (or null), unborn false here.
func TestGitStatusUpstream(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("echo STATUS-BEGIN"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "STATUS-BEGIN\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("rev-parse --abbrev-ref HEAD"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "main\n"}, nil).Maybe()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("diff --numstat; true"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("diff --cached --numstat; true"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("rev-parse --abbrev-ref @{u}"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "origin/main\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("rev-list --left-right --count"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "2 3\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", gitCmd("rev-parse --verify --quiet HEAD"), false).
		Return(docker.ExecResult{ExitCode: 0, Output: "0\n"}, nil).Maybe()

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/abc/git/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d %q, want 200", rec.Code, rec.Body)
	}
	var body struct {
		Upstream *struct {
			Name   string `json:"name"`
			Ahead  int    `json:"ahead"`
			Behind int    `json:"behind"`
		} `json:"upstream"`
		Unborn bool `json:"unborn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Upstream == nil || body.Upstream.Name != "origin/main" || body.Upstream.Ahead != 3 || body.Upstream.Behind != 2 {
		t.Fatalf("upstream = %+v, want origin/main ahead=3 behind=2", body.Upstream)
	}
	if body.Unborn {
		t.Fatalf("unborn = true, want false (HEAD resolves)")
	}
}
