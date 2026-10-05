//go:build integration

package testutil

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pcoder/internal/docker"
)

// Lifecycle manages Docker resources for integration tests.
type Lifecycle struct {
	dkr *docker.Docker
}

// NewLifecycle creates a new test lifecycle. It dials the Docker engine,
// pings it, and skips the test if the engine is unreachable.
func NewLifecycle(t *testing.T) *Lifecycle {
	t.Helper()
	return newLifecycle(t, false)
}

// NewLifecycleFatal creates a new test lifecycle and fails the test if
// Docker is unreachable. Use this for packages where Docker is a hard
// requirement (e.g. the docker control-plane tests).
func NewLifecycleFatal(t *testing.T) *Lifecycle {
	t.Helper()
	return newLifecycle(t, true)
}

func newLifecycle(t *testing.T, fatal bool) *Lifecycle {
	t.Helper()
	dkr, err := docker.New(os.Getenv("PCODER_DOCKER_SOCK"))
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	if err := dkr.Ping(ctx); err != nil {
		if fatal {
			t.Fatalf("docker unavailable — is the engine running? (PCODER_DOCKER_SOCK=%q): %v", os.Getenv("PCODER_DOCKER_SOCK"), err)
		}
		t.Skipf("docker unavailable: %v", err)
	}
	return &Lifecycle{dkr: dkr}
}

// Docker returns the underlying Docker client.
func (l *Lifecycle) Docker() *docker.Docker { return l.dkr }

// Ctx returns a background context for Docker operations.
func (l *Lifecycle) Ctx() context.Context { return context.Background() }

// EnsureNetwork ensures the pcoder-net bridge network exists.
func (l *Lifecycle) EnsureNetwork(t *testing.T) {
	t.Helper()
	if err := l.dkr.EnsureNetwork(l.Ctx(), docker.DefaultNetwork); err != nil {
		t.Fatalf("ensure network: %v", err)
	}
}

// FixtureDir walks up from the test cwd to find test/fixtures/docker.
func FixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		p := filepath.Join(dir, "test", "fixtures", "docker")
		if _, err := os.Stat(filepath.Join(p, "Dockerfile")); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("test/fixtures/docker not found above test cwd")
		}
		dir = parent
	}
}

// BuildFixtureImage builds the test fixture image from contextDir with the
// given tag. Skips the build if the image already exists on the engine.
func (l *Lifecycle) BuildFixtureImage(t *testing.T, tag, contextDir string) {
	t.Helper()
	ctx := l.Ctx()
	if err := l.dkr.InspectImage(ctx, tag); err == nil {
		return
	}
	var log bytes.Buffer
	if err := l.dkr.Build(ctx, docker.BuildOptions{Tag: tag, ContextDir: contextDir}, &log); err != nil {
		t.Fatalf("build fixture image %s: %v\n%s", tag, err, log.String())
	}
}

// CleanupContainer registers t.Cleanup to remove a container and its volumes.
// Cleanup is idempotent: already-removed containers and volumes are silently
// ignored.
func (l *Lifecycle) CleanupContainer(t *testing.T, id string, volumes ...string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := l.dkr.Remove(ctx, id, true); err != nil {
			t.Logf("cleanup container %s: %v", id, err)
		}
		for _, v := range volumes {
			if err := l.dkr.RemoveVolume(ctx, v); err != nil {
				t.Logf("cleanup volume %s: %v", v, err)
			}
		}
	})
}

// CleanupVolume registers t.Cleanup to remove a named volume.
func (l *Lifecycle) CleanupVolume(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := l.dkr.RemoveVolume(ctx, name); err != nil {
			t.Logf("cleanup volume %s: %v", name, err)
		}
	})
}

// GitDaemonFixture manages a temporary git repo served by git daemon.
// The daemon is started automatically and stopped on test cleanup.
type GitDaemonFixture struct {
	URL string
	// ID is the project id a service with the allow-any-repo hatch
	// derives from URL ("owner/repo", lowercased).
	ID      string
	cleanup func()
}

