// Work Sync HTTP surface: the authed code generator plus the public,
// code-driven session routes the laptop client uses. Handlers stay thin;
// state and the apply pipeline live in internal/sync.
package httpapi

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pcoder/internal/obs"
	"pcoder/internal/project"
	worksync "pcoder/internal/sync"
)

//go:embed work_sync.sh
var syncBootstrap string

// syncErrStatus maps stable error codes onto HTTP statuses.
func syncErrStatus(code string) int {
	switch code {
	case worksync.ErrProjectNotFound, worksync.ErrSessionUnknown:
		return http.StatusNotFound
	case worksync.ErrCodeInvalid, worksync.ErrCodeWrongProject, worksync.ErrBadPayload:
		return http.StatusUnauthorized
	case worksync.ErrCodeExpired, worksync.ErrCodeUsed, worksync.ErrSessionExpired:
		return http.StatusGone
	case worksync.ErrSyncRunning:
		return http.StatusConflict
	case worksync.ErrHTTPSRequired:
		return http.StatusForbidden
	case worksync.ErrBranchMismatch, worksync.ErrCommitMismatch, worksync.ErrDetachedHead,
		worksync.ErrNotGitRepo, worksync.ErrRemoteMismatch, worksync.ErrBadPlan:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// writeSyncErr answers with the stable {"error":{code,message,detail}}
// shape. Unknown errors become a generic internal error: raw Go errors
// never reach the user.
func writeSyncErr(w http.ResponseWriter, r *http.Request, err error) {
	var e *worksync.Error
	if !errors.As(err, &e) {
		e = worksync.NewError(worksync.ErrInternal, "Sync failed on pcoder.\nRun sync again; if it fails, check the pcoder logs.")
		obs.Error(r.Context(), obs.SyncError, "sync failed: "+err.Error(), map[string]any{"code": e.Code})
	} else {
		// Expected refusals are user-facing guidance, not server faults:
		// the tail keeps them, the errors inbox stays honest.
		obs.Info(r.Context(), obs.SyncError, "sync refused: "+e.Code, map[string]any{"code": e.Code, "error": e.Message})
	}
	writeJSON(w, syncErrStatus(e.Code), map[string]any{"error": e})
}

// requestScheme is the scheme the browser used, honouring TLS and proxies.
func requestScheme(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "https"
	}
	return "http"
}

// syncSession resolves the live session for the public routes, enforcing
// the transport rule. It answers the error itself.
func syncSession(d Deps, w http.ResponseWriter, r *http.Request) (*worksync.Session, bool) {
	if !syncAllowed(r, d.SyncAllowInsecure) {
		writeSyncErr(w, r, worksync.HTTPSRequired())
		return nil, false
	}
	sess, err := d.Sync.Store().Session(r.PathValue("session"))
	if err != nil {
		writeSyncErr(w, r, err)
		return nil, false
	}
	return sess, true
}

// syncAllowed enforces HTTPS, with loopback allowed for local development
// (and test stacks that opt in).
func syncAllowed(r *http.Request, allowInsecure bool) bool {
	if allowInsecure || r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// syncProjectContainer resolves the running project container and its repo
// dir for the public sync routes. It answers the error itself.
func syncProjectContainer(d Deps, w http.ResponseWriter, r *http.Request) (container, dir, branch, commit string, ok bool) {
	if d.Sync == nil {
		writeSyncErr(w, r, worksync.NewError(worksync.ErrInternal, "Work Sync is not available."))
		return "", "", "", "", false
	}
	id := r.PathValue("id")
	st, err := d.Projects.EnsureContainer(r.Context(), id)
	if err != nil {
		writeSyncErr(w, r, worksync.ProjectNotFound(id))
		return "", "", "", "", false
	}
	if st.State != project.StateRunning {
		writeSyncErr(w, r, worksync.ProjectNotFound(id))
		return "", "", "", "", false
	}
	container = project.ContainerName(id)
	dir, err = d.Sessions.RepoTarget(r.Context(), container)
	if err != nil {
		writeSyncErr(w, r, err)
		return "", "", "", "", false
	}
	branch, commit, err = d.Sync.Git(r.Context(), container, dir)
	if err != nil {
		writeSyncErr(w, r, err)
		return "", "", "", "", false
	}
	return container, dir, branch, commit, true
}

// handleSyncCode generates a project's sync code. Authed: this is the pcoder
// side of the journey.
func handleSyncCode(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok := ensureRunning(d, w, r); !ok {
			return
		}
		code, expires, err := d.Sync.Generate(id)
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		obs.Info(r.Context(), obs.SyncCode, "sync code generated", map[string]any{
			"code": code, "expiresAt": expires.UTC().Format(time.RFC3339),
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"code":      code,
			"project":   id,
			"expiresAt": expires,
			"command":   "curl -fsSL " + requestScheme(r) + "://" + r.Host + "/sync.sh | sh",
		})
	}
}

// handleSyncStatus reports whether a transfer is live for the project, so
// the web UI can block interaction while it runs.
func handleSyncStatus(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Sync == nil {
			writeJSON(w, http.StatusOK, map[string]string{"status": "idle"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": d.Sync.Store().ProjectStatus(r.PathValue("id"))})
	}
}

// handleSyncAbort ends a session the laptop cancelled or failed.
func handleSyncAbort(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !syncAllowed(r, d.SyncAllowInsecure) {
			writeSyncErr(w, r, worksync.HTTPSRequired())
			return
		}
		obs.Info(r.Context(), obs.SyncAbort, "sync aborted", map[string]any{"session": r.PathValue("session")})
		d.Sync.Abort(r.PathValue("session"))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleSyncHello validates the project and code, and opens (or reopens) the
// one session bound to them.
func handleSyncHello(d Deps) http.HandlerFunc {
	type body struct {
		Project string `json:"project"`
		Code    string `json:"code"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !syncAllowed(r, d.SyncAllowInsecure) {
			writeSyncErr(w, r, worksync.HTTPSRequired())
			return
		}
		var b body
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&b); err != nil {
			writeSyncErr(w, r, worksync.BadPlan("The hello request is not valid JSON."))
			return
		}
		id := strings.TrimSpace(b.Project)
		if id == "" || !strings.Contains(id, "/") {
			writeSyncErr(w, r, worksync.ProjectNotFound(id))
			return
		}
		r.SetPathValue("id", id)
		container, dir, branch, commit, ok := syncProjectContainer(d, w, r)
		if !ok {
			return
		}
		sess, err := d.Sync.Hello(r.Context(), id, b.Code, container, dir, branch, commit)
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		obs.Info(r.Context(), obs.SyncHello, "sync session opened", map[string]any{"session": sess.ID})
		writeJSON(w, http.StatusOK, map[string]any{
			"session":   sess.ID,
			"nonce":     sess.Nonce,
			"expiresAt": sess.ExpiresAt,
			"project":   sess.Project,
			"branch":    sess.Branch,
			"commit":    sess.Commit,
		})
	}
}

// handleSyncPlan binds the direction and returns the delta plus the
// server-side manifest.
func handleSyncPlan(d Deps) http.HandlerFunc {
	type body struct {
		Direction worksync.Direction `json:"direction"`
		Manifest  worksync.Manifest  `json:"manifest"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := syncSession(d, w, r)
		if !ok {
			return
		}
		var b body
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&b); err != nil {
			writeSyncErr(w, r, worksync.BadPlan("The plan request is not valid JSON."))
			return
		}
		server, ops, err := d.Sync.Plan(r.Context(), sess, b.Direction, b.Manifest)
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		added, overwritten, deleted := worksync.Counts(ops)
		obs.Info(r.Context(), obs.SyncPlan, fmt.Sprintf("%s plan: %d ops", b.Direction, len(ops)), map[string]any{
			"session": sess.ID, "added": added, "overwritten": overwritten, "deleted": deleted,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"project":  sess.Project,
			"branch":   sess.Branch,
			"commit":   sess.Commit,
			"manifest": server,
			"ops":      ops,
			"counts":   map[string]int{"added": added, "overwritten": overwritten, "deleted": deleted},
		})
	}
}

// handleSyncSend receives the encrypted tar and applies it to the project.
func handleSyncSend(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := syncSession(d, w, r)
		if !ok {
			return
		}
		res, err := d.Sync.Send(r.Context(), sess, http.MaxBytesReader(w, r.Body, 1<<30))
		if err != nil {
			writeSyncErr(w, r, err)
			return
		}
		obs.Info(r.Context(), obs.SyncUpload, "sync upload applied", map[string]any{
			"session": sess.ID, "applied": res.Applied, "deleted": res.Deleted, "backup": res.Backup,
		})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": res.Applied, "deleted": res.Deleted, "backup": res.Backup})
	}
}

// handleSyncReceive streams the encrypted tar of the server's changed files.
func handleSyncReceive(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := syncSession(d, w, r)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		obs.Info(r.Context(), obs.SyncDownload, "sync download streamed", map[string]any{"session": sess.ID})
		if err := d.Sync.Receive(r.Context(), sess, w); err != nil {
			// Headers may already be out; the client sees a truncated body
			// and reports a failed payload instead of a silent success.
			writeSyncErr(w, r, err)
		}
	}
}

// handleSyncClient serves the static client binary and its checksum. The
// bootstrap owns dependency checks, download, and verification.
func handleSyncClient(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := filepath.Base(r.PathValue("target"))
		wantSHA := strings.HasSuffix(target, ".sha256")
		target = strings.TrimSuffix(target, ".sha256")
		if target != "darwin-amd64" && target != "darwin-arm64" && target != "linux-amd64" && target != "linux-arm64" {
			writeSyncErr(w, r, worksync.NewError(worksync.ErrProjectNotFound, "Unsupported platform."))
			return
		}
		data, err := os.ReadFile(filepath.Join(d.SyncBinDir, target, "pcoder-sync"))
		if err != nil {
			writeSyncErr(w, r, worksync.NewError(worksync.ErrInternal, "This pcoder has no client binary for "+target+".\nRebuild the server image."))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if wantSHA {
			sum := sha256.Sum256(data)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "\n"))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	}
}

// handleSyncBootstrap serves the POSIX launcher, with this server's URL and
// version baked in.
func handleSyncBootstrap(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		script := strings.NewReplacer(
			"__SERVER__", requestScheme(r)+"://"+r.Host,
			"__VERSION__", d.Version,
		).Replace(syncBootstrap)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(script))
	}
}
