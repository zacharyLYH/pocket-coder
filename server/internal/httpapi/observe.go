package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pcoder/internal/docker"
	"pcoder/internal/obs"
	"pcoder/internal/project"
)

// Observe meta API: the canonical taxonomy (audit + build key sets) so
// readers filter by milestone without hardcoding keys that drift.
func handleObserveMeta(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"auditTypes": obs.AuditTypes,
			"buildTypes": obs.BuildTypes,
		})
	}
}

// Observe read API: seek-from-end tail with seq cursors + facet filters.
// Reads never log success (the tail polls); only malformed cursors land in
// the errors inbox, and even those are rare.
func handleObserve(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.ObserveRead, "observe read failed", err, nil)
			}
		}()
		q := r.URL.Query()
		after, before := parseSeq(q.Get("after")), parseSeq(q.Get("before"))
		if after < 0 || before < 0 {
			err = errors.New("after and before must be numbers")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		limit := 200
		if v := q.Get("limit"); v != "" {
			n, aerr := strconv.Atoi(v)
			if aerr != nil || n < 0 {
				err = errors.New("limit must be a non-negative number")
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			limit = n
		}
		if limit > 1000 {
			limit = 1000
		}
		var logs []obs.Entry
		var first, last int64
		if d.Obs != nil {
			logs, first, last = d.Obs.Read(r.PathValue("id"), after, before, limit,
				q.Get("level"), q.Get("source"), q.Get("type"), q.Get("trace"), q.Get("q"))
		}
		if logs == nil {
			logs = []obs.Entry{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "firstSeq": first, "lastSeq": last})
	}
}

func parseSeq(v string) int64 {
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// Observe errors inbox: projection over level==error, top 20 by count.
func handleObserveErrors(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var groups []obs.Group
		if d.Obs != nil {
			groups = d.Obs.Errors(r.PathValue("id"))
		}
		if groups == nil {
			groups = []obs.Group{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
	}
}

// Observe stats: live state + best-effort engine stats (CPU/mem/net/blkio)
// + best-effort disk via Exec df. Engine and disk failures stay silent:
// the frontend polls every 3s and keeps last-known values, so a gap reads
// as a flat line, not an error.
func handleObserveStats(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		state := "missing"
		if d.Projects != nil {
			if _, st, err := d.Projects.Get(r.Context(), id); err == nil {
				state = st.State
			}
		}
		sample := obs.ResourceSample{TS: time.Now().UTC(), State: state}
		if state == project.StateRunning && d.Docker != nil {
			if cs, err := d.Docker.Stats(r.Context(), project.ContainerName(id)); err == nil {
				sample.CPUPercent = cs.CPUPercent
				sample.MemUsed = cs.MemUsed
				sample.MemLimit = cs.MemLimit
				sample.NetRX = cs.NetRX
				sample.NetTX = cs.NetTX
				sample.BlockR = cs.BlockRead
				sample.BlockW = cs.BlockWrite
				sample.PIDs = cs.PIDs
			}
			if used, total, err := diskUsage(r.Context(), d.Docker, project.ContainerName(id)); err == nil {
				sample.DiskUsed, sample.DiskTotal = used, total
			}
		}
		writeJSON(w, http.StatusOK, sample)
	}
}

// diskUsage runs df -k /workspace inside the container and parses the
// data line (Filesystem 1K-blocks Used Available Use% Mounted).
func diskUsage(ctx context.Context, dkr docker.Client, container string) (uint64, uint64, error) {
	res, err := dkr.Exec(ctx, container, []string{"df", "-k", "/workspace"}, false)
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(res.Output), "\n")
	if len(lines) < 2 {
		return 0, 0, errDisk
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return 0, 0, errDisk
	}
	total, err1 := strconv.ParseUint(f[1], 10, 64)
	used, err2 := strconv.ParseUint(f[2], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, errDisk
	}
	return used * 1024, total * 1024, nil
}

var errDisk = errors.New("df parse failed")
