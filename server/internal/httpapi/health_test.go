// Desired-vs-live healthcheck tests: the report names every divergence in
// both directions (recorded-but-missing and live-but-unrecorded) and the
// check itself changes nothing. Repair tests pin what the Fix action heals
// (container/repo/harness) versus what stays manual (sessions).
package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/harness"
)

type healthBody struct {
	Project string `json:"project"`
	InSync  bool   `json:"inSync"`
	Checks  []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		State  string `json:"state"`
		System string `json:"system"`
		Fix    string `json:"fix"`
		Detail string `json:"detail"`
	} `json:"checks"`
	Actions []struct {
		Name   string `json:"name"`
		Action string `json:"action"`
		Detail string `json:"detail"`
	} `json:"actions"`
}

func getHealth(t *testing.T, h http.Handler, cookie *http.Cookie) healthBody {
	t.Helper()
	rec := authedPost(t, h, cookie, "/api/projects/abc/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health: got %d %q, want 200", rec.Code, rec.Body)
	}
	var rep healthBody
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func repairHealth(t *testing.T, h http.Handler, cookie *http.Cookie) healthBody {
	t.Helper()
	rec := authedPost(t, h, cookie, "/api/projects/abc/health/repair", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("repair: got %d %q, want 200", rec.Code, rec.Body)
	}
	var rep healthBody
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func healthByName(rep healthBody) map[string]string {
	out := map[string]string{}
	for _, c := range rep.Checks {
		out[c.Name] = c.Status
	}
	return out
}

// Fully converged project: container running, repo cloned, every recorded
// session live, every recorded harness present — zero drifts.
func TestProjectHealthInSync(t *testing.T) {
	d, md, dataDir := newSessionDeps(t)
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
		[]string{"bash", "-lc", harnessPath + `for c in fakecli bash; do command -v "$c" >/dev/null && echo "$c"; done`}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "fakecli\nbash\n"}, nil).Once()

	h := New(d)
	cookie := loginCookie(t, h)
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
	d, md, dataDir := newSessionDeps(t)
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
		[]string{"bash", "-lc", harnessPath + `for c in fakecli; do command -v "$c" >/dev/null && echo "$c"; done`}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "\n"}, nil).Once()

	h := New(d)
	cookie := loginCookie(t, h)
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

// Repair heals the safe drift classes: a recorded harness whose binary is
// missing is reinstalled through the boot path, and a present-but-unrecorded
// binary gets its install record written. The refreshed report must be in
// sync, with one action per row.
func TestProjectHealthRepairHealsHarnessDrift(t *testing.T) {
	d, md, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	if err := d.Projects.RecordInstall("abc", "fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Harnesses.Save(harness.Harness{Name: "Helper", Command: "helpercli"}); err != nil {
		t.Fatal(err)
	}

	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Times(2)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", "ls -A /workspace/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "src\n"}, nil).Times(2)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil).Times(2)
	// initial check: only the unrecorded binary is present; re-check: both
	probe := []string{"bash", "-lc", harnessPath + `for c in fakecli helpercli; do command -v "$c" >/dev/null && echo "$c"; done`}
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", probe, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "helpercli\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", probe, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "fakecli\nhelpercli\n"}, nil).Once()

	// Recorded-but-missing harness: probe miss -> install -> re-probe hit -> validate.
	lookup := []string{"bash", "-lc", harnessPath + "command -v fakecli"}
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", lookup, false).
		Return(docker.ExecResult{ExitCode: 1}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", harnessPath + "npm i -g fakecli"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "added 1 package\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", lookup, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "/usr/bin/fakecli\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", harnessPath + "fakecli --version || fakecli --help"}, true).
		Return(docker.ExecResult{ExitCode: 0, Output: "fakecli 1.0\n"}, nil).Once()

	h := New(d)
	cookie := loginCookie(t, h)
	rep := repairHealth(t, h, cookie)
	if !rep.InSync {
		t.Fatalf("inSync = false after repair: %+v", rep.Checks)
	}
	if len(rep.Actions) != 2 {
		t.Fatalf("actions = %+v, want two", rep.Actions)
	}
	if rep.Actions[0].Name != "harness:fake" || rep.Actions[0].Action != "reconciled" {
		t.Fatalf("harness:fake action = %+v, want reconciled", rep.Actions[0])
	}
	if rep.Actions[1].Name != "harness:helper" || rep.Actions[1].Action != "recorded" {
		t.Fatalf("harness:helper action = %+v, want recorded", rep.Actions[1])
	}
}

// An empty repo volume is healed by the same boot path: re-clone from the
// recorded repo URL, then the refreshed report is in sync.
func TestProjectHealthRepairReclonesEmptyRepo(t *testing.T) {
	d, md, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Times(3)
	repoCheck := []string{"bash", "-lc", "ls -A /workspace/repo"}
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", repoCheck, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", repoCheck, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "package.json\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"ls", "-A", "/workspace"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"git", "clone", "https://github.com/x/hello.git", "/workspace/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "Cloning into...\n"}, nil).Once()
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil).Times(2)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", harnessPath + `for c in fakecli; do command -v "$c" >/dev/null && echo "$c"; done`}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil).Times(2)

	h := New(d)
	cookie := loginCookie(t, h)
	rep := repairHealth(t, h, cookie)
	if !rep.InSync {
		t.Fatalf("inSync = false after re-clone: %+v", rep.Checks)
	}
	if len(rep.Actions) != 1 || rep.Actions[0].Name != "repo" || rep.Actions[0].Action != "reconciled" {
		t.Fatalf("actions = %+v, want repo reconciled", rep.Actions)
	}
}

// Session drift is reported, never auto-repaired: relaunching a terminal or
// killing a live one is the user's decision. Repair returns manual actions
// and leaves the report drifting.
func TestProjectHealthRepairLeavesSessionsManual(t *testing.T) {
	d, md, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")
	if err := d.Projects.RecordSession("abc", "ghost-1", "fake"); err != nil {
		t.Fatal(err)
	}

	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Times(2)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", "ls -A /workspace/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "src\n"}, nil).Times(2)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "stray-1\n"}, nil).Times(2)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"bash", "-lc", harnessPath + `for c in fakecli; do command -v "$c" >/dev/null && echo "$c"; done`}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil).Times(2)

	h := New(d)
	cookie := loginCookie(t, h)
	rep := repairHealth(t, h, cookie)
	if rep.InSync {
		t.Fatal("inSync = true, want false (session drifts stay)")
	}
	if len(rep.Actions) != 2 {
		t.Fatalf("actions = %+v, want two manual", rep.Actions)
	}
	for _, a := range rep.Actions {
		if a.Action != "manual" {
			t.Fatalf("action %+v, want manual", a)
		}
	}
}

// Missing container: the container row itself is the drift, everything
// exec-based degrades to unknown rather than guessing.
func TestProjectHealthMissingContainer(t *testing.T) {
	d, md, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{}, docker.ErrNotFound).Once()

	h := New(d)
	cookie := loginCookie(t, h)
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
