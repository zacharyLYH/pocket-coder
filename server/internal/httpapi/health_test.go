// Desired-vs-live healthcheck tests: the report must name every divergence
// in both directions (recorded-but-missing and live-but-unrecorded) while
// changing nothing.
package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/harness"
)

type healthReport struct {
	Project string `json:"project"`
	InSync  bool   `json:"inSync"`
	Checks  []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		State  string `json:"state"`
		System string `json:"system"`
		Detail string `json:"detail"`
	} `json:"checks"`
}

func getHealth(t *testing.T, h http.Handler, cookie *http.Cookie) healthReport {
	t.Helper()
	rec := authedPost(t, h, cookie, "/api/projects/abc/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health: got %d %q, want 200", rec.Code, rec.Body)
	}
	var rep healthReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func healthByName(rep healthReport) map[string]string {
	out := map[string]string{}
	for _, c := range rep.Checks {
		out[c.Name] = c.Status
	}
	return out
}

// Fully converged project: container running, repo cloned, every recorded
// session live, every recorded harness present — zero drifts.
func TestProjectHealthInSync(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	// The built-in shell mirrors production's seeded Terminal entry: always
	// present, deliberately never recorded — it must not appear at all,
	// neither ok nor drift.
	if _, err := d.Harnesses.Save(harness.Harness{Name: "Terminal", Command: "bash"}); err != nil {
		t.Fatal(err)
	}
	_ = d.Projects.RecordSession("abc", "main", "")
	_ = d.Projects.RecordSession("abc", "oc-1", "fake")
	if err := d.Projects.RecordInstall("abc", "fake"); err != nil {
		t.Fatal(err)
	}

	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", "ls -A /workspace/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "package.json\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "main\noc-1\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", `for c in fakecli bash; do command -v "$c" >/dev/null && echo "$c"; done`}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "fakecli\nbash\n"}, nil).Once()

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rep := getHealth(t, h, cookie)
	if !rep.InSync {
		t.Fatalf("inSync = false, want true: %+v", rep.Checks)
	}
	got := healthByName(rep)
	for _, name := range []string{"container", "repo", "session:main", "session:oc-1", "harness:fake"} {
		if got[name] != "ok" {
			t.Fatalf("%s = %q, want ok (full map: %v)", name, got[name], got)
		}
	}
	if _, ok := got["harness:terminal"]; ok {
		t.Fatalf("the built-in shell must not be health-checked at all, got %v", got)
	}
}

// Every divergence class at once: recorded session gone from tmux, live
// session with no record, recorded harness binary missing, empty repo
// volume — all drift, container itself ok.
func TestProjectHealthFindsDriftBothWays(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	_ = d.Projects.RecordSession("abc", "ghost-1", "fake")
	if err := d.Projects.RecordInstall("abc", "fake"); err != nil {
		t.Fatal(err)
	}

	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", "ls -A /workspace/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "stray-1\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", `for c in fakecli; do command -v "$c" >/dev/null && echo "$c"; done`}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "\n"}, nil).Once()

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rep := getHealth(t, h, cookie)
	if rep.InSync {
		t.Fatal("inSync = true, want false")
	}
	got := healthByName(rep)
	want := map[string]string{
		"container":       "ok",
		"repo":            "drift",
		"session:ghost-1": "drift",
		"session:stray-1": "drift",
		"harness:fake":    "drift",
	}
	for name, status := range want {
		if got[name] != status {
			t.Fatalf("%s = %q, want %q (full map: %v)", name, got[name], status, got)
		}
	}
}

// Missing container: the container row itself is the drift, everything
// exec-based degrades to unknown rather than guessing.
func TestProjectHealthMissingContainer(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{}, docker.ErrNotFound).Once()

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rep := getHealth(t, h, cookie)
	if rep.InSync {
		t.Fatal("inSync = true, want false")
	}
	got := healthByName(rep)
	if got["container"] != "drift" {
		t.Fatalf("container = %q, want drift", got["container"])
	}
	for _, name := range []string{"repo", "sessions", "harnesses"} {
		if got[name] != "unknown" {
			t.Fatalf("%s = %q, want unknown", name, got[name])
		}
	}
}
