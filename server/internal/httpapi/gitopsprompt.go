// Git ops-prompt endpoint: tailored AI instructions for the higher-level
// git ops this tab deliberately does not perform (PR flow, sync from base,
// discard/rollback). Deterministic template filled with live repo context —
// no model call, so it works with no AI key configured and stays stable.
// The user pastes the prompt into their terminal AI, which does the work.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"pcoder/internal/obs"
)

// opsPromptOps is the v1 op set: ship via PR, update from base, discard.
const (
	opsPromptPR   = "pr"
	opsPromptSync = "sync"
	opsPromptUndo = "undo"
)

// defaultBase resolves origin's default branch (origin/HEAD), falling back
// to "main" when the remote advertises none.
func defaultBase(ctx context.Context, d Deps, container, dir string) string {
	out, _ := d.Sessions.ExecCommand(ctx, container,
		"git -C "+shellQuote(dir)+" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null")
	out = strings.TrimSpace(out)
	if name := strings.TrimPrefix(out, "refs/remotes/origin/"); name != "" && name != out {
		return name
	}
	return "main"
}

// opsContext is the live repo state baked into the prompt and echoed back
// so the client can show what the prompt was tailored from.
type opsContext struct {
	branch     string
	detached   bool
	upstream   string
	ahead      int
	behind     int
	dirty      []string
	conflicted bool
	unborn     bool
	base       string
}

// opsState gathers branch, upstream counts, dirty files, conflict and
// unborn state. Soft failures degrade to zero values — the prompt still
// builds, with a caveat line instead of hard numbers.
func opsState(ctx context.Context, d Deps, container, dir string) opsContext {
	m := getGitMeta(ctx, d, container, dir)
	c := opsContext{
		branch: m.branch, detached: m.detached,
		upstream: m.upstream, ahead: m.ahead, behind: m.behind,
		unborn: m.unborn,
	}
	if dirty, derr := dirtyTrackedUnder(ctx, d, container, dir); derr == nil {
		c.dirty = dirty
	}
	if conflicted, cerr := conflictsUnder(ctx, d, container, dir); cerr == nil {
		c.conflicted = conflicted
	}
	return c
}

// validOpsBranch mirrors the switch endpoint: git's own format check plus
// length and leading-dash guards shared with identity/project inputs.
func validOpsBranch(ctx context.Context, d Deps, container, dir, name string) error {
	if name == "" || len(name) > 250 || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid branch name: %q", name)
	}
	if out, xerr := d.Sessions.ExecCommand(ctx, container,
		"git -C "+shellQuote(dir)+" check-ref-format --branch "+shellQuote(name)); xerr != nil {
		return fmt.Errorf("invalid branch name: %s", strings.TrimSpace(out))
	}
	return nil
}

func treeLine(c opsContext) string {
	if c.conflicted {
		return "A merge conflict is in progress — resolve it before anything else."
	}
	if len(c.dirty) == 0 {
		return "The working tree is clean."
	}
	names := strings.Join(c.dirty, ", ")
	if len(names) > 300 {
		names = names[:300] + "…"
	}
	return fmt.Sprintf("Uncommitted changes in %d file(s): %s.", len(c.dirty), names)
}

func upstreamLine(c opsContext) string {
	if c.upstream == "" {
		return "no upstream tracking yet"
	}
	return fmt.Sprintf("upstream %s (ahead %d, behind %d)", c.upstream, c.ahead, c.behind)
}

// promptPR moves work onto a new branch and opens a PR. newBranch and base
// are already validated; c carries the tailored state.
func promptPR(c opsContext, newBranch, repoDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are helping ship work in %s via a pull request.\n", repoDir)
	fmt.Fprintf(&b, "Current branch: %s (%s). %s\n", c.branch, upstreamLine(c), treeLine(c))
	if c.unborn {
		b.WriteString("The repo has no commits yet — the first push creates history.\n")
	}
	fmt.Fprintf(&b, "Goal: move this work onto a new branch '%s' based on '%s' and open a PR against '%s'.\n", newBranch, c.base, c.base)
	b.WriteString("\n1. Confirm with me first: branch '" + newBranch + "' from '" + c.base + "' — ask if I want a different name or base before running anything.\n")
	b.WriteString("2. Run: git fetch origin, then git status --short --branch. If the tree is dirty, ask me whether to carry the changes across (switch first, changes follow) or commit them here first — do not decide for me.\n")
	fmt.Fprintf(&b, "3. Create and switch: git switch -c %s. If it already exists, stop and ask me.\n", newBranch)
	fmt.Fprintf(&b, "4. Push: git push -u origin %s. Never push directly to '%s'. If push is rejected (protected branch / GH006), stop and report the exact error.\n", newBranch, c.base)
	b.WriteString("5. Open the PR: if `gh` is installed, run `gh pr create --base " + c.base + " --fill` and paste the URL. Otherwise give me the GitHub compare URL for the branch so I can open it in a browser.\n")
	b.WriteString("6. Finish with the PR URL plus a one-line summary of what shipped. Never force-push to '" + c.base + "'.")
	return b.String()
}

