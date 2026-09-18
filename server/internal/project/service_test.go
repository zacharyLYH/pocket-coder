package project

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/events"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
	dockermocks "pcoder/mocks/docker"
)

const testRepo = "https://github.com/x/hello.git"

// newService builds a Service over a real temp state file + event log and a
// mocked Docker client, so pipeline behavior is exercised end to end
// without an engine.
func newService(t *testing.T) (*Service, *dockermocks.MockClient, *state.Store, string) {
	t.Helper()
	dataDir := t.TempDir()
	st, err := state.Open(dataDir, state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := events.Open(filepath.Join(dataDir, "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ev.Close() })
	d := dockermocks.NewMockClient(t)
	return NewService(Open(st), d, ev), d, st, dataDir
}

func expectProjectReady(d *dockermocks.MockClient, id *string) {
	d.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	d.EXPECT().InspectImage(mock.Anything, ProjectImage).Return(nil)
	d.EXPECT().Run(mock.Anything, mock.MatchedBy(func(sp docker.Spec) bool {
		*id = sp.Name
		return strings.HasPrefix(sp.Name, "pcoder-") && sp.Image == ProjectImage &&
			sp.Writable && len(sp.Volumes) == 2
	})).Return("cid123", nil)
}

// expectReconcile pins the missing-container recovery chain: first Inspect
// misses, the container is recreated, the second Inspect reports running.
func expectReconcile(d *dockermocks.MockClient) {
	d.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{}, docker.ErrNotFound).Once()
	d.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	d.EXPECT().InspectImage(mock.Anything, ProjectImage).Return(nil)
	d.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	d.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Once()
}

func eventsOf(t *testing.T, s *Service) []events.Event {
	t.Helper()
	l, ok := s.ev.(*events.Log)
	if !ok {
		t.Fatalf("service event log is %T", s.ev)
	}
	evs, err := l.Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func types(evs []events.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func TestCreateDerivesIDFromRepo(t *testing.T) {
	s, d, _, _ := newService(t)
	var name string
	expectProjectReady(d, &name)
	d.EXPECT().Exec(mock.Anything, "cid123",
		[]string{"git", "clone", testRepo, repoTarget + "/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	id, p, err := s.Create(t.Context(), testRepo, "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "x/hello" {
		t.Fatalf("id = %q, want x/hello", id)
	}
	want := Project{Repo: testRepo, Branch: "", CloneMethod: "http"}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("project = %+v, want %+v", p, want)
	}
	if name != "pcoder-x-hello" {
		t.Fatalf("container name = %q, want pcoder-x-hello", name)
	}
	gotTypes := types(eventsOf(t, s))
	joined := strings.Join(gotTypes, ",")
	if gotTypes[len(gotTypes)-1] != "project.ready" || !strings.Contains(joined, "project.create") || !strings.Contains(joined, "project.clone") {
		t.Fatalf("unexpected events: %v", gotTypes)
	}
}

func TestCreateRequiresGitHubRepo(t *testing.T) {
	s, _, _, _ := newService(t)
	for _, tc := range []struct{ repo, branch string }{
		{"", ""},
		{"   ", ""},
		{"https://example.com/x/hello.git", ""},
		{"git@gitlab.com:x/hello.git", ""},
		{"https://github.com/onlyone.git", ""},
		{"not a url at all", ""},
	} {
		_, _, err := s.Create(t.Context(), tc.repo, tc.branch, "")
		if err == nil || !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Create(%q) err = %v, want ErrInvalidInput", tc.repo, err)
		}
	}
	if entries, _ := s.List(); len(entries) != 0 {
		t.Fatalf("rejected creates must leave no metadata: %+v", entries)
	}
}

func TestCreateDuplicateRepoIsConflict(t *testing.T) {
	s, d, _, _ := newService(t)
	var name string
	expectProjectReady(d, &name)
	d.EXPECT().Exec(mock.Anything, "cid123", mock.Anything, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	if _, _, err := s.Create(t.Context(), testRepo, "", ""); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, _, err := s.Create(t.Context(), testRepo, "", "")
	if err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("second create err = %v, want ErrConflict", err)
	}
	// same repo with different URL spellings is the same project
	_, _, err = s.Create(t.Context(), "git@github.com:x/hello.git", "", "")
	if err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("alias create err = %v, want ErrConflict", err)
	}
}

func TestCreateClonesInsideContainer(t *testing.T) {
	for _, tc := range []struct{ branch string }{{""}, {"main"}} {
		s, d, _, _ := newService(t)
		var cid string
		expectProjectReady(d, &cid)

		wantArgs := []string{"git", "clone"}
		if tc.branch != "" {
			wantArgs = append(wantArgs, "--branch", tc.branch, "--single-branch")
		}
		wantArgs = append(wantArgs, testRepo, repoTarget+"/repo")
		d.EXPECT().Exec(mock.Anything, "cid123", wantArgs, false).
			Return(docker.ExecResult{ExitCode: 0}, nil)

		id, p, err := s.Create(t.Context(), testRepo, tc.branch, "")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		wantBranch := tc.branch
		want := Project{Repo: testRepo, Branch: wantBranch, CloneMethod: "http"}
		if !reflect.DeepEqual(p, want) {
			t.Fatalf("project = %+v, want %+v", p, want)
		}
		if id != "x/hello" {
			t.Fatalf("id = %q, want x/hello", id)
		}
		d.EXPECT().Inspect(mock.Anything, ContainerName(id)).Return(docker.Container{Running: true, Status: "running"}, nil)
		if _, status, err := s.Get(t.Context(), id); err != nil || status.State != "running" {
			t.Fatalf("get: %v %+v", err, status)
		}
		if evs := eventsOf(t, s); !strings.Contains(strings.Join(types(evs), ","), "project.clone") {
			t.Fatalf("missing clone event: %v", types(evs))
		}
	}
}

func TestCreateCloneFailureKeepsProject(t *testing.T) {
	s, d, _, _ := newService(t)
	var cname string
	expectProjectReady(d, &cname)
	d.EXPECT().Exec(mock.Anything, "cid123", []string{"git", "clone", testRepo, repoTarget + "/repo"}, false).
		Return(docker.ExecResult{ExitCode: 128, Output: "fatal: repository not found"}, nil)

	_, _, err := s.Create(t.Context(), testRepo, "", "")
	want := "clone " + testRepo + ": fatal: repository not found"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	entries, listErr := s.List()
	if listErr != nil || len(entries) != 1 {
		t.Fatalf("project should survive clone failure: %v %v", entries, listErr)
	}
	foundErr := false
	for _, e := range eventsOf(t, s) {
		if e.Type == "error" && e.Data["op"] == "project.clone" {
			foundErr = true
		}
	}
	if !foundErr {
		t.Fatal("no error event for failed clone")
	}
}

func TestCreateBuildsMissingProjectImage(t *testing.T) {
	s, d, _, _ := newService(t)
	d.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	d.EXPECT().InspectImage(mock.Anything, ProjectImage).
		Return(fmt.Errorf("inspect: %w", docker.ErrNotFound))
	d.EXPECT().Build(mock.Anything, mock.MatchedBy(func(o docker.BuildOptions) bool {
		return o.Tag == ProjectImage && o.InputStream != nil
	}), mock.Anything).Return(nil)
	d.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	d.EXPECT().Exec(mock.Anything, "cid", mock.Anything, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	if _, _, err := s.Create(t.Context(), testRepo, "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
}

func TestCreateRunFailureCleansUpMetadata(t *testing.T) {
	s, d, _, _ := newService(t)
	d.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	d.EXPECT().InspectImage(mock.Anything, ProjectImage).Return(nil)
	d.EXPECT().Run(mock.Anything, mock.Anything).Return("", errors.New("engine on fire"))

	if _, _, err := s.Create(t.Context(), testRepo, "", ""); err == nil {
		t.Fatal("expected run failure")
	}
	if entries, _ := s.List(); len(entries) != 0 {
		t.Fatalf("metadata not cleaned up: %+v", entries)
	}
}

func TestCreateRejectsOptionInjection(t *testing.T) {
	s, _, _, _ := newService(t)
	for _, bad := range [][2]string{{"--upload-pack=evil", ""}, {"", "-oProxyCommand=x"}} {
		_, _, err := s.Create(t.Context(), bad[0], bad[1], "")
		want := "invalid input: repo url and branch must not start with \"-\""
		if err == nil || err.Error() != want {
			t.Fatalf("Create(%+v) err = %v, want %q", bad, err, want)
		}
	}
}

func TestStartStopRestartEvents(t *testing.T) {
	s, d, _, _ := newService(t)
	var cname string
	expectProjectReady(d, &cname)
	d.EXPECT().Exec(mock.Anything, "cid123", mock.Anything, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	id, _, err := s.Create(t.Context(), testRepo, "", "")
	if err != nil {
		t.Fatal(err)
	}

	d.EXPECT().Stop(mock.Anything, ContainerName(id), stopWait).Return(nil)
	d.EXPECT().Start(mock.Anything, ContainerName(id)).Return(nil)
	if err := s.Restart(t.Context(), id); err != nil {
		t.Fatalf("restart: %v", err)
	}

	got := types(eventsOf(t, s))
	joined := strings.Join(got, ",")
	for _, want := range []string{"project.stop", "project.start"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, got)
		}
	}
}

func TestStopToleratesMissingContainer(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Repo: testRepo}); err != nil {
		t.Fatal(err)
	}
	wrapped := fmt.Errorf("stop: %w", docker.ErrNotFound)
	d.EXPECT().Stop(mock.Anything, "pcoder-abc", stopWait).Return(wrapped)
	if err := s.Stop(t.Context(), "abc"); err != nil {
		t.Fatalf("stop missing container should be idempotent: %v", err)
	}
}

func TestDeleteScopes(t *testing.T) {
	cases := []struct {
		scope           Scope
		removeContainer bool
		home            bool
		repo            bool
		metadata        bool
	}{
		{ScopeContainer, true, true, false, false},
		{ScopeRepo, true, false, true, false},
		{ScopeMetadata, false, false, false, true},
		{ScopeAll, true, true, true, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.scope), func(t *testing.T) {
			s, d, _, _ := newService(t)
			if err := s.store.Create("abc", Project{Repo: testRepo}); err != nil {
				t.Fatal(err)
			}
			// volume-taking scopes stop the container first (best effort)
			if tc.scope != ScopeMetadata {
				d.EXPECT().Stop(mock.Anything, "pcoder-abc", stopWait).Return(nil)
			}
			if tc.removeContainer {
				d.EXPECT().Remove(mock.Anything, "pcoder-abc", true).Return(nil)
			}
			if tc.home {
				d.EXPECT().RemoveVolume(mock.Anything, "pcoder-abc-home").Return(nil)
			}
			if tc.repo {
				d.EXPECT().RemoveVolume(mock.Anything, "pcoder-abc-repo").Return(nil)
			}
			if err := s.Delete(t.Context(), "abc", tc.scope); err != nil {
				t.Fatalf("delete: %v", err)
			}
			_, getErr := s.store.Get("abc")
			metadataGone := getErr != nil
			if metadataGone != tc.metadata {
				t.Fatalf("metadata gone = %v, want %v", metadataGone, tc.metadata)
			}
			evs := eventsOf(t, s)
			last := evs[len(evs)-1]
			if last.Type != "project.delete" || last.Data["scope"] != string(tc.scope) {
				t.Fatalf("delete event: %+v", last)
			}
		})
	}
}

func TestDeleteValidation(t *testing.T) {
	s, _, _, _ := newService(t)
	if err := s.Delete(t.Context(), "abc", "nope"); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("err = %v, want ErrInvalidScope", err)
	}
	if err := s.Delete(t.Context(), "ghost", ScopeAll); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, _, err := s.Get(t.Context(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get ghost: %v, want ErrNotFound", err)
	}
}

func TestParseRepoID(t *testing.T) {
	cases := map[string]string{
		testRepo:                           "x/hello",
		"https://github.com/x/hello":       "x/hello",
		"https://github.com/x/hello/":      "x/hello",
		"https://github.com/X/Hello.git":   "x/hello",
		"http://github.com/x/hello.git":    "x/hello",
		"github.com/x/hello":               "x/hello",
		"git@github.com:x/hello.git":       "x/hello",
		"git@github.com:X/Hello":           "x/hello",
		"ssh://git@github.com/x/hello.git": "x/hello",
	}
	for raw, want := range cases {
		if got, err := parseRepoID(raw, false); err != nil || got != want {
			t.Errorf("parseRepoID(%q, false) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, bad := range []string{
		"",
		"   ",
		"https://example.com/x/hello.git",
		"git@gitlab.com:x/hello.git",
		"https://github.com/onlyone",
		"https://github.com//.git",
		"https://github.com/x/hello/extra/path",
		"just some words",
		"--upload-pack=evil",
	} {
		if got, err := parseRepoID(bad, false); err == nil || !errors.Is(err, ErrInvalidInput) {
			t.Errorf("parseRepoID(%q, false) = %q, %v; want ErrInvalidInput", bad, got, err)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"x/hello":       "x-hello",
		"Org/Repo.Name": "org-repo.name",
		"a/b_c-d.e":     "a-b_c-d.e",
		"abc":           "abc",
	}
	for id, want := range cases {
		if got := SanitizeName(id); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", id, got, want)
		}
	}
	if got := ContainerName("x/hello"); got != "pcoder-x-hello" {
		t.Errorf("ContainerName = %q, want pcoder-x-hello", got)
	}
}

func TestCreateCloneExecErrorSurfaces(t *testing.T) {
	s, d, _, _ := newService(t)
	var cname string
	expectProjectReady(d, &cname)
	d.EXPECT().Exec(mock.Anything, "cid123", mock.Anything, false).
		Return(docker.ExecResult{}, errors.New("exec infra exploded"))

	_, _, err := s.Create(t.Context(), testRepo, "", "")
	want := "clone " + testRepo + ": exec infra exploded"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestStartMissingContainerPropagates(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Repo: testRepo}); err != nil {
		t.Fatal(err)
	}
	// metadata exists but the container is gone (scope=container deleted):
	// Start must surface the engine's not-found so HTTP maps it to 404.
	wrapped := fmt.Errorf("start: %w", docker.ErrNotFound)
	d.EXPECT().Start(mock.Anything, "pcoder-abc").Return(wrapped)
	if err := s.Start(t.Context(), "abc"); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("err = %v, want docker.ErrNotFound", err)
	}

	// Restart tolerates the stop but still fails at start.
	d.EXPECT().Stop(mock.Anything, "pcoder-abc", stopWait).Return(nil)
	d.EXPECT().Start(mock.Anything, "pcoder-abc").Return(wrapped)
	if err := s.Restart(t.Context(), "abc"); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("restart err = %v, want docker.ErrNotFound", err)
	}
}

