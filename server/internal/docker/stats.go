package docker

import (
	"context"
	"fmt"
	"time"

	dockerclient "github.com/fsouza/go-dockerclient"
)

// ContainerStats is one resource sample for a container: everything the
// observe ResourceSample needs except disk (df via exec) and state (inspect).
type ContainerStats struct {
	CPUPercent float64
	MemUsed    uint64
	MemLimit   uint64
	NetRX      uint64
	NetTX      uint64
	BlockRead  uint64
	BlockWrite uint64
	PIDs       int
}

// Stats takes one non-streaming sample for id. Stopped/missing containers
// are ErrNotFound; anything else is an engine failure. Callers treat any
// error as "unknown right now" and keep last-known values.
func (d *Docker) Stats(ctx context.Context, id string) (ContainerStats, error) {
	ch := make(chan *dockerclient.Stats, 1)
	done := make(chan bool, 1)
	defer close(done)
	err := d.c.Stats(dockerclient.StatsOptions{
		ID: id, Stats: ch, Stream: false, Done: done,
		Timeout: 5 * time.Second, InactivityTimeout: 5 * time.Second, Context: ctx,
	})
	if err != nil {
		return ContainerStats{}, wrapNotFound(id, err)
	}
	select {
	case s, ok := <-ch:
		if !ok || s == nil {
			return ContainerStats{}, fmt.Errorf("stats container %s: no sample", id)
		}
		return SummarizeStats(s), nil
	case <-ctx.Done():
		return ContainerStats{}, fmt.Errorf("stats container %s: %w", id, ctx.Err())
	}
}

// SummarizeStats folds one engine sample into ContainerStats. Pure so it is
// unit-testable without an engine. CPU comes from the sample's own
// precpu window (same formula as obs.CpuPercent, kept local so this
// package stays dependency-free); mem is usage/limit straight through;
// network sums every interface; blkio sums read/write service bytes.
func SummarizeStats(s *dockerclient.Stats) ContainerStats {
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage - s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemCPUUsage - s.PreCPUStats.SystemCPUUsage)
	cpus := int(s.CPUStats.OnlineCPUs)
	if cpus <= 0 {
		cpus = len(s.CPUStats.CPUUsage.PercpuUsage)
	}
	var cpu float64
	if sysDelta > 0 && cpus > 0 {
		cpu = cpuDelta / sysDelta * float64(cpus) * 100
	}
	out := ContainerStats{
		CPUPercent: cpu,
		MemUsed:    s.MemoryStats.Usage,
		MemLimit:   s.MemoryStats.Limit,
		PIDs:       int(s.PidsStats.Current),
	}
	if out.PIDs == 0 {
		out.PIDs = int(s.NumProcs)
	}
	for _, n := range s.Networks {
		out.NetRX += n.RxBytes
		out.NetTX += n.TxBytes
	}
	// Legacy single-network payloads (no Networks map) still carry totals.
	if len(s.Networks) == 0 {
		out.NetRX = s.Network.RxBytes
		out.NetTX = s.Network.TxBytes
	}
	for _, e := range s.BlkioStats.IOServiceBytesRecursive {
		switch e.Op {
		case "Read":
			out.BlockRead += e.Value
		case "Write":
			out.BlockWrite += e.Value
		}
	}
	return out
}
