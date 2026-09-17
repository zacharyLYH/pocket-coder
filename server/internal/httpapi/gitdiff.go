// Git diff endpoints: read-only status/diff plus file and hunk staging.
// All git work runs inside the project container through Sessions,
// scoped to the repo dir (see session.RepoTarget). No commit, push, or
// discard lives here on purpose: the diff tab stages, the terminal owns
// everything else.
package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"pcoder/internal/project"
)

// gitFile is one row in the status response.
type gitFile struct {
	Path        string `json:"path"`
	Staged      string `json:"staged"`
	Unstaged    string `json:"unstaged"`
	StagedAdd   int    `json:"stagedAdd"`
	StagedDel   int    `json:"stagedDel"`
	UnstagedAdd int    `json:"unstagedAdd"`
	UnstagedDel int    `json:"unstagedDel"`
	Binary      bool   `json:"binary"`
}

// shellQuote wraps s in single quotes for `bash -lc` embedding.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// validGitPath rejects empty, absolute, parent-escaping, and empty-segment
// paths. The empty-segment check matches codemap's validRepoPath so the
// file overlay and the read_file tool accept the same path shapes.
func validGitPath(p string) bool {
	if p == "" || len(p) > 1024 || strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "" {
			return false
		}
	}
	return true
}

// gitRepoDir resolves the container and its repo dir, ensuring the project
// exists and the container runs. It writes the error response on failure.
func gitRepoDir(d Deps, w http.ResponseWriter, r *http.Request) (container, dir string, ok bool) {
	id := r.PathValue("id")
	if d.Projects == nil || d.Sessions == nil {
		writeErr(w, http.StatusNotFound, "no such project")
		return "", "", false
	}
	st, err := d.Projects.EnsureContainer(r.Context(), id)
	if err != nil {
		if err == project.ErrNotFound {
			writeErr(w, http.StatusNotFound, "no such project")
		} else {
			writeInternalErr(w, "ensure container", err)
		}
		return "", "", false
	}
	if st.State != project.StateRunning {
		writeErr(w, http.StatusConflict, "container not running")
		return "", "", false
	}
	container = project.ContainerName(id)
	dir, err = d.Sessions.RepoTarget(r.Context(), container)
	if err != nil {
		writeInternalErr(w, "repo target", err)
		return "", "", false
	}
	return container, dir, true
}

// parsePorcelain splits `git status --porcelain=v1` into path -> [X, Y].
// Rename entries ("R  old -> new") key on the new side.
func parsePorcelain(out string) map[string][2]string {
	files := map[string][2]string{}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		x, y := string(line[0]), string(line[1])
		path := line[3:]
		// Quoted paths ("a b.txt") come wrapped in quotes; strip them so
		// the key matches numstat and diff paths.
		if len(path) >= 2 && strings.HasPrefix(path, `"`) && strings.HasSuffix(path, `"`) {
			if unq, err := strconv.Unquote(path); err == nil {
				path = unq
			}
		}
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		files[path] = [2]string{x, y}
	}
	return files
}

// parseNumstat fills add/del counts from `git diff --numstat` output into
// counts[path]. Binary entries ("- - path") mark binary[path].
func parseNumstat(out string, counts map[string][2]int, binary map[string]bool) {
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if i := strings.Index(path, " => "); i >= 0 {
			path = path[i+4:]
		}
		path = strings.Trim(path, "{}")
		if parts[0] == "-" {
			binary[path] = true
			continue
		}
		var add, del int
		_, _ = fmt.Sscanf(parts[0], "%d", &add)
		_, _ = fmt.Sscanf(parts[1], "%d", &del)
		counts[path] = [2]int{add, del}
	}
}

func handleGitStatus(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			return
		}
		qd := shellQuote(dir)
		// ExecCommand trims output, which would eat the leading XY space
		// of a first porcelain line like " M notes.txt" (unstaged-only
		// change: X is a space). A sentinel first line keeps every entry
		// line byte-intact; the handler drops it before parsing.
		// git diff exits 1 on differences; `; true` keeps ExecCommand happy.
		raw, err := d.Sessions.ExecCommand(r.Context(), container,
			"echo STATUS-BEGIN; git -C "+qd+" status --porcelain=v1 --untracked-files=normal")
		if err != nil {
			if strings.Contains(err.Error(), "not a git repository") {
				writeJSON(w, http.StatusOK, map[string]any{"branch": "", "files": []gitFile{}, "notRepo": true})
				return
			}
			writeInternalErr(w, "git status", err)
			return
		}
		porcelain := ""
		if i := strings.Index(raw, "\n"); i >= 0 {
			porcelain = raw[i+1:]
		}
		branch, _ := d.Sessions.ExecCommand(r.Context(), container,
			"git -C "+qd+" rev-parse --abbrev-ref HEAD")
		unstagedStat, _ := d.Sessions.ExecCommand(r.Context(), container,
			"git -C "+qd+" diff --numstat; true")
		stagedStat, _ := d.Sessions.ExecCommand(r.Context(), container,
			"git -C "+qd+" diff --cached --numstat; true")

		xy := parsePorcelain(porcelain)
		unstaged := map[string][2]int{}
		staged := map[string][2]int{}
		binary := map[string]bool{}
		parseNumstat(unstagedStat, unstaged, binary)
		parseNumstat(stagedStat, staged, binary)

		files := make([]gitFile, 0, len(xy))
		for path, codes := range xy {
			u := unstaged[path]
			s := staged[path]
			files = append(files, gitFile{
				Path: path, Staged: codes[0], Unstaged: codes[1],
				StagedAdd: s[0], StagedDel: s[1],
				UnstagedAdd: u[0], UnstagedDel: u[1],
				Binary: binary[path],
			})
		}
		if files == nil {
			files = []gitFile{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"branch": branch, "files": files})
	}
}

