package project

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"sps/internal/docker"
	"sps/internal/events"
	"sps/internal/sshkeys"
	"sps/internal/state"
	"sps/internal/textutil"
)

// SandboxImage is the shared project sandbox. Built once from the embedded
// Dockerfile if absent. The tag version bumps whenever the embedded
// Dockerfile changes, so engines holding an older build rebuild it.
const SandboxImage = "sps-sandbox:v3"

const (
	repoTarget = "/workspace"
	stopWait   = 10 * time.Second
)

// Events is the append side of the event log.
type Events interface {
	Append(typ string, data map[string]any) (events.Event, error)
}

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
// the pipeline will not attempt.
var (
	ErrNotFound     = errors.New("project not found")
	ErrInvalidScope = errors.New("invalid delete scope")
	ErrInvalidInput = errors.New("invalid input")
)

// Installer installs a harness into a project container during recovery.
// The project package must not depend on the session package directly.
type Installer interface {
	InstallHarness(ctx context.Context, container string, harnessID string) error
}

// Service is the project control plane on top of the store and Docker.
type Service struct {
	store     Store
	dkr       docker.Client
	ev        Events
	sshKeys   *sshkeys.Store
	installer Installer
}

// NewService wires the pipeline together.
func NewService(store Store, dkr docker.Client, ev Events) *Service {
	return &Service{store: store, dkr: dkr, ev: ev}
}

// SetSSHKeys attaches an SSH key store for container key injection.
func (s *Service) SetSSHKeys(sk *sshkeys.Store) { s.sshKeys = sk }

// SetInstaller attaches a harness installer for eager recovery.
func (s *Service) SetInstaller(ins Installer) { s.installer = ins }

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

// ContainerName is the docker container backing project id.
func ContainerName(id string) string { return "sps-" + id }

func repoVolume(id string) string { return "sps-" + id + "-repo" }
func homeVolume(id string) string { return "sps-" + id + "-home" }

// Create runs a sandbox for the repo and clones it inside the container
// (blank sandbox when repoURL is empty). Synchronous: returns when the
// project is ready or failed. A clone failure keeps the sandbox running so
// the user can repair it from the terminal — only the error surfaces here.
// cloneMethod is "ssh" or "http" (empty defaults to "http").
func (s *Service) Create(ctx context.Context, repoURL, branch, cloneMethod string) (string, Project, error) {
	if strings.HasPrefix(repoURL, "-") || strings.HasPrefix(branch, "-") {
		return "", Project{}, fmt.Errorf("%w: repo url and branch must not start with \"-\"", ErrInvalidInput)
	}
	if cloneMethod == "" {
		cloneMethod = "http"
	}
	if cloneMethod != "ssh" && cloneMethod != "http" {
		return "", Project{}, fmt.Errorf("%w: cloneMethod must be \"ssh\" or \"http\"", ErrInvalidInput)
	}
	id, err := newID()
	if err != nil {
		return "", Project{}, err
	}
	p := Project{Name: defaultName(repoURL), Repo: repoURL, Branch: branch, CloneMethod: cloneMethod}
	if err := s.store.Create(id, p); err != nil {
		return "", Project{}, err
	}
	s.ev.Append("project.create", map[string]any{"id": id, "name": p.Name, "repo": repoURL, "branch": branch, "cloneMethod": cloneMethod})

	cid, err := s.runSandbox(ctx, id)
	if err != nil {
		_ = s.store.Delete(id)
		return "", Project{}, err
	}

	// Inject SSH keys before any clone so git SSH works.
	if err := s.injectSSHKeys(ctx, cid); err != nil {
		slog.Warn("ssh key injection", "id", id, "err", err)
	}

	if err := s.cloneRepo(ctx, id, cid, p); err != nil {
		return "", Project{}, err
	}
	s.ev.Append("project.ready", map[string]any{"id": id})
	return id, p, nil
}