// NewGitDaemonFixture serves a fresh repo on a random port: every fixture
// gets a unique URL, so parallel runs never share containers or volumes.
// The URL is reachable from containers via host.docker.internal.
func NewGitDaemonFixture(t *testing.T) *GitDaemonFixture {
	t.Helper()
	return newGitDaemonFixture(t, "", 0)
}

// FixedGitDaemonPort serves deterministic fixture URLs
// (git://host.docker.internal:<port>/...). Fails loudly if taken —
// callers choosing it accept no parallel runs.
const FixedGitDaemonPort = 39418

// NewFixedGitDaemonFixture serves a fresh repo at a fixed owner and port,
// so the repo URL is known before boot and seeds can hardcode it. One
// user at a time: parallel runs collide on the port, the project id,
// and the container.
func NewFixedGitDaemonFixture(t *testing.T, owner string) *GitDaemonFixture {
	t.Helper()
	return newGitDaemonFixture(t, owner, FixedGitDaemonPort)
}

func newGitDaemonFixture(t *testing.T, owner string, port int) *GitDaemonFixture {
	t.Helper()
	dir := initFixtureRepo(t)
	port = grabPort(t, port)
	if owner == "" {
		owner = fmt.Sprintf("itest-%d", port)
	}
	serveDir := filepath.Join(dir, owner)
	if err := os.MkdirAll(serveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "repo"), filepath.Join(serveDir, "repo")); err != nil {
		t.Fatal(err)
	}
	daemon := startDaemon(t, dir, port)
	g := &GitDaemonFixture{
		URL: fmt.Sprintf("git://host.docker.internal:%d/%s/repo", port, owner),
		ID:  strings.ToLower(owner + "/repo"),
		cleanup: func() {
			stopDaemon(daemon, dir)
		},
	}
	t.Cleanup(g.Close)
	return g
}

// initFixtureRepo creates a temp repo with one commit on main plus a dev
// branch, and returns the dir holding it.
func initFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if out, err := exec.Command("git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("-C", repo, "add", "-A")
	run("-C", repo, "commit", "-m", "first")
	run("-C", repo, "branch", "dev")
	return dir
}

// grabPort returns a free port: random when port is 0, else the fixed one
// after proving nobody holds it. A held fixed port fails loudly instead
// of shadowing clones into a deleted directory (see stopDaemon).
func grabPort(t *testing.T, port int) int {
	t.Helper()
	if port == 0 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().(*net.TCPAddr).Port
	}
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second); err == nil {
		// Stale daemon from a killed run (cleanup never ran). Reclaim with:
		//   pkill -f 'git daemon.*<port>'; docker volume rm $(docker volume ls -q --filter name=pcoder-itest-)
		conn.Close()
		t.Fatalf("fixed git daemon port %d held by a stale daemon — kill it and re-run", port)
	}
	return port
}

// startDaemon serves dir over the git protocol. The returned command is
// stopped via stopDaemon: the Go child handle is the git frontend, which
// forks the real listener, so killing the handle alone orphans it.
func startDaemon(t *testing.T, dir string, port int) *exec.Cmd {
	t.Helper()
	daemon := exec.Command("git", "daemon",
		"--base-path="+dir, "--export-all", "--reuseaddr",
		"--listen=0.0.0.0", "--port="+fmt.Sprint(port))
	if err := daemon.Start(); err != nil {
		t.Fatalf("git daemon: %v", err)
	}
	return daemon
}

// stopDaemon kills the listener by its unique base-path as well as the
// child handle. Best effort throughout; safe to call twice.
func stopDaemon(cmd *exec.Cmd, dir string) {
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	_ = exec.Command("pkill", "-f", "git-daemon.*"+dir).Run()
}

// Close stops the git daemon. Safe to call multiple times.
func (g *GitDaemonFixture) Close() {
	if g.cleanup != nil {
		g.cleanup()
		g.cleanup = nil
	}
}
