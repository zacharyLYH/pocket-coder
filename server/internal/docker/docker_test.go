package docker

import (
	"archive/tar"
	"io"
	"testing"

	dockerclient "github.com/fsouza/go-dockerclient"
)

func TestContainerOptionsDefaults(t *testing.T) {
	opts := containerOptions(t.Context(), Spec{Name: "pcoder-x", Image: "img"})
	if opts.Name != "pcoder-x" || opts.Config.Image != "img" {
		t.Fatalf("name/image not set: %+v", opts)
	}
	h := opts.HostConfig
	if h.NetworkMode != "pcoder-net" {
		t.Fatalf("network = %q, want pcoder-net", h.NetworkMode)
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
		Name: "pcoder-x", Image: "img", Cmd: []string{"sleep", "1"},
		Env: []string{"A=B"}, WorkDir: "/root", Entrypoint: []string{"sh", "-c"},
		Writable: true, Memory: 1 << 30, Network: "custom-net",
		Volumes: []Mount{{Name: "pcoder-x-home", Dest: "/root"}},
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
	if len(h.Mounts) != 1 || h.Mounts[0].Source != "pcoder-x-home" || h.Mounts[0].Target != "/root" {
		t.Fatalf("volumes: %+v", h.Mounts)
	}
	if len(h.Binds) != 1 || h.Binds[0] != "/host/key:/root/.ssh/id_ed25519:ro" {
		t.Fatalf("binds: %+v", h.Binds)
	}
	if opts.Config.WorkingDir != "/root" || len(opts.Config.Env) != 1 || len(opts.Config.Entrypoint) != 2 {
		t.Fatalf("config: %+v", opts.Config)
	}
	if len(opts.HostConfig.ExtraHosts) != 1 {
		t.Fatalf("networked spec should keep the host-gateway mapping: %v", opts.HostConfig.ExtraHosts)
	}
}

func TestContainerNamespaceOmitsHostGatewayMapping(t *testing.T) {
	opts := containerOptions(t.Context(), Spec{Name: "preview", Image: "browser", Network: "container:pcoder-project"})
	if opts.HostConfig.NetworkMode != "container:pcoder-project" {
		t.Fatalf("network = %q", opts.HostConfig.NetworkMode)
	}
	if len(opts.HostConfig.ExtraHosts) != 0 {
		t.Fatalf("namespace sidecar must not have ExtraHosts: %v", opts.HostConfig.ExtraHosts)
	}
}

func TestContainerOptionsPublishLoopback(t *testing.T) {
	opts := containerOptions(t.Context(), Spec{
		Name: "sidecar", Image: "img",
		Network:         "pcoder-net",
		PublishLoopback: []int{9223, 6080},
	})
	if opts.Config.ExposedPorts == nil || len(opts.Config.ExposedPorts) != 2 {
		t.Fatalf("exposed = %v", opts.Config.ExposedPorts)
	}
	if _, ok := opts.Config.ExposedPorts[dockerclient.Port("9223/tcp")]; !ok {
		t.Fatalf("9223 not exposed: %v", opts.Config.ExposedPorts)
	}
	binds := opts.HostConfig.PortBindings
	if len(binds) != 2 {
		t.Fatalf("bindings = %v", binds)
	}
	for port, list := range binds {
		if len(list) != 1 {
			t.Fatalf("port %s bindings = %v", port, list)
		}
		if list[0].HostIP != "127.0.0.1" {
			t.Fatalf("port %s bound to %q, want loopback", port, list[0].HostIP)
		}
		if list[0].HostPort != "" {
			t.Fatalf("port %s host port = %q, want engine-assigned", port, list[0].HostPort)
		}
	}
}

func TestPublishedPorts(t *testing.T) {
	got := publishedPorts(map[dockerclient.Port][]dockerclient.PortBinding{
		"9223/tcp": {{HostIP: "127.0.0.1", HostPort: "49153"}},
		"6080/tcp": {{HostIP: "0.0.0.0", HostPort: "49154"}},
		"22/tcp":   {{HostIP: "192.168.1.5", HostPort: "2222"}}, // non-loopback: ignored
	})
	if got[9223] != 49153 || got[6080] != 49154 {
		t.Fatalf("got %v", got)
	}
	if _, ok := got[22]; ok {
		t.Fatalf("non-loopback binding leaked: %v", got)
	}
}

func TestPublishedPortsEmpty(t *testing.T) {
	if got := publishedPorts(map[dockerclient.Port][]dockerclient.PortBinding{}); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
	if got := publishedPorts(nil); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestTarFileRoundTrip(t *testing.T) {
	tr := tar.NewReader(tarFile(".pcoder-env", []byte("FOO=bar\n")))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if hdr.Name != ".pcoder-env" {
		t.Fatalf("tar name = %q, want %q", hdr.Name, ".pcoder-env")
	}
	got, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "FOO=bar\n" {
		t.Fatalf("content = %q", got)
	}
}
