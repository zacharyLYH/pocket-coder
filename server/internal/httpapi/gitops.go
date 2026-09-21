// Git write/ship endpoints: commit, identity, push, pull, branches,
// switch. All git work runs inside the project container through
// Sessions, scoped to the repo dir (see session.RepoTarget) — the same
// pattern as gitdiff.go, which keeps the read/stage half. AI-assisted
// endpoints (commit-message, pr-body) live in aigen_handlers.go; the
// codemap explainer lives in codemap_explain.go.
package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"pcoder/internal/obs"
)

// gitAuthMsg is the rot signal: the stored token died — update it in Git
// setup. Distinct from conflict output, which keeps the terminal hint.
const gitAuthMsg = "Git credentials rejected — update them in Git setup"

// isGitAuthError matches the one auth shape across push/pull/clone.
func isGitAuthError(s string) bool {
	s = strings.ToLower(s)
	for _, sub := range []string{"could not read username", "authentication failed", "401", "403"} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// conflictsUnder reports whether the repo is in merge-conflict state.
func conflictsUnder(ctx context.Context, d Deps, container, dir string) (bool, error) {
	out, err := d.Sessions.ExecCommand(ctx, container,
		"git -C "+shellQuote(dir)+" diff --name-only --diff-filter=U; true")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// dirtyTrackedUnder lists tracked modifications (unstaged or staged; the
// first porcelain column pair with anything but space/'?'). Untracked
// files do not block branch switches. Uses the same sentinel trick as
// handleGitStatus: ExecCommand trims output, which would eat the leading
// space of a first line like " M notes.txt" and shift the XY columns.
func dirtyTrackedUnder(ctx context.Context, d Deps, container, dir string) ([]string, error) {
	out, err := d.Sessions.ExecCommand(ctx, container,
		"echo DIRTY-BEGIN; git -C "+shellQuote(dir)+" status --porcelain=v1 --untracked-files=no")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "DIRTY-BEGIN") || len(line) < 4 {
			continue
		}
		x, y := line[0], line[1]
		// Tracked modification = anything but untracked-only ('?') and
		// untouched (' '). Rename lines ("R  old -> new") key on X too.
		dirty := (x != ' ' && x != '?') || (y != ' ' && y != '?')
		if dirty {
			files = append(files, strings.TrimSpace(line[3:]))
		}
	}
	return files, nil
}