func TestDeletePartialFailureReportsFirstError(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Repo: testRepo}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Stop(mock.Anything, "pcoder-abc", stopWait).Return(nil)
	first := fmt.Errorf("remove: %w", docker.ErrNotFound)
	d.EXPECT().Remove(mock.Anything, "pcoder-abc", true).Return(first)
	d.EXPECT().RemoveVolume(mock.Anything, "pcoder-abc-home").Return(errors.New("second"))

	err := s.Delete(t.Context(), "abc", ScopeContainer)
	if !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("err = %v, want first failure", err)
	}
	// expected event-log state after a failed delete: no project.delete line
	deleteEvents := 0
	for _, e := range eventsOf(t, s) {
		if e.Type == "project.delete" {
			deleteEvents++
		}
	}
	if deleteEvents != 0 {
		t.Fatalf("got %d project.delete events, want 0", deleteEvents)
	}
}

func TestDeleteAllKeepsRecordWhenDockerCleanupFails(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Repo: testRepo}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Stop(mock.Anything, "pcoder-abc", stopWait).Return(nil)
	d.EXPECT().Remove(mock.Anything, "pcoder-abc", true).Return(nil)
	d.EXPECT().RemoveVolume(mock.Anything, "pcoder-abc-home").Return(errors.New("volume busy"))
	d.EXPECT().RemoveVolume(mock.Anything, "pcoder-abc-repo").Return(nil)

	if err := s.Delete(t.Context(), "abc", ScopeAll); err == nil {
		t.Fatal("expected the volume failure to surface")
	}
	// The record must survive so the project stays listed (and deletable
	// on retry) instead of becoming an invisible volume orphan.
	if _, err := s.store.Get("abc"); err != nil {
		t.Fatalf("record dropped despite failed cleanup: %v", err)
	}
}