// runSandbox ensures network + image exist, then creates and starts the
// project's container.
func (s *Service) runSandbox(ctx context.Context, id string) (string, error) {
	if err := s.dkr.EnsureNetwork(ctx, docker.DefaultNetwork); err != nil {
		return "", err
	}
	if err := s.ensureSandboxImage(ctx); err != nil {
		return "", err
	}
	spec := docker.Spec{
		Name:     ContainerName(id),
		Image:    SandboxImage,
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

// ensureSandboxImage builds the embedded sandbox definition when the image
// is not on the engine yet.
func (s *Service) ensureSandboxImage(ctx context.Context) error {
	err := s.dkr.InspectImage(ctx, SandboxImage)
	if err == nil {
		return nil
	}
	if !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	slog.Info("building sandbox image", "image", SandboxImage)
	s.ev.Append("project.image.build", map[string]any{"image": SandboxImage})
	return s.dkr.Build(ctx, docker.BuildOptions{Tag: SandboxImage, InputStream: sandboxContext()}, io.Discard)
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

// EnsureContainer makes sure a project's container exists, returning its
// current state. If the container is missing but the project's volumes
// persist (the disk is the source of truth), it recreates the container
// reusing those volumes. If the repo volume is gone too (fresh engine —
// state.json copied to a new machine), the repo is re-cloned: state.json
// is desired state, so recovery restores the code with it. This is the
// lazy reconciliation that closes the disk/Docker drift gap. Exited/paused
// containers are left alone (the user can Start explicitly); callers check
// State themselves.
func (s *Service) EnsureContainer(ctx context.Context, id string) (Status, error) {
	p, err := s.store.Get(id)
	if err != nil {
		return Status{}, s.wrapNotFound(err)
	}
	st, err := ContainerStatus(ctx, s.dkr, ContainerName(id))
	if err != nil {
		return Status{}, err
	}
	switch st.State {
	case StateRunning:
		return st, nil
	case StateMissing:
		// Container gone, volumes persist — recreate it.
		slog.Info("reconciling missing container", "id", id)
		cid, err := s.runSandbox(ctx, id)
		if err != nil {
			return Status{}, fmt.Errorf("reconcile container %s: %w", id, err)
		}
		s.ev.Append("project.reconcile", map[string]any{"id": id, "container": cid})
		// Inject SSH keys so git clones work immediately.
		if err := s.injectSSHKeys(ctx, cid); err != nil {
			slog.Warn("ssh key injection after reconcile", "id", id, "err", err)
		}
		// Fresh engine: the repo volume doesn't exist yet, so nothing was
		// recovered by recreating the container — re-clone from the repo
		// URL. Like Create, a clone failure keeps the sandbox running.
		if p.Repo != "" {
			if empty, err := s.repoVolumeEmpty(ctx, cid); err != nil {
				slog.Warn("repo volume check after reconcile", "id", id, "err", err)
			} else if empty {
				if err := s.cloneRepo(ctx, id, cid, p); err != nil {
					slog.Warn("repo re-clone after reconcile", "id", id, "err", err)
				}
			}
		}
		// Eagerly reinstall recorded harnesses (state.json is desired state).
		// InstallHarness probes when already present, so this is cheap on a healthy volume.
		if s.installer != nil && len(p.Harnesses) > 0 {
			for _, hid := range p.Harnesses {
				if err := s.installer.InstallHarness(ctx, cid, hid); err != nil {
					if strings.Contains(err.Error(), "no such harness") {
						continue
					}
					slog.Warn("harness reinstall after reconcile", "id", id, "harness", hid, "err", err)
				}
			}
		}
		return ContainerStatus(ctx, s.dkr, ContainerName(id))
	default:
		return st, nil
	}
}

// repoVolumeEmpty reports whether the sandbox's repo volume has no visible
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

// cloneRepo clones the project's repo into the sandbox's repo volume.
// Shared by Create and post-reconcile recovery.
func (s *Service) cloneRepo(ctx context.Context, id, cid string, p Project) error {
	if p.Repo == "" {
		return nil
	}
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
		s.ev.Append("error", map[string]any{"op": "project.clone", "id": id, "detail": detail})
		return fmt.Errorf("clone %s: %s", p.Repo, detail)
	}
	s.ev.Append("project.clone", map[string]any{"id": id, "branch": p.Branch})
	return nil
}

// List returns the projects index without touching Docker.
func (s *Service) List() ([]Entry, error) {
	return s.store.List()
}

// Start starts a stopped project's container.
func (s *Service) Start(ctx context.Context, id string) error {
	if _, err := s.store.Get(id); err != nil {
		return s.wrapNotFound(err)
	}
	if err := s.dkr.Start(ctx, ContainerName(id)); err != nil {
		return err
	}
	s.ev.Append("project.start", map[string]any{"id": id})
	return nil
}

// Stop stops a project's container; volumes persist across stops and
// deletes unless the scope explicitly takes them.
func (s *Service) Stop(ctx context.Context, id string) error {
	if _, err := s.store.Get(id); err != nil {
		return s.wrapNotFound(err)
	}
	if err := s.dkr.Stop(ctx, ContainerName(id), stopWait); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	s.ev.Append("project.stop", map[string]any{"id": id})
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
	if scope == ScopeMetadata || scope == ScopeAll {
		fail(s.store.Delete(id))
	}
	if firstErr != nil {
		return firstErr
	}
	s.ev.Append("project.delete", map[string]any{"id": id, "scope": scope})
	return nil
}

func (s *Service) wrapNotFound(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// defaultName derives the project name from the repo URL ("untitled" for a
// blank sandbox), mirroring what a polished SaaS would show in its list.
func defaultName(repoURL string) string {
	name := strings.TrimSuffix(strings.TrimRight(repoURL, "/"), ".git")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		name = "untitled"
	}
	return name
}

// newID returns 8 hex characters of crypto/rand.
func newID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// ReconcileState syncs every running container to match state.json.
// Called once at startup so the live Docker state aligns with persisted
// desired state: missing harnesses are reinstalled, state.json is the
// single source of truth. This replaces per-handler workarounds for the
// split-brain between container probes and state.json records.
func (s *Service) ReconcileState(ctx context.Context) {
	entries, err := s.store.List()
	if err != nil {
		slog.Warn("reconcile: list projects", "err", err)
		return
	}
	for _, e := range entries {
		st, err := ContainerStatus(ctx, s.dkr, ContainerName(e.ID))
		if err != nil || st.State != StateRunning {
			continue
		}
		p, err := s.store.Get(e.ID)
		if err != nil || len(p.Harnesses) == 0 {
			continue
		}
		for _, hid := range p.Harnesses {
			if s.installer == nil {
				break
			}
			if err := s.installer.InstallHarness(ctx, ContainerName(e.ID), hid); err != nil {
				if strings.Contains(err.Error(), "no such harness") {
					continue
				}
				slog.Warn("reconcile harness", "id", e.ID, "harness", hid, "err", err)
			}
		}
	}
}
