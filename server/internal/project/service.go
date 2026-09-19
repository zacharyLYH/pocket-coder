package project

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"pcoder/internal/codemapthreads"
	"pcoder/internal/docker"
	"pcoder/internal/obs"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
	"pcoder/internal/textutil"
)

// ProjectImage is the shared project image. Built once from the embedded
// Dockerfile if absent. The tag version bumps whenever the embedded
// Dockerfile changes, so engines holding an older build rebuild it.
const ProjectImage = "pcoder-project:v3"

const (
	repoTarget = "/workspace"
	stopWait   = 10 * time.Second
)

// Scope selects what a delete removes. The home volume is runtime state (tmux sessions, harness files),
// so it goes with the container; the repo volume is the user's work.
// Because the engine refuses to drop a volume referenced by any container,
// scope=repo also removes the container (rootfs only — home survives).
type Scope string

const (
	ScopeContainer Scope = "container" // container + home volume
	ScopeRepo      Scope = "repo"      // repo volume only
	ScopeMetadata  Scope = "metadata"  // project.json + index entry only
	ScopeAll       Scope = "all"       // everything
)

// ErrNotFound means no such project (metadata). ErrInvalidScope means a
// delete scope that does not exist. ErrInvalidInput means a create request
// the pipeline will not attempt. ErrConflict means the repo is already a
// project (ids are owner/repo, so one repo is one project).
var (
	ErrNotFound     = errors.New("project not found")
	ErrInvalidScope = errors.New("invalid delete scope")
	ErrInvalidInput = errors.New("invalid input")
	ErrConflict     = errors.New("project already exists")
)

// Installer installs a harness into a project container during recovery.
// The project package must not depend on the session package directly.
type Installer interface {
	InstallHarness(ctx context.Context, container string, harnessID string) error
}

// Service is the project control plane on top of the store and Docker. It
// logs through obs (ctx, project, key, message, data): project lines go to
// the observe file and stderr with no event-log wiring.
type Service struct {
	store     Store
	dkr       docker.Client
	sshKeys   *sshkeys.Store
	installer Installer
	// codemaps is the codemap chat store. Chats are project-scoped
	// artifacts: when the record goes, the chat files go with it. Wired
	// via SetCodemaps (the SetSSHKeys pattern), so tests can leave it nil.
	codemaps *codemapthreads.Store
	// allowAnyRepo lifts the GitHub-only create requirement so test
	// stacks can clone from a local git daemon. Set via SetAllowAnyRepo
	// (wired from config in main, set directly by integration tests);
	// production leaves it false.
	allowAnyRepo bool
}

// NewService wires the pipeline together.
func NewService(store Store, dkr docker.Client) *Service {
	return &Service{store: store, dkr: dkr}
}

// SetSSHKeys attaches an SSH key store for container key injection.
func (s *Service) SetSSHKeys(sk *sshkeys.Store) { s.sshKeys = sk }

// SetCodemaps attaches the codemap chat store for delete cascades.
func (s *Service) SetCodemaps(cs *codemapthreads.Store) { s.codemaps = cs }

// SetInstaller attaches a harness installer for eager recovery.
func (s *Service) SetInstaller(ins Installer) { s.installer = ins }

// SetAllowAnyRepo lifts the GitHub-only create requirement (test stacks
// cloning from a local git daemon). Never set in production.
func (s *Service) SetAllowAnyRepo(v bool) { s.allowAnyRepo = v }

// RecordInstall records that harnessID is installed in projectID.
func (s *Service) RecordInstall(projectID, harnessID string) error {
	if err := s.store.RecordInstall(projectID, harnessID); err != nil {
		return s.wrapNotFound(err)
	}
	return nil
}

// RecordSession saves session metadata (name → harness) in state.json.
func (s *Service) RecordSession(projectID, name, harnessID string) error {
	return s.wrapNotFound(s.store.RecordSession(projectID, name, harnessID))
}

// GetSession returns session metadata from state.json. ok is false when
// the session has no recorded metadata.
func (s *Service) GetSession(projectID, name string) (state.Session, bool) {
	return s.store.GetSession(projectID, name)
}

// RemoveSession deletes session metadata from state.json.
func (s *Service) RemoveSession(projectID, name string) error {
	return s.store.RemoveSession(projectID, name)
}

