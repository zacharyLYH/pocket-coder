// Package docker is the server's only interface to the Docker engine: a
// thin, boring wrapper over go-dockerclient. No raw docker API leaks past
// this package — callers depend on the Client interface (mockable) and get
// typed errors.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	dockerclient "github.com/fsouza/go-dockerclient"
)

// Sentinel errors. Always wrap with %w so callers can errors.Is.
var (
	// ErrUnavailable means the Docker engine is unreachable.
	ErrUnavailable = errors.New("docker unavailable")
	// ErrNotFound means a container does not exist (or was already removed).
	ErrNotFound = errors.New("container not found")
)

// DefaultEndpoint is used when SPS_DOCKER_SOCK is unset.
const DefaultEndpoint = "unix:///var/run/docker.sock"

// Client is everything the server needs from Docker. Defined as an interface
// so consumers can be tested with mocks instead of a real engine.
type Client interface {
	EnsureNetwork(ctx context.Context, name string) error
	Build(ctx context.Context, opts BuildOptions, log io.Writer) error
	InspectImage(ctx context.Context, name string) error
	Create(ctx context.Context, spec Spec) (string, error)
	Start(ctx context.Context, id string) error
	Run(ctx context.Context, spec Spec) (string, error)
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Remove(ctx context.Context, id string, force bool) error
	RemoveVolume(ctx context.Context, name string) error
	Inspect(ctx context.Context, id string) (Container, error)
	Exec(ctx context.Context, id string, cmd []string, tty bool) (ExecResult, error)
	Attach(ctx context.Context, id string, cmd []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) (string, <-chan ExecDone, error)
	ResizeTTY(ctx context.Context, execID string, height, width int) error
	WriteFile(ctx context.Context, id, path string, content []byte) error
}

// Container is the inspect summary the server needs — never the raw docker type.
type Container struct {
	ID      string
	Running bool
	Status  string
	Image   string
}

// Docker implements Client over go-dockerclient.
type Docker struct {
	c *dockerclient.Client
}

// New returns a Docker client for endpoint (unix socket or tcp). It does not
// touch the engine — call Ping to check availability.
func New(endpoint string) (*Docker, error) {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	c, err := dockerclient.NewClient(endpoint)
	if err != nil {
		return nil, fmt.Errorf("docker client for %s: %w", endpoint, err)
	}
	return &Docker{c: c}, nil
}

// Ping reports whether the engine is reachable.
func (d *Docker) Ping(ctx context.Context) error {
	if err := d.c.PingWithContext(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// EnsureNetwork creates name (bridge driver) if it does not exist yet.
func (d *Docker) EnsureNetwork(ctx context.Context, name string) error {
	nets, err := d.c.ListNetworks()
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	for _, n := range nets {
		if n.Name == name {
			return nil
		}
	}
	if _, err := d.c.CreateNetwork(dockerclient.CreateNetworkOptions{Name: name, Driver: "bridge", CheckDuplicate: true}); err != nil {
		return fmt.Errorf("create network %s: %w", name, err)
	}
	return nil
}

// Create creates a container from spec and returns its id.
func (d *Docker) Create(ctx context.Context, spec Spec) (string, error) {
	c, err := d.c.CreateContainer(containerOptions(ctx, spec))
	if err != nil {
		return "", fmt.Errorf("create container %s: %w", spec.Name, err)
	}
	return c.ID, nil
}

// Start starts a container.
func (d *Docker) Start(ctx context.Context, id string) error {
	if err := d.c.StartContainerWithContext(id, nil, ctx); err != nil {
		return fmt.Errorf("start container %s: %w", id, err)
	}
	return nil
}

// Run creates and starts a container, cleaning up on start failure.
func (d *Docker) Run(ctx context.Context, spec Spec) (string, error) {
	id, err := d.Create(ctx, spec)
	if err != nil {
		return "", err
	}
	if err := d.Start(ctx, id); err != nil {
		_ = d.Remove(ctx, id, true)
		return "", err
	}
	return id, nil
}

// Stop stops a container, giving it timeout to exit before SIGKILL.
// Stopping an already-stopped container is not an error (idempotent stop).
func (d *Docker) Stop(ctx context.Context, id string, timeout time.Duration) error {
	err := d.c.StopContainerWithContext(id, uint(timeout.Seconds()), ctx)
	if err != nil && !isAlreadyStopped(err) {
		return fmt.Errorf("stop container %s: %w", id, err)
	}
	return nil
}

// isAlreadyStopped reports the engine's "not running" responses, which
// describe an already-satisfied stop.
func isAlreadyStopped(err error) bool {
	var nr *dockerclient.ContainerNotRunning
	return errors.As(err, &nr)
}

// Remove removes a container and its volumes. Removing an already-removed
// container is not an error (idempotent delete).
func (d *Docker) Remove(ctx context.Context, id string, force bool) error {
	err := d.c.RemoveContainer(dockerclient.RemoveContainerOptions{ID: id, Force: force, RemoveVolumes: true, Context: ctx})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	return nil
}

// Inspect returns the container's current state.
func (d *Docker) Inspect(ctx context.Context, id string) (Container, error) {
	c, err := d.c.InspectContainerWithContext(id, ctx)
	if err != nil {
		return Container{}, wrapNotFound(id, err)
	}
	return Container{
		ID:      c.ID,
		Running: c.State.Running,
		Status:  c.State.Status,
		Image:   c.Config.Image,
	}, nil
}

// InspectImage reports whether an image exists locally. ErrNotFound when
// it does not; other errors are engine failures.
func (d *Docker) InspectImage(ctx context.Context, name string) error {
	_, err := d.c.InspectImage(name)
	if err != nil {
		if errors.Is(err, dockerclient.ErrNoSuchImage) {
			return fmt.Errorf("%w: image %s", ErrNotFound, name)
		}
		return fmt.Errorf("inspect image %s: %w", name, err)
	}
	return nil
}

// RemoveVolume removes a named volume. Removing an already-removed volume
// is not an error (idempotent delete).
func (d *Docker) RemoveVolume(ctx context.Context, name string) error {
	err := d.c.RemoveVolumeWithOptions(dockerclient.RemoveVolumeOptions{Name: name, Context: ctx})
	if err != nil && !isNotFoundVolume(err) {
		return fmt.Errorf("remove volume %s: %w", name, err)
	}
	return nil
}

func isNotFoundVolume(err error) bool {
	return errors.Is(err, dockerclient.ErrNoSuchVolume)
}

func isNotFound(err error) bool {
	return errors.As(err, new(*dockerclient.NoSuchContainer))
}

func wrapNotFound(id string, err error) error {
	if isNotFound(err) {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return fmt.Errorf("inspect container %s: %w", id, err)
}
