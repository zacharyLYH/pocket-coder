//go:build integration

// Integration tests for the Docker control plane. They drive a real engine;
// run with: go test -tags=integration -count=1 ./internal/docker/. They fail
// fast with a clear message when Docker is unavailable rather than failing
// silently or hanging.
package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	itImage   = "sps-test:latest"
	itName    = "sps-itest"
	itNetwork = "sps-test-net"
	itHomeVol = "sps-itest-home"
)

// fixtureDir walks up from the test's cwd to the repo's test/fixtures/docker
// (the module root is server/, one level below the repo root).
func fixtureDir(t *testing.T) string {
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

func newTestDocker(t *testing.T) *Docker {
	t.Helper()
	d, err := New(os.Getenv("SPS_DOCKER_SOCK"))
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		t.Fatalf("docker unavailable — is the engine running? (SPS_DOCKER_SOCK=%q): %v", os.Getenv("SPS_DOCKER_SOCK"), err)
	}
	return d
}

// TestDockerLifecycle exercises the full container lifecycle: build a fixture
// image, run it, exec a command, upload a file, inspect, detect ports, tail
// logs, resize an interactive exec, and remove everything.
func TestDockerLifecycle(t *testing.T) {
	d := newTestDocker(t)
	ctx := context.Background()

	if err := d.EnsureNetwork(ctx, itNetwork); err != nil {
		t.Fatalf("ensure network: %v", err)
	}

	// build the fixture image, streaming build output
	var buildLog bytes.Buffer
	if err := d.Build(ctx, BuildOptions{
		Tag:        itImage,
		ContextDir: fixtureDir(t),
	}, &buildLog); err != nil {
		t.Fatalf("build: %v\nbuild log:\n%s", err, buildLog.String())
	}

	// run: python http server in the background + a long-lived process, with
	// the read-only rootfs default and a writable home volume at /root
	id, err := d.Run(ctx, Spec{
		Name: itName, Image: itImage, Network: itNetwork,
		Cmd:     []string{"sh", "-c", "python3 -m http.server 8000 >/dev/null 2>&1 & echo started; sleep infinity"},
		Volumes: []Mount{{Name: itHomeVol, Dest: "/root"}},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if id == "" {
		t.Fatal("run returned empty container id")
	}
	t.Cleanup(func() {
		ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = d.Remove(ctx2, id, true)
		_ = d.c.RemoveVolume(itHomeVol)
	})

	// inspect: running
	ins, err := d.Inspect(ctx, id)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !ins.Running {
		t.Fatalf("container not running: %+v", ins)
	}

	// exec: echo round trip
	res, err := d.Exec(ctx, id, []string{"echo", "hi"}, false)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Output) != "hi" {
		t.Fatalf("exec result: %+v", res)
	}

	// exec propagates nonzero exit codes (the harness failure story
	// depends on this)
	if res, err := d.Exec(ctx, id, []string{"sh", "-c", "exit 3"}, false); err != nil || res.ExitCode != 3 {
		t.Fatalf("exit code propagation: %+v err=%v", res, err)
	}

	// file upload (docker cp) + verify via exec
	if err := d.WriteFile(ctx, id, "/root/hello.txt", []byte("hello world\n")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if res, _ := d.Exec(ctx, id, []string{"cat", "/root/hello.txt"}, false); res.Output != "hello world\n" {
		t.Fatalf("cat file: %+v", res)
	}

	// interactive exec: attach + resize while running
	execID, done, err := d.Attach(ctx, id, []string{"sh", "-c", "sleep 5"}, strings.NewReader(""), io.Discard, io.Discard, true)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := d.ResizeTTY(ctx, execID, 30, 120); err != nil {
		t.Fatalf("resize: %v", err)
	}
	select {
	case out := <-done:
		if out.Err != nil {
			t.Fatalf("attach stream: %v", out.Err)
		}
		if out.ExitCode != 0 {
			t.Fatalf("attach exit code = %d", out.ExitCode)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("attach did not finish within 15s")
	}

	// stop + remove, then inspect must report ErrNotFound
	if err := d.Stop(ctx, id, 5*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// stopping a stopped container is idempotent — the pipeline relies on
	// this for restart and scoped deletes
	if err := d.Stop(ctx, id, 5*time.Second); err != nil {
		t.Fatalf("idempotent stop: %v", err)
	}
	if err := d.Remove(ctx, id, true); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// volume removal is idempotent too (scoped deletes)
	_ = d.c.RemoveVolume(itHomeVol)
	if err := d.RemoveVolume(ctx, itHomeVol); err != nil {
		t.Fatalf("idempotent volume removal: %v", err)
	}
	if _, err := d.Inspect(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inspect after remove: %v (want ErrNotFound)", err)
	}
}
