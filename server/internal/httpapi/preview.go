package httpapi

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"sps/internal/preview"
	"sps/internal/project"
)

func handlePreviewStatus(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		worker, err := d.Preview.Get(id)
		if errors.Is(err, preview.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"project": id, "status": "stopped"})
			return
		}
		if err != nil {
			writeInternalErr(w, "get preview", err)
			return
		}
		_ = worker
		writeJSON(w, http.StatusOK, map[string]any{
			"project": id, "status": "ready", "surface": "/api/projects/" + id + "/preview/vnc.html",
		})
	}
}

func handlePreviewStart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Port int `json:"port"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if body.Port < 1 || body.Port > 65535 {
			writeErr(w, http.StatusBadRequest, "invalid port")
			return
		}
		worker, err := ensurePreviewWorker(d, r)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) || errors.Is(err, preview.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "project not found")
				return
			}
			writeInternalErr(w, "ensure preview", err)
			return
		}
		ep := worker.Endpoint()
		s, err := getCDP(r.PathValue("id"), ep.CDP)
		if err != nil {
			writeInternalErr(w, "cdp connect", err)
			return
		}
		url := "http://127.0.0.1:" + strconv.Itoa(body.Port)
		_, _ = s.call(r.Context(), "Page.navigate", map[string]any{"url": url})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "port": body.Port, "status": "ready"})
	}
}

func handlePreviewClose(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := d.Preview.Stop(r.Context(), id); err != nil {
			if errors.Is(err, preview.ErrNotFound) {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "stopped"})
				return
			}
			writeInternalErr(w, "stop preview", err)
			return
		}
		// clear cached CDP session
		cdpMu.Lock()
		if s, ok := cdpCache[id]; ok {
			_ = s.ws.Close()
			delete(cdpCache, id)
		}
		cdpMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "stopped"})
	}
}

// handlePreviewSurface forwards noVNC assets and its websocket through the
// authenticated SPS origin. The worker endpoint is private and never placed
// in a Location header or JSON response.
func handlePreviewSurface(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worker, err := ensurePreviewWorker(d, r)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) || errors.Is(err, preview.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "preview is not running")
				return
			}
			writeInternalErr(w, "get preview surface", err)
			return
		}
		ep := worker.Endpoint()
		target, err := url.Parse(ep.Display)
		if err != nil || target.Scheme != "http" || target.Host == "" {
			if err == nil {
				err = errors.New("invalid private display endpoint")
			}
			writeInternalErr(w, "parse preview surface endpoint", err)
			return
		}
		prefix := "/api/projects/" + r.PathValue("id") + "/preview/"
		path := strings.TrimPrefix(r.URL.Path, prefix)
		if path == "" {
			path = "vnc.html"
		}
		proxy := newPreviewProxy(target, path)
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
			writeInternalErr(w, "proxy preview surface", proxyErr)
		}
		proxy.ServeHTTP(w, r)
	}
}

func newPreviewProxy(target *url.URL, path string) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = "/" + strings.TrimPrefix(path, "/")
		req.URL.RawPath = ""
		req.Host = target.Host
	}
	return proxy
}

// handlePreviewPorts probes listening ports inside the project container.
func handlePreviewPorts(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if d.Projects == nil {
			writeErr(w, http.StatusNotFound, "no projects")
			return
		}
		if _, _, err := d.Projects.Get(r.Context(), id); err != nil {
			writeErr(w, http.StatusNotFound, "no such project")
			return
		}
		cid := project.ContainerName(id)
		output, err := d.Sessions.ExecCommand(r.Context(), cid, "ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null || true")
		if err != nil {
			// Container may not be running — return empty slots.
			writeJSON(w, http.StatusOK, map[string]any{"ports": []map[string]any{}})
			return
		}
		ports := parseListeningPorts(output)
		writeJSON(w, http.StatusOK, map[string]any{"ports": ports})
	}
}

// parseListeningPorts extracts port numbers from ss/netstat output.
func parseListeningPorts(output string) []map[string]any {
	seen := map[int]bool{}
	var slots []map[string]any
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "State") || strings.HasPrefix(line, "Netid") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if i := strings.LastIndex(field, ":"); i > 0 {
				p, err := strconv.Atoi(field[i+1:])
				if err == nil && p > 0 && p < 65536 && !seen[p] {
					seen[p] = true
					slots = append(slots, map[string]any{"port": p, "status": "live"})
				}
			}
		}
	}
	return slots
}

func ensurePreviewWorker(d Deps, r *http.Request) (preview.Worker, error) {
	id := r.PathValue("id")
	if d.Projects == nil {
		return d.Preview.Get(id)
	}
	if _, _, err := d.Projects.Get(r.Context(), id); err != nil {
		return nil, err
	}
	return d.Preview.Ensure(r.Context(), preview.Config{
		ProjectID: id, ContainerID: project.ContainerName(id),
	})
}