// ContainerName is the docker container backing a project id. Ids are
// owner/repo (a slash), which Docker forbids in names, so the id is
// sanitized to owner-repo: the container name still reads as the repo.
func ContainerName(id string) string { return "pcoder-" + SanitizeName(id) }

func repoVolume(id string) string { return "pcoder-" + SanitizeName(id) + "-repo" }
func homeVolume(id string) string { return "pcoder-" + SanitizeName(id) + "-home" }

// Create clones the repoURL into a new project and returns when it is
// ready or failed. repoURL is required and must be a GitHub repository
// URL — cloning is the only way to create a project. The project id is
// the repo's owner/repo, so creating the same repo twice is a conflict.
// A clone failure keeps the project running so the user can repair it
// from the terminal — only the error surfaces here.
// cloneMethod is "ssh" or "http" (empty defaults to "http").
func (s *Service) Create(ctx context.Context, repoURL, branch, cloneMethod string) (string, Project, error) {
	repoURL = strings.TrimSpace(repoURL)
	branch = strings.TrimSpace(branch)
	cloneMethod = strings.TrimSpace(cloneMethod)
	if strings.HasPrefix(branch, "-") {
		return "", Project{}, fmt.Errorf("%w: repo url and branch must not start with \"-\"", ErrInvalidInput)
	}
	id, err := parseRepoID(repoURL, s.allowAnyRepo)
	if err != nil {
		return "", Project{}, err
	}
	// The project never changes through this call chain, so it rides in
	// ctx from here on — downstream logs name only key and message.
	ctx = obs.WithProject(ctx, id)
	if cloneMethod == "" {
		cloneMethod = "http"
	}
	if cloneMethod != "ssh" && cloneMethod != "http" {
		return "", Project{}, fmt.Errorf("%w: cloneMethod must be \"ssh\" or \"http\"", ErrInvalidInput)
	}
	if _, err := s.store.Get(id); err == nil {
		return "", Project{}, fmt.Errorf("%w: project %q", ErrConflict, id)
	}
	p := Project{Repo: repoURL, Branch: branch, CloneMethod: cloneMethod}
	if err := s.store.Create(id, p); err != nil {
		return "", Project{}, err
	}
	obs.Info(ctx, obs.ProjectCreate, "project created", map[string]any{"repo": repoURL, "branch": branch, "cloneMethod": cloneMethod})

	cid, err := s.runProject(ctx, id)
	if err != nil {
		_ = s.store.Delete(id)
		return "", Project{}, err
	}

	// Inject SSH keys before any clone so git SSH works.
	if err := s.injectSSHKeys(ctx, cid); err != nil {
		obs.Warn(ctx, obs.ProjectSSHKeys, "ssh key injection failed: "+err.Error(),
			map[string]any{"error": err.Error()})
	}

	if err := s.cloneRepo(ctx, id, cid, p); err != nil {
		return "", Project{}, err
	}
	obs.Info(ctx, obs.ProjectReady, "project ready", nil)
	return id, p, nil
}

// runProject ensures network + image exist, then creates and starts the
// project's container. Preview traffic stays inside the project/browser
// network path; project ports are never published on the Docker host.
func (s *Service) runProject(ctx context.Context, id string) (string, error) {
	if err := s.dkr.EnsureNetwork(ctx, docker.DefaultNetwork); err != nil {
		return "", err
	}
	if err := s.ensureProjectImage(ctx, id); err != nil {
		return "", err
	}
	spec := docker.Spec{
		Name:     ContainerName(id),
		Image:    ProjectImage,
		Writable: true,
		Volumes: []docker.Mount{
			{Name: repoVolume(id), Dest: repoTarget},
			{Name: homeVolume(id), Dest: "/root"},
		},
	}
	cid, err := s.dkr.Run(ctx, spec)
	if err != nil {
		return "", err
	}
	return cid, nil
}

