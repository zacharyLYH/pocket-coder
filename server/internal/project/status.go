package project

import (
	"context"
	"errors"

	"sps/internal/docker"
)

// Live container status mapping. The strings surface verbatim in the HTTP
// API (GET /api/projects/{id} → "status"), so they are stable.

// State values for a project's container.
const (
	StateMissing = "missing" // no container (never created, or scope=container deleted)
	StateRunning = "running"
)

// Status is the live state of one container.
type Status struct {
	State string
}

// ContainerStatus inspects name and maps it to a Status. A missing container
// is a valid status, not an error.
func ContainerStatus(ctx context.Context, d docker.Client, name string) (Status, error) {
	c, err := d.Inspect(ctx, name)
	if errors.Is(err, docker.ErrNotFound) {
		return Status{State: StateMissing}, nil
	}
	if err != nil {
		return Status{}, err
	}
	if c.Running {
		return Status{State: StateRunning}, nil
	}
	return Status{State: c.Status}, nil
}