func TestEnsureContainerRunningIsNoop(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Repo: testRepo}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Once()
	if _, err := s.EnsureContainer(t.Context(), "abc"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureContainerMissingReconciles(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Repo: testRepo}); err != nil {
		t.Fatal(err)
	}
	expectReconcile(d)
	if st, err := s.EnsureContainer(t.Context(), "abc"); err != nil || st.State != StateRunning {
		t.Fatalf("st=%+v err=%v", st, err)
	}
}

func TestEnsureContainerExitedIsNoop(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Repo: testRepo}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: false, Status: "exited"}, nil)
	if _, err := s.EnsureContainer(t.Context(), "abc"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureContainerNotFoundReturnsErrNotFound(t *testing.T) {
	s, _, _, _ := newService(t)
	_, err := s.EnsureContainer(t.Context(), "ghost")
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestEnsureContainerReconcileEvent(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Repo: testRepo}); err != nil {
		t.Fatal(err)
	}
	expectReconcile(d)
	if _, err := s.EnsureContainer(t.Context(), "abc"); err != nil {
		t.Fatal(err)
	}
	evs := eventsOf(t, s)
	found := false
	for _, e := range evs {
		if e.Type == "project.reconcile" && e.Data["id"] == "abc" {
			found = true
		}
	}
	if !found {
		t.Fatal("no project.reconcile event")
	}
}

