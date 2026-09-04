package docker

import (
	"context"
	"strings"

	dockerclient "github.com/fsouza/go-dockerclient"
)

// Safe container defaults: one shared bridge network, 512 MiB memory cap.
const DefaultNetwork = "sps-net"

// Mount is a volume mounted into a container.
type Mount struct {
	Name string // named volume; empty = anonymous (removed with the container)
	Dest string // container path, e.g. /root
}

// NetworkNamespace returns Docker's network-mode value for a sidecar that
// must share a project's loopback interface. The sidecar then sees the
// project's localhost frontend/backend without publishing project ports.
func NetworkNamespace(containerID string) string {
	if containerID == "" {
		return ""
	}
	return "container:" + containerID
}

// Spec describes a container to create or run. Zero values get safe
// defaults: non-privileged, read-only rootfs, unlimited memory, sps-net
// network.
type Spec struct {
	Name     string
	Image    string
	Cmd      []string
	Env      []string
	WorkDir  string
	Writable bool   // opt out of the read-only rootfs
	Memory   int64  // 0 = unlimited
	Network  string // "" = DefaultNetwork; container:<id> shares another container's namespace
	Volumes  []Mount
	Binds    []string // host path:container path[:ro] (e.g. an SSH key)
}

// containerOptions translates a Spec into go-dockerclient options. Pure, so
// it is unit-tested without a Docker engine.
func containerOptions(ctx context.Context, spec Spec) dockerclient.CreateContainerOptions {
	network := spec.Network
	if network == "" {
		network = DefaultNetwork
	}
	host := &dockerclient.HostConfig{
		NetworkMode:    network,
		ReadonlyRootfs: !spec.Writable,
		Memory:         spec.Memory,
		Binds:          spec.Binds,
	}
	if !strings.HasPrefix(network, "container:") {
		// Projects reach host services (e.g. a local git remote) through
		// the conventional name; on Linux it maps to the bridge gateway,
		// on Docker Desktop it is built in. Docker rejects this mapping
		// alongside container:<id> network mode, so sidecars omit it.
		host.ExtraHosts = []string{"host.docker.internal:host-gateway"}
	}
	for _, m := range spec.Volumes {
		host.Mounts = append(host.Mounts, dockerclient.HostMount{Type: "volume", Source: m.Name, Target: m.Dest})
	}
	return dockerclient.CreateContainerOptions{
		Name:       spec.Name,
		Config:     &dockerclient.Config{Image: spec.Image, Cmd: spec.Cmd, Env: spec.Env, WorkingDir: spec.WorkDir},
		HostConfig: host,
		Context:    ctx,
	}
}
