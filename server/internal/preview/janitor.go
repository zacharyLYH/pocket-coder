package preview

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"pcoder/internal/docker"
	"pcoder/internal/project"
)

// sidecarNamePrefix names one project's preview sidecar; the Docker Desktop
// relay that forwards its ports is relayNamePrefix.
const sidecarNamePrefix = "pcoder-preview-"

// ContainerLister is the Docker slice the janitor needs: find containers by
// name prefix and remove them. *docker.Docker implements it.
type ContainerLister interface {
	ListContainers(ctx context.Context, namePrefix string) ([]docker.ContainerInfo, error)
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Remove(ctx context.Context, id string, force bool) error
}

// SweepContainers removes preview sidecars (and their relays) that no live
// worker owns. Manager state is in memory, so a server that dies without
// closing its workers — SIGKILL, a crashed host, a shutdown that ran out of
// its grace period — leaves pcoder-preview-* containers behind and nothing
// would ever remove them. Ownership is read from the container name itself
// (pcoder-preview-<sanitized project id>, the same slug Start used), so a
// container whose slug has no live worker is a leftover.
//
// A live worker's containers are never touched: its Stop/Close owns removal,
// and a preview a user is watching must survive a sweep.
func (m *Manager) SweepContainers(ctx context.Context, d ContainerLister) (removed []string, err error) {
	containers, err := d.ListContainers(ctx, sidecarNamePrefix)
	if err != nil {
		return nil, err
	}
	live := m.liveSlugs()
	var firstErr error
	for _, c := range containers {
		if live[previewSlug(c.Name)] {
			continue
		}
		if serr := d.Stop(ctx, c.ID, 5*time.Second); serr != nil && firstErr == nil {
			firstErr = fmt.Errorf("stop orphaned %s: %w", c.Name, serr)
		}
		if rerr := d.Remove(ctx, c.ID, true); rerr != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove orphaned %s: %w", c.Name, rerr)
		}
		removed = append(removed, c.Name)
	}
	if len(removed) > 0 {
		slog.Info("swept orphaned preview containers", "containers", removed)
	}
	return removed, firstErr
}

// liveSlugs maps the container-name slug of every project with a live worker
// (or a start in flight). The manager is the ownership record: sidecars are
// named after it. In-flight starts count as owned because the container is
// created before Start returns, and sweeping it mid-start would kill the
// preview being opened.
func (m *Manager) liveSlugs() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	slugs := make(map[string]bool, len(m.workers)+len(m.inflight))
	for id := range m.workers {
		slugs[project.SanitizeName(id)] = true
	}
	for id := range m.inflight {
		slugs[project.SanitizeName(id)] = true
	}
	return slugs
}

// previewSlug resolves a preview container name to the sanitized project slug
// it came from: both "pcoder-preview-<slug>" (sidecar) and
// "pcoder-preview-relay-<slug>" (relay) resolve to <slug>.
func previewSlug(name string) string {
	return strings.TrimPrefix(strings.TrimPrefix(name, sidecarNamePrefix), "relay-")
}