// promptSync updates the current branch from base, asking rebase-vs-merge.
func promptSync(c opsContext, repoDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are helping update a branch from its base in %s.\n", repoDir)
	fmt.Fprintf(&b, "Current branch: %s (%s). Base: '%s'. %s\n", c.branch, upstreamLine(c), c.base, treeLine(c))
	if c.detached {
		b.WriteString("HEAD is detached — switch to a branch before syncing.\n")
	}
	b.WriteString("\n1. Ask me: rebase onto '" + c.base + "' or merge '" + c.base + "' in? Recommend rebase when my branch is unshared feature work, merge when it is shared or already pushed for review. Do not proceed until I choose.\n")
	b.WriteString("2. Run: git fetch origin. If the tree is dirty, ask me whether to stash or commit first — do not decide for me.\n")
	fmt.Fprintf(&b, "3. Then either git rebase origin/%s or git merge origin/%s, exactly as I chose.\n", c.base, c.base)
	b.WriteString("4. On conflict: stop immediately, show git status --short and the conflicted files, and ask me how to resolve each one — never auto-resolve by taking one side.\n")
	fmt.Fprintf(&b, "5. Push only '%s' and only with my say-so (git push --force-with-lease if rebased). Never force-push '%s'.", c.branch, c.base)
	return b.String()
}

// promptUndo discards local work back to the upstream state. Confirmation
// is part of the prompt: the AI must show the blast radius and wait.
func promptUndo(c opsContext, repoDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are helping discard local work in %s. This destroys data — go slowly.\n", repoDir)
	fmt.Fprintf(&b, "Current branch: %s (%s). %s\n", c.branch, upstreamLine(c), treeLine(c))
	target := c.upstream
	if target == "" {
		target = "origin/" + c.branch
	}
	b.WriteString("\n1. First show me exactly what will be lost, before touching anything: git status --short, git diff --stat, and git log " + target + "..HEAD --oneline. List every file and commit that will disappear.\n")
	fmt.Fprintf(&b, "2. Ask me to confirm by typing the branch name '%s'. Do nothing until I confirm with that exact name.\n", c.branch)
	fmt.Fprintf(&b, "3. Only then: git reset --hard %s, then git clean -fd limited to the untracked paths just shown, then git status to prove the tree is clean.\n", target)
	b.WriteString("4. Never touch any other branch. Never run git push --force anywhere as part of this.")
	return b.String()
}

func handleGitOpsPrompt(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer obsFailAt(r, obs.GitOpsPrompt, "git ops-prompt failed", &err, nil)()
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			err = errors.New("repo unavailable")
			return
		}
		var body struct {
			Op        string `json:"op"`
			NewBranch string `json:"newBranch"`
			Base      string `json:"base"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		op := strings.TrimSpace(body.Op)
		if op != opsPromptPR && op != opsPromptSync && op != opsPromptUndo {
			err = errors.New(`op must be one of "pr", "sync", "undo"`)
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx := r.Context()
		c := opsState(ctx, d, container, dir)
		base := strings.TrimSpace(body.Base)
		if base == "" {
			base = defaultBase(ctx, d, container, dir)
		}
		if err = validOpsBranch(ctx, d, container, dir, base); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		c.base = base
		var prompt string
		switch op {
		case opsPromptPR:
			newBranch := strings.TrimSpace(body.NewBranch)
			if newBranch == "" {
				err = errors.New("newBranch is required for the pr flow")
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			if err = validOpsBranch(ctx, d, container, dir, newBranch); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			prompt = promptPR(c, newBranch, dir)
		case opsPromptSync:
			prompt = promptSync(c, dir)
		case opsPromptUndo:
			prompt = promptUndo(c, dir)
		}
		obs.Info(ctx, obs.GitOpsPrompt, "git ops prompt: "+op, map[string]any{"op": op, "branch": c.branch})
		writeJSON(w, http.StatusOK, map[string]any{
			"prompt": prompt,
			"context": map[string]any{
				"branch": c.branch, "upstream": c.upstream,
				"ahead": c.ahead, "behind": c.behind,
				"dirty": len(c.dirty), "conflicted": c.conflicted,
				"base": c.base,
			},
		})
	}
}
