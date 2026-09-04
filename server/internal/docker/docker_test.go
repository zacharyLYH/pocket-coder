package docker

import (
	"archive/tar"
	"io"
	"testing"
)

func TestContainerOptionsDefaults(t *testing.T) {
	opts := containerOptions(t.Context(), Spec{Name: "sps-x", Image: "img"})
	if opts.Name != "sps-x" || opts.Config.Image != "img" {
		t.Fatalf("name/image not set: %+v", opts)
	}
	h := opts.HostConfig
	if h.NetworkMode != "sps-net" {
		t.Fatalf("network = %q, want sps-net", h.NetworkMode)
	}
	if !h.ReadonlyRootfs {
		t.Fatal("default spec should have a read-only rootfs")
	}
	if h.Memory != 0 {
		t.Fatalf("memory = %d, want 0 (unlimited)", h.Memory)
	}
	if h.Privileged {
		t.Fatal("default spec must not be privileged")
	}
	if len(h.Mounts) != 0 || len(h.Binds) != 0 {
		t.Fatalf("unexpected mounts/binds: %+v %+v", h.Mounts, h.Binds)
	}
}

func TestContainerOptionsOverrides(t *testing.T) {
	opts := containerOptions(t.Context(), Spec{
		Name: "sps-x", Image: "img", Cmd: []string{"sleep", "1"},
		Env: []string{"A=B"}, WorkDir: "/root",
		Writable: true, Memory: 1 << 30, Network: "custom-net",
		Volumes: []Mount{{Name: "sps-x-home", Dest: "/root"}},
		Binds:   []string{"/host/key:/root/.ssh/id_ed25519:ro"},
	})
	h := opts.HostConfig
	if h.ReadonlyRootfs {
		t.Fatal("writable spec must not be read-only")
	}
	if h.Memory != 1<<30 {
		t.Fatalf("memory = %d, want 1GiB", h.Memory)
	}
	if h.NetworkMode != "custom-net" {
		t.Fatalf("network = %q", h.NetworkMode)
	}
	if len(h.Mounts) != 1 || h.Mounts[0].Source != "sps-x-home" || h.Mounts[0].Target != "/root" {
		t.Fatalf("volumes: %+v", h.Mounts)
	}
	if len(h.Binds) != 1 || h.Binds[0] != "/host/key:/root/.ssh/id_ed25519:ro" {
		t.Fatalf("binds: %+v", h.Binds)
	}
	if opts.Config.WorkingDir != "/root" || len(opts.Config.Env) != 1 {
		t.Fatalf("config: %+v", opts.Config)
	}
}

func TestContainerNamespaceOmitsHostGatewayMapping(t *testing.T) {
	opts := containerOptions(t.Context(), Spec{Name: "preview", Image: "browser", Network: "container:sps-project"})
	if opts.HostConfig.NetworkMode != "container:sps-project" {
		t.Fatalf("network = %q", opts.HostConfig.NetworkMode)
	}
	if len(opts.HostConfig.ExtraHosts) != 0 {
		t.Fatalf("namespace sidecar must not have ExtraHosts: %v", opts.HostConfig.ExtraHosts)
	}
}

func TestTarFileRoundTrip(t *testing.T) {
	tr := tar.NewReader(tarFile(".sps-env", []byte("FOO=bar\n")))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if hdr.Name != ".sps-env" {
		t.Fatalf("tar name = %q, want %q", hdr.Name, ".sps-env")
	}
	got, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "FOO=bar\n" {
		t.Fatalf("content = %q", got)
	}
}