// injectSSHKeys writes the user's registered SSH public keys into
// ~/.ssh/authorized_keys inside the container so git SSH clones work.
func (s *Service) injectSSHKeys(ctx context.Context, container string) error {
	if s.sshKeys == nil {
		return nil
	}
	// Single-user deployment: inject every registered key. Multi-user would
	// scope to the project owner.
	allKeys, err := s.sshKeys.AllAuthorizedKeys()
	if err != nil {
		return err
	}
	if len(allKeys) == 0 {
		return nil
	}
	// mkdir -p ~/.ssh then write authorized_keys. Exec is raw argv (no
	// shell), so the compound command goes through sh -c.
	if res, err := s.dkr.Exec(ctx, container, []string{"sh", "-c", "mkdir -p /root/.ssh && chmod 700 /root/.ssh"}, false); err != nil {
		return fmt.Errorf("mkdir .ssh: %w", err)
	} else if res.ExitCode != 0 {
		return fmt.Errorf("mkdir .ssh: %s", strings.TrimSpace(res.Output))
	}
	if err := s.dkr.WriteFile(ctx, container, "/root/.ssh/authorized_keys", allKeys); err != nil {
		return fmt.Errorf("write authorized_keys: %w", err)
	}
	return nil
}

// ensureProjectImage builds the embedded project definition when the image
// is not on the engine yet.
func (s *Service) ensureProjectImage(ctx context.Context, id string) error {
	ctx = obs.WithProject(ctx, id)
	err := s.dkr.InspectImage(ctx, ProjectImage)
	if err == nil {
		return nil
	}
	if !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	obs.Info(ctx, obs.ProjectImageBuild, "building project image", map[string]any{"image": ProjectImage})
	return s.dkr.Build(ctx, docker.BuildOptions{Tag: ProjectImage, InputStream: projectContext()}, io.Discard)
}

// Get returns one project plus its live container status.
func (s *Service) Get(ctx context.Context, id string) (Project, Status, error) {
	p, err := s.store.Get(id)
	if err != nil {
		return Project{}, Status{}, s.wrapNotFound(err)
	}
	st, err := ContainerStatus(ctx, s.dkr, ContainerName(id))
	if err != nil {
		return Project{}, Status{}, err
	}
	return p, st, nil
}

// EnsureContainer makes sure a project's CONTAINER exists, returning its
// current state. If the container is missing but the project's volumes
// persist (the disk is the source of truth), it recreates the container
// reusing those volumes — code and harness binaries live in volumes, so a
// mid-run recreate loses nothing and needs no re-clone or reinstall.
// Everything beyond the container (repo re-clone on a fresh engine, harness
// installs) is boot's job, done by BringAllUp before the server accepts
// requests. Exited/paused containers are left alone (the user can Start
// explicitly); callers check State themselves.
func (s *Service) EnsureContainer(ctx context.Context, id string) (Status, error) {
	ctx = obs.WithProject(ctx, id)
	if _, err := s.store.Get(id); err != nil {
		return Status{}, s.wrapNotFound(err)
	}
	st, err := ContainerStatus(ctx, s.dkr, ContainerName(id))
	if err != nil {
		return Status{}, err
	}
	if st.State != StateMissing {
		return st, nil
	}
	// Container gone, volumes persist — recreate it.
	cid, err := s.runProject(ctx, id)
	if err != nil {
		return Status{}, fmt.Errorf("reconcile container %s: %w", id, err)
	}
	obs.Info(ctx, obs.ProjectReconcile, "reconciling missing container", map[string]any{"container": cid})
	// Inject SSH keys so git clones work immediately.
	if err := s.injectSSHKeys(ctx, cid); err != nil {
		obs.Warn(ctx, obs.ProjectSSHKeys, "ssh key injection failed: "+err.Error(),
			map[string]any{"error": err.Error()})
	}
	return ContainerStatus(ctx, s.dkr, ContainerName(id))
}