func handleGitCommit(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.GitCommit, "git commit failed", err, nil)
			}
		}()
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			err = errors.New("repo unavailable")
			return
		}
		var body struct {
			Message string `json:"message"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		msg := strings.TrimSpace(body.Message)
		if msg == "" {
			err = errors.New("message is required")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(msg) > 5000 {
			err = errors.New("message over 5000 chars")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx := r.Context()
		if conflicted, cerr := conflictsUnder(ctx, d, container, dir); cerr == nil && conflicted {
			err = errors.New("merge conflict in progress — resolve in the terminal")
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		b64 := base64.StdEncoding.EncodeToString([]byte(msg))
		qd := shellQuote(dir)
		out, xerr := d.Sessions.ExecCommand(ctx, container,
			"echo '"+b64+"' | base64 -d | git -C "+qd+" commit --no-verify --file - && git -C "+qd+" rev-parse --short HEAD")
		if xerr != nil {
			detail := strings.ToLower(out)
			switch {
			case strings.Contains(detail, "please tell me who you are"),
				strings.Contains(detail, "user.email"),
				strings.Contains(detail, "user.name"):
				err = errors.New("git identity not set — add your name and email to commit")
				writeErr(w, http.StatusConflict, err.Error())
			case strings.Contains(detail, "nothing staged"), strings.Contains(detail, "no changes added"):
				err = errors.New("nothing staged to commit")
				writeErr(w, http.StatusBadRequest, err.Error())
			default:
				err = fmt.Errorf("%s", strings.TrimSpace(out))
				writeErr(w, http.StatusBadGateway, err.Error())
			}
			return
		}
		// The chained command prints the commit summary plus the sha;
		// the sha is the last line (ExecCommand trims outer whitespace).
		sha := strings.TrimSpace(out)
		if i := strings.LastIndex(sha, "\n"); i >= 0 {
			sha = strings.TrimSpace(sha[i+1:])
		}
		if f := strings.Fields(sha); len(f) > 0 {
			sha = f[len(f)-1]
		}
		branch, _ := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" rev-parse --abbrev-ref HEAD")
		obs.Info(ctx, obs.GitCommit, "git commit: "+sha, map[string]any{"sha": sha})
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "commit": sha, "branch": strings.TrimSpace(branch),
		})
	}
}

func handleGitIdentity(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := obs.GitIdentity
		var err error
		defer func() {
			if err != nil {
				obsFail(r, key, "git identity failed", err, nil)
			}
		}()
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			err = errors.New("repo unavailable")
			return
		}
		if r.Method == http.MethodPost {
			var body struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			}
			if !decodeBody(w, r, &body, false) {
				return
			}
			name := strings.TrimSpace(body.Name)
			email := strings.TrimSpace(body.Email)
			if name == "" || email == "" || len(name) > 200 || len(email) > 200 ||
				strings.HasPrefix(name, "-") || strings.HasPrefix(email, "-") {
				err = errors.New("name and email must be non-empty, ≤ 200 chars, and not start with -")
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			qd := shellQuote(dir)
			if _, xerr := d.Sessions.ExecCommand(r.Context(), container,
				"git -C "+qd+" config user.name "+shellQuote(name)+
					" && git -C "+qd+" config user.email "+shellQuote(email)); xerr != nil {
				err = xerr
				writeInternalErr(w, "git config", xerr)
				return
			}
			obs.Info(r.Context(), key, "git identity set", map[string]any{"stage": "set"})
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name, "email": email})
			return
		}
		qd := shellQuote(dir)
		name, _ := d.Sessions.ExecCommand(r.Context(), container,
			"git -C "+qd+" config user.name; true")
		email, _ := d.Sessions.ExecCommand(r.Context(), container,
			"git -C "+qd+" config user.email; true")
		writeJSON(w, http.StatusOK, map[string]any{
			"name": strings.TrimSpace(name), "email": strings.TrimSpace(email),
		})
	}
}

func handleGitPush(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.GitPush, "git push failed", err, nil)
			}
		}()
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			err = errors.New("repo unavailable")
			return
		}
		ctx := r.Context()
		qd := shellQuote(dir)
		branch, _ := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" rev-parse --abbrev-ref HEAD")
		branch = strings.TrimSpace(branch)
		if branch == "" || branch == "HEAD" {
			err = errors.New("detached HEAD — switch to a branch before pushing")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, xerr := d.Sessions.ExecCommand(ctx, container,
			"GIT_TERMINAL_PROMPT=0 git -C "+qd+" push -u origin "+shellQuote(branch))
		detail := strings.TrimSpace(out)
		if xerr != nil {
			if isGitAuthError(detail) {
				err = errors.New(gitAuthMsg)
			} else {
				err = errors.New(detail + " — fix credentials or conflicts in the terminal")
			}
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		obs.Info(ctx, obs.GitPush, "git push: "+branch, map[string]any{"branch": branch})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "branch": branch, "output": tailLines(detail, 50)})
	}
}

func handleGitPull(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.GitPull, "git pull failed", err, nil)
			}
		}()
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			err = errors.New("repo unavailable")
			return
		}
		out, xerr := d.Sessions.ExecCommand(r.Context(), container,
			"GIT_TERMINAL_PROMPT=0 git -C "+shellQuote(dir)+" pull --ff-only")
		detail := strings.TrimSpace(out)
		if xerr != nil {
			if isGitAuthError(detail) {
				err = errors.New(gitAuthMsg)
			} else {
				err = errors.New(detail + " — fix in the terminal")
			}
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		obs.Info(r.Context(), obs.GitPull, "git pull ok", nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": tailLines(detail, 50)})
	}
}

var prShaRe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// handleGitBranches serves GET /git/branches (the listing half; the
// switch action is handleGitSwitch on POST /git/switch).
func handleGitBranches(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.GitBranches, "git branches failed", err, nil)
			}
		}()
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			err = errors.New("repo unavailable")
			return
		}
		ctx := r.Context()
		qd := shellQuote(dir)
		type branch struct {
			Name         string `json:"name"`
			RelativeTime string `json:"relativeTime"`
		}
		parse := func(out string) []branch {
			list := []branch{}
			for _, line := range strings.Split(out, "\n") {
				parts := strings.SplitN(line, "\x00", 2)
				if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
					continue
				}
				list = append(list, branch{Name: parts[0], RelativeTime: parts[1]})
			}
			return list
		}
		localOut, xerr := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" branch --sort=-committerdate --format=%(refname:short)%00%(committerdate:relative)")
		if xerr != nil {
			err = xerr
			writeInternalErr(w, "git branch", xerr)
			return
		}
		remoteOut, _ := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" branch --remotes --sort=-committerdate --format=%(refname:short)%00%(committerdate:relative)")
		current, _ := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" branch --show-current")
		current = strings.TrimSpace(current)
		detached := current == ""
		if detached {
			current, _ = d.Sessions.ExecCommand(ctx, container,
				"git -C "+qd+" rev-parse --short HEAD")
			current = strings.TrimSpace(current)
		}
		local := parse(localOut)
		remote := []branch{}
		for _, b := range parse(remoteOut) {
			b.Name = strings.TrimPrefix(b.Name, "origin/")
			if b.Name == "HEAD" || b.Name == "" {
				continue
			}
			remote = append(remote, b)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"current": current, "detached": detached,
			"local": local, "remote": remote,
		})
	}
}

func handleGitSwitch(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.GitSwitch, "git switch failed", err, nil)
			}
		}()
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			err = errors.New("repo unavailable")
			return
		}
		var body struct {
			Branch string `json:"branch"`
			Create bool   `json:"create"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		name := strings.TrimSpace(body.Branch)
		ctx := r.Context()
		qd := shellQuote(dir)
		if name == "" {
			err = errors.New("branch is required")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if out, xerr := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" check-ref-format --branch "+shellQuote(name)); xerr != nil {
			err = fmt.Errorf("invalid branch name: %s", strings.TrimSpace(out))
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		flag := ""
		if body.Create {
			if out, xerr := d.Sessions.ExecCommand(ctx, container,
				"git -C "+qd+" show-ref --verify --quiet refs/heads/"+shellQuote(name)+"; echo $?"); xerr == nil && strings.TrimSpace(out) == "0" {
				err = errors.New("branch already exists")
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			flag = "-c "
		}
		dirty, derr := dirtyTrackedUnder(ctx, d, container, dir)
		if derr == nil && len(dirty) > 0 {
			err = fmt.Errorf("working tree has uncommitted changes — commit or stage first: %s",
				strings.Join(dirty, ", "))
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		if conflicted, cerr := conflictsUnder(ctx, d, container, dir); cerr == nil && conflicted {
			err = errors.New("merge conflict in progress — resolve in the terminal")
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		out, xerr := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" switch "+flag+shellQuote(name))
		if xerr != nil {
			err = errors.New(strings.TrimSpace(out))
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		obs.Info(ctx, obs.GitSwitch, "git switch: "+name, map[string]any{"branch": name, "create": body.Create})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "branch": name})
	}
}

// commitExists reports whether sha resolves to a commit. The probe is
// --quiet so ExecCommand's trimmed output is just the exit code ("0").
func commitExists(ctx context.Context, d Deps, container, dir, sha string) bool {
	out, _ := d.Sessions.ExecCommand(ctx, container,
		"git -C "+shellQuote(dir)+" rev-parse --verify --quiet "+shellQuote(sha+"^{commit}")+" >/dev/null 2>&1; echo $?")
	return strings.TrimSpace(out) == "0"
}

// tailLines keeps the last n lines of s (shared with AI output caps).
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