func handleGitDiff(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			return
		}
		path := r.URL.Query().Get("path")
		if !validGitPath(path) {
			writeErr(w, http.StatusBadRequest, "invalid path")
			return
		}
		args := " diff -U3 -- "
		if r.URL.Query().Get("staged") == "true" {
			args = " diff --cached -U3 -- "
		}
		out, err := d.Sessions.ExecCommand(r.Context(), container,
			"git -C "+shellQuote(dir)+args+shellQuote(path)+"; true")
		if err != nil {
			writeInternalErr(w, "git diff", err)
			return
		}
		// Untracked files never appear in git diff; fall back to showing
		// the new file as one all-added hunk so the viewer still has
		// something to render and quote from.
		if strings.TrimSpace(out) == "" {
			content, cerr := d.Sessions.ExecCommand(r.Context(), container,
				"git -C "+shellQuote(dir)+" check-ignore -q "+shellQuote(path)+" && echo IGNORED || "+
					"git -C "+shellQuote(dir)+" ls-files --others --exclude-standard -- "+shellQuote(path))
			if cerr == nil && strings.TrimSpace(content) == path {
				raw, rerr := d.Sessions.ExecCommand(r.Context(), container,
					"git -C "+shellQuote(dir)+" diff --no-index -- /dev/null "+shellQuote(path)+"; true")
				if rerr == nil && strings.TrimSpace(raw) != "" {
					out = raw
				}
			}
		}
		truncated := false
		const maxDiff = 300 * 1024
		if len(out) > maxDiff {
			out = out[:maxDiff]
			truncated = true
		}
		oldContent, newContent, contentsTruncated, binary := gitFileContents(
			r.Context(), d, container, dir, path, r.URL.Query().Get("staged") == "true")
		writeJSON(w, http.StatusOK, map[string]any{
			"path": path, "diff": out, "truncated": truncated,
			"oldContent": oldContent, "newContent": newContent,
			"contentsTruncated": contentsTruncated, "binary": binary,
		})
	}
}

// gitFileContents returns the old/new blob texts the viewer renders hunk
// lines against. The diff viewer builds its rows from file contents, not
// from hunks alone, so hunks without contents render empty. Each side is
// capped; NUL bytes mark binary. Missing blobs (new files, no HEAD) come
// back empty, never an error.
func gitFileContents(ctx context.Context, d Deps, container, dir, path string, staged bool) (old, new string, truncated, binary bool) {
	const maxBlob = 100 * 1024
	qd := shellQuote(dir)
	blob := func(cmd string) string {
		out, err := d.Sessions.ExecCommand(ctx, container, cmd+" || true")
		if err != nil || len(out) > 4*1024*1024 {
			return ""
		}
		return out
	}
	// headBlob is HEAD's version ("" when the file is new or HEAD is unborn).
	headBlob := blob("git -C " + qd + " show " + shellQuote("HEAD:"+path))
	// indexBlob is the staged version ("" when untracked).
	indexBlob := blob("git -C " + qd + " show " + shellQuote(":"+path))
	// workBlob is the worktree file ("" when deleted).
	workBlob := blob("cat -- " + shellQuote(dir+"/"+path))
	if staged {
		old, new = headBlob, indexBlob
	} else {
		// Unstaged hunks come from `git diff` (worktree vs index), so the
		// old side is the index blob, matching hunk line counts exactly.
		old, new = indexBlob, workBlob
	}
	if strings.IndexByte(old, 0) >= 0 || strings.IndexByte(new, 0) >= 0 {
		return "", "", false, true
	}
	if len(old) > maxBlob {
		old = old[:maxBlob]
		truncated = true
	}
	if len(new) > maxBlob {
		new = new[:maxBlob]
		truncated = true
	}
	return old, new, truncated, false
}

func handleGitStage(d Deps, unstage bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			return
		}
		var body struct {
			Path string `json:"path"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if !validGitPath(body.Path) {
			writeErr(w, http.StatusBadRequest, "invalid path")
			return
		}
		var cmd string
		if unstage {
			cmd = "git -C " + shellQuote(dir) + " reset HEAD -- " + shellQuote(body.Path)
		} else {
			cmd = "git -C " + shellQuote(dir) + " add -- " + shellQuote(body.Path)
		}
		if _, err := d.Sessions.ExecCommand(r.Context(), container, cmd); err != nil {
			writeInternalErr(w, "git stage", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleGitStageHunk stages (or unstages with reverse=true) a single hunk
// patch for one file. The patch travels as JSON text; the handler base64s
// it into the shell so quotes and newlines cannot break the command.
func handleGitStageHunk(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			return
		}
		var body struct {
			Path    string `json:"path"`
			Patch   string `json:"patch"`
			Reverse bool   `json:"reverse"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if !validGitPath(body.Path) {
			writeErr(w, http.StatusBadRequest, "invalid path")
			return
		}
		if body.Patch == "" || len(body.Patch) > 256*1024 ||
			(!strings.Contains(body.Patch, "diff --git") && !strings.Contains(body.Patch, "@@")) {
			writeErr(w, http.StatusBadRequest, "invalid patch")
			return
		}
		b64 := base64.StdEncoding.EncodeToString([]byte(body.Patch))
		flag := ""
		if body.Reverse {
			flag = " --reverse"
		}
		cmd := "echo '" + b64 + "' | base64 -d | git -C " + shellQuote(dir) +
			" apply --cached" + flag + " -"
		if _, err := d.Sessions.ExecCommand(r.Context(), container, cmd); err != nil {
			writeErr(w, http.StatusBadRequest, "hunk no longer applies — refresh the diff")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