// installRecordedHarnesses runs the explicit install for every harness id
// recorded on the project. Unknown ids (harness deleted from the registry)
// are skipped; real failures are logged and returned to the caller, which
// decides whether they are fatal.
func (s *Service) installRecordedHarnesses(ctx context.Context, id, cid string, harnessIDs []string) error {
	if s.installer == nil || len(harnessIDs) == 0 {
		return nil
	}
	ctx = obs.WithProject(ctx, id)
	var firstErr error
	for _, hid := range harnessIDs {
		obs.Info(ctx, obs.HarnessInstall, "bootstrap: installing harness "+hid,
			map[string]any{"harness": hid, "stage": "bootstrap"})
		if err := s.installer.InstallHarness(ctx, cid, hid); err != nil {
			if strings.Contains(err.Error(), "no such harness") {
				obs.Warn(ctx, obs.HarnessInstall, "bootstrap: recorded harness no longer registered, skipping "+hid,
					map[string]any{"harness": hid, "stage": "bootstrap"})
				continue
			}
			obs.Error(ctx, obs.HarnessInstall, "bootstrap: harness install failed "+hid+": "+err.Error(),
				map[string]any{"harness": hid, "stage": "bootstrap", "error": err.Error()})
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		obs.Info(ctx, obs.HarnessInstall, "bootstrap: harness installed "+hid,
			map[string]any{"harness": hid, "stage": "bootstrap"})
	}
	return firstErr
}

// provisionProject brings ONE project to full desired state: container
// running, repo present (re-cloned when the engine lost the volume), and
// every recorded harness installed. This is the boot path; the per-request
// safety net (EnsureContainer) only guarantees the container itself.
func (s *Service) provisionProject(ctx context.Context, id string) error {
	ctx = obs.WithProject(ctx, id)
	st, err := s.EnsureContainer(ctx, id)
	if err != nil {
		return err
	}
	obs.Info(ctx, obs.ProjectBootstrap, "bootstrap: container "+st.State, map[string]any{"state": st.State})
	if st.State != StateRunning {
		if err := s.Start(ctx, id); err != nil {
			return fmt.Errorf("start container: %w", err)
		}
	}
	p, err := s.store.Get(id)
	if err != nil {
		return err
	}
	// Fresh engine: the repo volume came up empty — nothing was recovered by
	// recreating the container, so re-clone from the repo URL. Like Create,
	// a clone failure keeps the project running (the user can repair it).
	// cloneRepo owns its own lines (project.clone); only the trigger is here.
	if p.Repo != "" {
		cid := ContainerName(id)
		if empty, err := s.repoVolumeEmpty(ctx, cid); err != nil {
			obs.Warn(ctx, obs.ProjectBootstrap, "bootstrap: repo volume check failed: "+err.Error(),
				map[string]any{"error": err.Error()})
		} else if empty {
			obs.Info(ctx, obs.ProjectBootstrap, "bootstrap: repo volume empty, re-cloning",
				map[string]any{"repo": p.Repo})
			if err := s.cloneRepo(ctx, id, cid, p); err != nil {
				obs.Warn(ctx, obs.ProjectBootstrap, "bootstrap: repo re-clone failed: "+err.Error(),
					map[string]any{"error": err.Error()})
			}
		}
	}
	return s.installRecordedHarnesses(ctx, id, ContainerName(id), p.Harnesses)
}

// BringAllUp makes the live Docker state match state.json for EVERY project:
// each container runs, repos are present, and recorded harnesses are
// installed. Blocking by design — callers (main) run it before serving so
// no request can ever observe a missing container or harness binary.
// Per-project failures are logged and returned; they never abort the rest.
func (s *Service) BringAllUp(ctx context.Context) error {
	entries, err := s.store.List()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	slog.Info("bootstrap: starting", "projects", len(entries))
	var firstErr error
	for _, e := range entries {
		pctx := obs.WithProject(ctx, e.ID)
		obs.Info(pctx, obs.ProjectBootstrap, "bootstrap: project", nil)
		if err := s.provisionProject(ctx, e.ID); err != nil {
			obs.Error(pctx, obs.ProjectBootstrap, "bootstrap: project failed: "+err.Error(),
				map[string]any{"error": err.Error()})
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	slog.Info("bootstrap: done", "projects", len(entries))
	return firstErr
}

// repoVolumeEmpty reports whether the project's repo volume has no visible
// content (a fresh volume — nothing was recovered from disk).
func (s *Service) repoVolumeEmpty(ctx context.Context, cid string) (bool, error) {
	res, err := s.dkr.Exec(ctx, cid, []string{"ls", "-A", repoTarget}, false)
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, fmt.Errorf("ls %s: %s", repoTarget, textutil.Tail(res.Output))
	}
	return strings.TrimSpace(res.Output) == "", nil
}

// cloneRepo clones the project's repo into the project's repo volume.
// Shared by Create and post-reconcile recovery.
func (s *Service) cloneRepo(ctx context.Context, id, cid string, p Project) error {
	if p.Repo == "" {
		return nil
	}
	ctx = obs.WithProject(ctx, id)
	args := []string{"git", "clone"}
	if p.Branch != "" {
		args = append(args, "--branch", p.Branch, "--single-branch")
	}
	args = append(args, p.Repo, repoTarget+"/repo")
	res, err := s.dkr.Exec(ctx, cid, args, false)
	if err != nil || res.ExitCode != 0 {
		detail := textutil.Tail(res.Output)
		if err != nil {
			detail = err.Error()
		}
		obs.Error(ctx, obs.ProjectClone, "clone failed: "+detail, map[string]any{"detail": detail})
		return fmt.Errorf("clone %s: %s", p.Repo, detail)
	}
	obs.Info(ctx, obs.ProjectClone, "clone landed", map[string]any{"branch": p.Branch})
	return nil
}

// List returns the projects index without touching Docker.
func (s *Service) List() ([]Entry, error) {
	return s.store.List()
}

// Start starts a stopped project's container.
func (s *Service) Start(ctx context.Context, id string) error {
	ctx = obs.WithProject(ctx, id)
	if _, err := s.store.Get(id); err != nil {
		return s.wrapNotFound(err)
	}
	if err := s.dkr.Start(ctx, ContainerName(id)); err != nil {
		return err
	}
	obs.Info(ctx, obs.ProjectStart, "container started", nil)
	return nil
}

// Stop stops a project's container; volumes persist across stops and
// deletes unless the scope explicitly takes them.
func (s *Service) Stop(ctx context.Context, id string) error {
	ctx = obs.WithProject(ctx, id)
	if _, err := s.store.Get(id); err != nil {
		return s.wrapNotFound(err)
	}
	if err := s.dkr.Stop(ctx, ContainerName(id), stopWait); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	obs.Info(ctx, obs.ProjectStop, "container stopped", nil)
	return nil
}

// Restart stops then starts a project's container.
func (s *Service) Restart(ctx context.Context, id string) error {
	if err := s.Stop(ctx, id); err != nil {
		return err
	}
	return s.Start(ctx, id)
}

// Delete removes exactly the requested scope.
func (s *Service) Delete(ctx context.Context, id string, scope Scope) error {
	ctx = obs.WithProject(ctx, id)
	if scope != ScopeContainer && scope != ScopeRepo && scope != ScopeMetadata && scope != ScopeAll {
		return fmt.Errorf("%w: %q", ErrInvalidScope, scope)
	}
	if _, err := s.store.Get(id); err != nil {
		return s.wrapNotFound(err)
	}
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	// Volume removal requires the container to let go: stop it first when
	// the scope takes volumes. A missing container is fine (already gone).
	// The engine refuses to remove a volume referenced by ANY container
	// (even stopped), so taking the repo also takes the container — its
	// rootfs is disposable state; the home volume survives it.
	if scope != ScopeMetadata {
		_ = s.dkr.Stop(ctx, ContainerName(id), stopWait)
	}
	if scope == ScopeContainer || scope == ScopeRepo || scope == ScopeAll {
		fail(s.dkr.Remove(ctx, ContainerName(id), true))
	}
	if scope == ScopeContainer || scope == ScopeAll {
		fail(s.dkr.RemoveVolume(ctx, homeVolume(id)))
	}
	if scope == ScopeRepo || scope == ScopeAll {
		fail(s.dkr.RemoveVolume(ctx, repoVolume(id)))
	}
	// Only drop the record when docker cleanup actually succeeded: a
	// half-deleted project must stay listed (and retryable) instead of
	// becoming an invisible orphan the next cleanup can no longer see.
	if scope == ScopeAll && firstErr != nil {
		return firstErr
	}
	if scope == ScopeMetadata || scope == ScopeAll {
		fail(s.store.Delete(id))
		// Chats are project-scoped: once the record is gone no request can
		// reach them, so they must not linger on disk. Best effort — a
		// failed file cleanup never blocks the project deletion itself.
		if s.codemaps != nil {
			if cerr := s.codemaps.DeleteProjectDir(id); cerr != nil {
				obs.Warn(ctx, obs.ProjectDelete, "codemap cleanup failed: "+cerr.Error(),
					map[string]any{"scope": string(scope), "stage": "cleanup_codemaps", "error": cerr.Error()})
			}
		}
	}
	if firstErr != nil {
		return firstErr
	}
	obs.Info(ctx, obs.ProjectDelete, "project deleted", map[string]any{"scope": string(scope)})
	return nil
}

func (s *Service) wrapNotFound(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return err
}