// EnsureContainer is container-only now: a mid-run recreate must NOT touch
// the repo volume (code lives there and survives) — no clone, no installs.
// Any unexpected Exec fails the mock.
func TestEnsureContainerRecreateIsContainerOnly(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{
		Repo:      testRepo,
		Harnesses: []string{"opencode"},
	}); err != nil {
		t.Fatal(err)
	}
	expectReconcile(d)

	inst := &fakeInstaller{}
	s.SetInstaller(inst)

	if _, err := s.EnsureContainer(t.Context(), "abc"); err != nil {
		t.Fatal(err)
	}
	if len(inst.installed) != 0 {
		t.Fatalf("EnsureContainer installed %v — installs are boot's job", inst.installed)
	}
}

// fakeInstaller records which harnesses were installed. Implements Installer.
type fakeInstaller struct {
	installed []string
	errOn     map[string]error
}

func (f *fakeInstaller) InstallHarness(ctx context.Context, _ string, harnessID string) error {
	f.installed = append(f.installed, harnessID)
	if err := f.errOn[harnessID]; err != nil {
		return err
	}
	return nil
}

// unknownSkipInstaller mirrors the real harnessInstaller: an unknown harness
// id ("no such harness") is silently skipped, anything else is an error.
type unknownSkipInstaller struct {
	installed []string
}

