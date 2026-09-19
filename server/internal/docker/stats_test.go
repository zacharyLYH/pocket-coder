package docker

import (
	"testing"

	dockerclient "github.com/fsouza/go-dockerclient"
)

// SummarizeStats is pure: a fabricated engine sample covers CPU delta,
// mem, interface-summed network, read/write blkio, and PIDs without an
// engine.
func TestSummarizeStats(t *testing.T) {
	s := &dockerclient.Stats{}
	s.CPUStats.CPUUsage.TotalUsage = 200
	s.CPUStats.CPUUsage.PercpuUsage = []uint64{100, 100}
	s.CPUStats.SystemCPUUsage = 1000
	s.CPUStats.OnlineCPUs = 2
	s.PreCPUStats.CPUUsage.TotalUsage = 100
	s.PreCPUStats.SystemCPUUsage = 500
	s.MemoryStats.Usage = 512 << 20
	s.MemoryStats.Limit = 16 << 30
	s.Networks = map[string]dockerclient.NetworkStats{
		"eth0": {RxBytes: 100, TxBytes: 200},
		"eth1": {RxBytes: 50, TxBytes: 25},
	}
	s.BlkioStats.IOServiceBytesRecursive = []dockerclient.BlkioStatsEntry{
		{Op: "Read", Value: 1024},
		{Op: "Write", Value: 2048},
		{Op: "Total", Value: 3072},
	}
	s.PidsStats.Current = 7

	got := SummarizeStats(s)
	// cpuDelta=100 sysDelta=500 cpus=2 → 40%.
	if got.CPUPercent != 40 {
		t.Fatalf("cpu = %v, want 40", got.CPUPercent)
	}
	if got.MemUsed != 512<<20 || got.MemLimit != 16<<30 {
		t.Fatalf("mem = %v/%v", got.MemUsed, got.MemLimit)
	}
	if got.NetRX != 150 || got.NetTX != 225 {
		t.Fatalf("net = %v/%v, want 150/225", got.NetRX, got.NetTX)
	}
	if got.BlockRead != 1024 || got.BlockWrite != 2048 {
		t.Fatalf("blkio = %v/%v", got.BlockRead, got.BlockWrite)
	}
	if got.PIDs != 7 {
		t.Fatalf("pids = %v, want 7", got.PIDs)
	}
}

// Zero precpu window (fresh container, first sample) is 0% CPU, never NaN.
func TestSummarizeStatsZeroWindow(t *testing.T) {
	got := SummarizeStats(&dockerclient.Stats{})
	if got.CPUPercent != 0 {
		t.Fatalf("cpu = %v, want 0", got.CPUPercent)
	}
}
