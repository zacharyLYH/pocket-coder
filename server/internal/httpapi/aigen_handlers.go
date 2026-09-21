// AI-assisted git endpoints: commit message + PR description drafting.
// One-shot structured completions via agent.CompleteJSON; the diff is
// built by cappedDiff (same exec-through-Sessions pattern as the rest of
// the git endpoints).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"pcoder/internal/agent"
	"pcoder/internal/obs"
)

const maxDiffBytes = 50 * 1024

// aiGitSetup is the shared prologue for the AI-assisted git endpoints: AI
// config, then repo resolution. On failure it writes the response and
// returns the cause for the caller's obsFail line.
func aiGitSetup(d Deps, w http.ResponseWriter, r *http.Request) (agent.Config, string, string, error) {
	cfg := aiConfig(d, aiBody{})
	if !cfg.Valid() {
		writeErr(w, http.StatusConflict, "ai not configured")
		return cfg, "", "", errors.New("ai not configured")
	}
	container, dir, ok := gitRepoDir(d, w, r)
	if !ok {
		return cfg, "", "", errors.New("repo unavailable")
	}
	return cfg, container, dir, nil
}

// truncateDiff caps one git-produced diff at maxDiffBytes. cappedDiff adds
// a numstat summary instead; this plain cut is for show/commit outputs.
func truncateDiff(diff string) (string, bool) {
	if len(diff) > maxDiffBytes {
		return diff[:maxDiffBytes] + "\n…[diff truncated]", true
	}
	return diff, false
}

// cappedDiff builds the diff text the AI endpoints see: staged diff when
// the index is non-empty, else unstaged diff + untracked file contents.
// Returns (diff, truncated); truncated diffs carry a numstat summary line
// so the model still sees the full file list. Capped at 50 KB.
func cappedDiff(ctx context.Context, d Deps, container, dir string, includeUntracked bool) (string, bool) {
	qd := shellQuote(dir)
	stagedOut, _ := d.Sessions.ExecCommand(ctx, container,
		"git -C "+qd+" diff --cached -U3; true")
	unstagedOut, _ := d.Sessions.ExecCommand(ctx, container,
		"git -C "+qd+" diff -U3; true")
	var sb strings.Builder
	hasStaged := strings.TrimSpace(stagedOut) != ""
	if hasStaged {
		sb.WriteString(stagedOut)
	}
	if !hasStaged {
		sb.WriteString(unstagedOut)
	}
	if includeUntracked && !hasStaged {
		out, err := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" ls-files --others --exclude-standard")
		if err == nil {
			for _, path := range strings.Split(out, "\n") {
				path = strings.TrimSpace(path)
				if path == "" || !validGitPath(path) {
					continue
				}
				content, cerr := d.Sessions.ExecCommand(ctx, container,
					"cat -- "+shellQuote(dir+"/"+path)+"; true")
				if cerr != nil || content == "" {
					continue
				}
				sb.WriteString("\n--- NEW FILE: " + path + " ---\n" + content + "\n")
			}
		}
	}
	diff := sb.String()
	truncated := false
	if len(diff) > maxDiffBytes {
		numstat, _ := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" diff --numstat; git -C "+qd+" diff --cached --numstat; true")
		diff = diff[:maxDiffBytes] + "\n…[diff truncated; full file stats follow]\n" + numstat
		truncated = true
	}
	return diff, truncated
}

const commitMsgSystem = `You write git commit messages from diffs. Respond ONLY with JSON matching the schema: {"subject": string, "body": string}. The subject is one conventional-commit line (type(scope): change), imperative mood, at most 72 characters, no trailing period. The body is at most 15 short lines explaining what changed and why; empty string when the change is trivial. Never invent files or changes not visible in the diff.`

const commitMsgSchemaName = "commit_message"

var commitMsgSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"subject": map[string]any{"type": "string", "maxLength": 72},
		"body":    map[string]any{"type": "string"},
	},
	"required":             []string{"subject", "body"},
	"additionalProperties": false,
}

func handleGitCommitMessage(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.GitCommitMsg, "git commit-message failed", err, nil)
			}
		}()
		cfg, container, dir, serr := aiGitSetup(d, w, r)
		if serr != nil {
			err = serr
			return
		}
		diff, truncated := cappedDiff(r.Context(), d, container, dir, true)
		if strings.TrimSpace(diff) == "" {
			err = errors.New("no changes to describe")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		user := "Write a commit message for this diff:\n\n" + diff
		if truncated {
			user += "\n\n(the diff was truncated — summarize conservatively)"
		}
		raw, aerr := agent.CompleteJSON(r.Context(), cfg, commitMsgSystem, user, commitMsgSchemaName, commitMsgSchema)
		if aerr != nil {
			err = aerr
			writeErr(w, http.StatusBadGateway, "generation failed: "+aerr.Error())
			return
		}
		var out struct {
			Subject string `json:"subject"`
			Body    string `json:"body"`
		}
		if merr := json.Unmarshal(raw, &out); merr != nil || strings.TrimSpace(out.Subject) == "" {
			err = fmt.Errorf("unusable generation: %s", string(raw))
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		obs.Info(r.Context(), obs.GitCommitMsg, "commit message generated", map[string]any{"subject": out.Subject})
		writeJSON(w, http.StatusOK, out)
	}
}

const prBodySystem = `You draft pull request descriptions from a commit's diff and message. Respond ONLY with JSON matching the schema: {"title": string, "body": string}. The title is a one-line summary (max 100 characters). The body is a markdown PR description with three short sections: "## What", "## Why", "## How" — at most 60 lines total. Base every statement on the diff; never invent context.`

const prBodySchemaName = "pr_description"

var prBodySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"title": map[string]any{"type": "string", "maxLength": 100},
		"body":  map[string]any{"type": "string"},
	},
	"required":             []string{"title", "body"},
	"additionalProperties": false,
}

func handleGitPRBody(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.GitPRBody, "git pr-body failed", err, nil)
			}
		}()
		cfg, container, dir, serr := aiGitSetup(d, w, r)
		if serr != nil {
			err = serr
			return
		}
		var body struct {
			Sha string `json:"sha"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		sha := strings.TrimSpace(body.Sha)
		if !prShaRe.MatchString(sha) {
			err = errors.New("invalid sha")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx := r.Context()
		qd := shellQuote(dir)
		if !commitExists(ctx, d, container, dir, sha) {
			err = errors.New("unknown commit")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		msg, _ := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" log -1 --format=%B "+shellQuote(sha))
		diff, _ := d.Sessions.ExecCommand(ctx, container,
			"git -C "+qd+" show "+shellQuote(sha)+" --format= -U3; true")
		diff, truncated := truncateDiff(diff)
		if strings.TrimSpace(diff) == "" {
			err = errors.New("commit has no diff")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		user := "Commit message:\n\n" + msg + "\n\nCommit diff:\n\n" + diff
		if truncated {
			user += "\n\n(the diff was truncated — summarize conservatively)"
		}
		raw, aerr := agent.CompleteJSON(ctx, cfg, prBodySystem, user, prBodySchemaName, prBodySchema)
		if aerr != nil {
			err = aerr
			writeErr(w, http.StatusBadGateway, "generation failed: "+aerr.Error())
			return
		}
		var out struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if merr := json.Unmarshal(raw, &out); merr != nil || strings.TrimSpace(out.Title) == "" {
			err = fmt.Errorf("unusable generation: %s", string(raw))
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		obs.Info(r.Context(), obs.GitPRBody, "pr description generated", map[string]any{"sha": sha})
		writeJSON(w, http.StatusOK, out)
	}
}