func (u *unknownSkipInstaller) InstallHarness(ctx context.Context, _ string, harnessID string) error {
	u.installed = append(u.installed, harnessID)
	if harnessID == "ghost" {
		return fmt.Errorf("no such harness %q", harnessID)
	}
	return nil
}

func TestBringAllUpInstallsRecordedHarnesses(t *testing.T) {
	s, d, _, _ := newService(t)
	// Project with two installed harnesses, container missing → full
	// provisioning at boot.
	if err := s.store.Create("abc", Project{
		Harnesses: []string{"opencode", "freebuff"},
	}); err != nil {
		t.Fatal(err)
	}
	expectReconcile(d)

	inst := &fakeInstaller{}
	s.SetInstaller(inst)

	if err := s.BringAllUp(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(inst.installed) != 2 {
		t.Fatalf("installed %d harnesses, want 2: %v", len(inst.installed), inst.installed)
	}
	if inst.installed[0] != "opencode" || inst.installed[1] != "freebuff" {
		t.Fatalf("installed = %v, want [opencode freebuff]", inst.installed)
	}
}

// Fresh-engine recovery is boot's job: BringAllUp re-clones an empty repo
// volume from the recorded URL and installs the recorded harnesses.
func TestBringAllUpReclonesEmptyRepoVolume(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{
		Repo:      testRepo,
		Harnesses: []string{"opencode"},
	}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{}, docker.ErrNotFound).Once()
	var cid string
	expectProjectReady(d, &cid)
	// fresh engine: the repo volume comes up empty...
	d.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"ls", "-A", repoTarget}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	// ...so boot re-clones the repo from state.json
	d.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"git", "clone", testRepo, repoTarget + "/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	d.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Once()

	inst := &fakeInstaller{}
	s.SetInstaller(inst)

	if err := s.BringAllUp(t.Context()); err != nil {
		t.Fatal(err)
	}
	evs := eventsOf(t, s)
	var reconciled, cloned bool
	for _, e := range evs {
		if e.Type == "project.reconcile" && e.Data["id"] == "abc" {
			reconciled = true
		}
		if e.Type == "project.clone" && e.Data["id"] == "abc" {
			cloned = true
		}
	}
	if !reconciled || !cloned {
		t.Fatalf("events: reconcile=%v clone=%v", reconciled, cloned)
	}
	if len(inst.installed) != 1 || inst.installed[0] != "opencode" {
		t.Fatalf("installed = %v, want [opencode]", inst.installed)
	}
}

