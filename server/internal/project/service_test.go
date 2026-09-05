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

	"sps/internal/docker"
	"sps/internal/events"
	"sps/internal/sshkeys"
	"sps/internal/state"
	dockermocks "sps/mocks/docker"
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
		return strings.HasPrefix(sp.Name, "sps-") && sp.Image == ProjectImage &&
			sp.Writable && len(sp.Volumes) == 2
	})).Return("cid123", nil)
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

func TestCreateBlank(t *testing.T) {
	s, d, _, _ := newService(t)
	var name string
	expectProjectReady(d, &name)

	id, p, err := s.Create(t.Context(), "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(id) != 8 {
		t.Fatalf("id = %q, want 8 hex chars", id)
	}
	want := Project{Name: "untitled", Repo: "", Branch: "", CloneMethod: "http"}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("project = %+v, want %+v", p, want)
	}
	if name != "sps-"+id {
		t.Fatalf("container name = %q, want sps-%s", name, id)
	}
	gotTypes := types(eventsOf(t, s))
	if gotTypes[len(gotTypes)-2] != "project.create" || gotTypes[len(gotTypes)-1] != "project.ready" {
		t.Fatalf("unexpected events: %v", gotTypes)
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
		want := Project{Name: "hello", Repo: testRepo, Branch: wantBranch, CloneMethod: "http"}
		if !reflect.DeepEqual(p, want) {
			t.Fatalf("project = %+v, want %+v", p, want)
		}
		d.EXPECT().Inspect(mock.Anything, "sps-"+id).Return(docker.Container{Running: true, Status: "running"}, nil)
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

	if _, _, err := s.Create(t.Context(), "", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
}

func TestCreateRunFailureCleansUpMetadata(t *testing.T) {
	s, d, _, _ := newService(t)
	d.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	d.EXPECT().InspectImage(mock.Anything, ProjectImage).Return(nil)
	d.EXPECT().Run(mock.Anything, mock.Anything).Return("", errors.New("engine on fire"))

	if _, _, err := s.Create(t.Context(), "", "", ""); err == nil {
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
	id, _, err := s.Create(t.Context(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	d.EXPECT().Stop(mock.Anything, "sps-"+id, stopWait).Return(nil)
	d.EXPECT().Start(mock.Anything, "sps-"+id).Return(nil)
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
	if err := s.store.Create("abc", Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	wrapped := fmt.Errorf("stop: %w", docker.ErrNotFound)
	d.EXPECT().Stop(mock.Anything, "sps-abc", stopWait).Return(wrapped)
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
			if err := s.store.Create("abc", Project{Name: "x"}); err != nil {
				t.Fatal(err)
			}
			// volume-taking scopes stop the container first (best effort)
			if tc.scope != ScopeMetadata {
				d.EXPECT().Stop(mock.Anything, "sps-abc", stopWait).Return(nil)
			}
			if tc.removeContainer {
				d.EXPECT().Remove(mock.Anything, "sps-abc", true).Return(nil)
			}
			if tc.home {
				d.EXPECT().RemoveVolume(mock.Anything, "sps-abc-home").Return(nil)
			}
			if tc.repo {
				d.EXPECT().RemoveVolume(mock.Anything, "sps-abc-repo").Return(nil)
			}
			if err := s.Delete(t.Context(), "abc", tc.scope); err != nil {
				t.Fatalf("delete: %v", err)
			}
			_, getErr := s.store.Get("abc")
			metadataGone := getErr != nil
			if metadataGone != tc.metadata {
				t.Fatalf("metadata gone = %v, want %v", metadataGone, tc.metadata)
			}
			last := eventsOf(t, s)[len(eventsOf(t, s))-1]
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

func TestDefaultName(t *testing.T) {
	cases := map[string]string{
		testRepo:                      "hello",
		"https://github.com/x/hello":  "hello",
		"https://github.com/x/hello/": "hello",
		"":                            "untitled",
		"git@gitlab.com:y/thing.git":  "thing",
	}
	for url, want := range cases {
		if got := defaultName(url); got != want {
			t.Errorf("defaultName(%q) = %q, want %q", url, got, want)
		}
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
	if err := s.store.Create("abc", Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	// metadata exists but the container is gone (scope=container deleted):
	// Start must surface the engine's not-found so HTTP maps it to 404.
	wrapped := fmt.Errorf("start: %w", docker.ErrNotFound)
	d.EXPECT().Start(mock.Anything, "sps-abc").Return(wrapped)
	if err := s.Start(t.Context(), "abc"); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("err = %v, want docker.ErrNotFound", err)
	}

	// Restart tolerates the stop but still fails at start.
	d.EXPECT().Stop(mock.Anything, "sps-abc", stopWait).Return(nil)
	d.EXPECT().Start(mock.Anything, "sps-abc").Return(wrapped)
	if err := s.Restart(t.Context(), "abc"); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("restart err = %v, want docker.ErrNotFound", err)
	}
}

func TestDeletePartialFailureReportsFirstError(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Stop(mock.Anything, "sps-abc", stopWait).Return(nil)
	first := fmt.Errorf("remove: %w", docker.ErrNotFound)
	d.EXPECT().Remove(mock.Anything, "sps-abc", true).Return(first)
	d.EXPECT().RemoveVolume(mock.Anything, "sps-abc-home").Return(errors.New("second"))

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
	if err := s.store.Create("abc", Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Stop(mock.Anything, "sps-abc", stopWait).Return(nil)
	d.EXPECT().Remove(mock.Anything, "sps-abc", true).Return(nil)
	d.EXPECT().RemoveVolume(mock.Anything, "sps-abc-home").Return(errors.New("volume busy"))
	d.EXPECT().RemoveVolume(mock.Anything, "sps-abc-repo").Return(nil)

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
	if err := s.store.Create("abc", Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil).Once()
	if _, err := s.EnsureContainer(t.Context(), "abc"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureContainerMissingReconciles(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	// first Inspect: missing → triggers reconciliation
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{}, docker.ErrNotFound).Once()
	// reconciliation recreates the container
	d.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	d.EXPECT().InspectImage(mock.Anything, ProjectImage).Return(nil)
	d.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	// second Inspect: report the reconciled container as running
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil).Once()
	if st, err := s.EnsureContainer(t.Context(), "abc"); err != nil || st.State != StateRunning {
		t.Fatalf("st=%+v err=%v", st, err)
	}
}

func TestEnsureContainerExitedIsNoop(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: false, Status: "exited"}, nil)
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
	if err := s.store.Create("abc", Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{}, docker.ErrNotFound).Once()
	d.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	d.EXPECT().InspectImage(mock.Anything, ProjectImage).Return(nil)
	d.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil).Once()
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

func TestEnsureContainerReconcileReclonesEmptyVolume(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{Name: "hello", Repo: testRepo}); err != nil {
		t.Fatal(err)
	}
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{}, docker.ErrNotFound).Once()
	var cid string
	expectProjectReady(d, &cid)
	// fresh engine: the repo volume came up empty...
	d.EXPECT().Exec(mock.Anything, "cid123", []string{"ls", "-A", repoTarget}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	// ...so recovery re-clones the repo from state.json
	d.EXPECT().Exec(mock.Anything, "cid123",
		[]string{"git", "clone", testRepo, repoTarget + "/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil).Once()

	if _, err := s.EnsureContainer(t.Context(), "abc"); err != nil {
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
}

// fakeInstaller records which harnesses were installed. Implements Installer.
type fakeInstaller struct {
	installed []string
}

func (f *fakeInstaller) InstallHarness(ctx context.Context, _ string, harnessID string) error {
	f.installed = append(f.installed, harnessID)
	return nil
}

func TestEnsureContainerReinstallsHarnesses(t *testing.T) {
	s, d, _, _ := newService(t)
	// Project with two installed harnesses
	if err := s.store.Create("abc", Project{
		Name:      "x",
		Harnesses: []string{"opencode", "freebuff"},
	}); err != nil {
		t.Fatal(err)
	}
	// Container is missing → triggers reconciliation
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{}, docker.ErrNotFound).Once()
	d.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	d.EXPECT().InspectImage(mock.Anything, ProjectImage).Return(nil)
	d.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil).Once()

	inst := &fakeInstaller{}
	s.SetInstaller(inst)

	if _, err := s.EnsureContainer(t.Context(), "abc"); err != nil {
		t.Fatal(err)
	}
	if len(inst.installed) != 2 {
		t.Fatalf("installed %d harnesses, want 2: %v", len(inst.installed), inst.installed)
	}
	if inst.installed[0] != "opencode" || inst.installed[1] != "freebuff" {
		t.Fatalf("installed = %v, want [opencode freebuff]", inst.installed)
	}
}

func TestReconcileStateInstallsMissingHarnesses(t *testing.T) {
	s, d, _, _ := newService(t)
	// Project with two installed harnesses
	if err := s.store.Create("abc", Project{
		Name:      "x",
		Harnesses: []string{"opencode", "freebuff"},
	}); err != nil {
		t.Fatal(err)
	}
	// Second project with no harnesses — should be skipped
	if err := s.store.Create("def", Project{Name: "y"}); err != nil {
		t.Fatal(err)
	}
	// Third project — container is missing, should be skipped
	if err := s.store.Create("ghi", Project{
		Name:      "z",
		Harnesses: []string{"opencode"},
	}); err != nil {
		t.Fatal(err)
	}

	inst := &fakeInstaller{}
	s.SetInstaller(inst)

	// abc: running → reconcile
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil).Once()
	// def: running but no harnesses → skip (no Inspect call expected)
	d.EXPECT().Inspect(mock.Anything, "sps-def").Return(docker.Container{Running: true}, nil).Once()
	// ghi: missing → skip
	d.EXPECT().Inspect(mock.Anything, "sps-ghi").Return(docker.Container{}, docker.ErrNotFound).Once()

	s.ReconcileState(t.Context())

	if len(inst.installed) != 2 {
		t.Fatalf("installed %d harnesses, want 2: %v", len(inst.installed), inst.installed)
	}
	if inst.installed[0] != "opencode" || inst.installed[1] != "freebuff" {
		t.Fatalf("installed = %v, want [opencode freebuff]", inst.installed)
	}
}

func TestReconcileStateSkipsWithoutInstaller(t *testing.T) {
	s, d, _, _ := newService(t)
	if err := s.store.Create("abc", Project{
		Name:      "x",
		Harnesses: []string{"opencode"},
	}); err != nil {
		t.Fatal(err)
	}
	// No installer set — should not panic
	d.EXPECT().Inspect(mock.Anything, "sps-abc").Return(docker.Container{Running: true}, nil).Once()
	s.ReconcileState(t.Context())
	// No crash = pass
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
	d.EXPECT().Inspect(mock.Anything, "sps-"+id).Return(docker.Container{Running: true}, nil)
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