// BringAllUp starts a stopped (exited) container too: boot must leave every
// project's container running, not just the ones Docker still has up.
func TestBringAllUpStartsExitedContainer(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Harnesses: []string{"opencode"}}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: false, Status: "exited"}, nil).Once()
	d.EXPECT().Start(mock.Anything, "pcoder-abc").Return(nil)

	inst := &fakeInstaller{}
	s.SetInstaller(inst)

	if err := s.BringAllUp(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(inst.installed) != 1 || inst.installed[0] != "opencode" {
		t.Fatalf("installed = %v, want [opencode]", inst.installed)
	}
}

// BringAllUp on a healthy engine (containers already running) is a no-op:
// no downloads, no restarts, just probes.
func TestBringAllUpHealthyIsProbeOnly(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Harnesses: []string{"opencode"}}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Once()

	inst := &fakeInstaller{}
	s.SetInstaller(inst)

	if err := s.BringAllUp(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(inst.installed) != 1 {
		t.Fatalf("installed = %v, want the recorded harness probed once", inst.installed)
	}
}

// An install failure inside BringAllUp is reported but never aborts the
// remaining projects.
func TestBringAllUpContinuesPastInstallFailure(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Harnesses: []string{"broken"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Create("def", Project{Harnesses: []string{"opencode"}}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil).Once()
	d.EXPECT().Inspect(mock.Anything, "pcoder-def").Return(docker.Container{Running: true}, nil).Once()

	inst := &fakeInstaller{errOn: map[string]error{"broken": errors.New("npm registry down")}}
	s.SetInstaller(inst)

	err := s.BringAllUp(t.Context())
	if err == nil || !strings.Contains(err.Error(), "npm registry down") {
		t.Fatalf("err = %v, want the install failure", err)
	}
	if len(inst.installed) != 2 {
		t.Fatalf("installed = %v, want both projects attempted", inst.installed)
	}
}

// Unknown recorded harness ids (deleted from the registry) are skipped by
// the bootstrap installer, not surfaced as failures.
func TestInstallRecordedHarnessesSkipsUnknown(t *testing.T) {
	s, _, _, _ := newService(t)
	// unknownSkipInstaller returns "no such harness" for the id "ghost",
	// mirroring the real harnessInstaller's unknown-id path.
	unknown := &unknownSkipInstaller{}
	s.SetInstaller(unknown)

	if err := s.installRecordedHarnesses(t.Context(), "abc", "cid", []string{"ghost", "opencode"}); err != nil {
		t.Fatalf("err = %v, want nil (unknown ids skipped)", err)
	}
	if len(unknown.installed) != 2 {
		t.Fatalf("installed = %v, want both attempted", unknown.installed)
	}
}

func TestCreateCloneMethodSSH(t *testing.T) {
	s, d, st, _ := newService(t)
	var cid string
	expectProjectReady(d, &cid)

	// set up ssh key store so injectSSHKeys writes authorized_keys
	s.sshKeys = sshkeys.New(st)
	if _, err := s.sshKeys.Add("me@example.com", "ssh-ed25519 AAAA-testkey", "test"); err != nil {
		t.Fatal(err)
	}
	// injectSSHKeys runs mkdir -p /root/.ssh + chmod + write authorized_keys
	d.EXPECT().Exec(mock.Anything, "cid123",
		[]string{"sh", "-c", "mkdir -p /root/.ssh && chmod 700 /root/.ssh"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	d.EXPECT().WriteFile(mock.Anything, "cid123", "/root/.ssh/authorized_keys",
		[]byte("ssh-ed25519 AAAA-testkey\n")).Return(nil)
	// git clone
	d.EXPECT().Exec(mock.Anything, "cid123",
		[]string{"git", "clone", testRepo, repoTarget + "/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	id, p, err := s.Create(t.Context(), testRepo, "", "ssh")
	if err != nil {
		t.Fatal(err)
	}
	if p.CloneMethod != "ssh" {
		t.Fatalf("CloneMethod = %q, want ssh", p.CloneMethod)
	}
	d.EXPECT().Inspect(mock.Anything, ContainerName(id)).Return(docker.Container{Running: true}, nil)
	if _, status, err := s.Get(t.Context(), id); err != nil || status.State != "running" {
		t.Fatalf("get: %v %+v", err, status)
	}
}

func TestCreateCloneMethodInvalid(t *testing.T) {
	s, _, _, _ := newService(t)
	_, _, err := s.Create(t.Context(), testRepo, "", "ftp")
	if err == nil {
		t.Fatal("expected error for invalid cloneMethod")
	}
}
