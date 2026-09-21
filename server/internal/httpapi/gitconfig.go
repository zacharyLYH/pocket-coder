// Git setup endpoints: the single global git identity + HTTPS token
// (GitHub PAT, repo scope). Test-then-save, mirroring the AI config
// pattern. The token never leaves the server: GET omits it.
package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"pcoder/internal/state"
)

type gitBody struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Token string `json:"token"`
}

// gitHubUserURL is the live token check; var so tests can fake it.
var gitHubUserURL = "https://api.github.com/user"

var gitHTTP = &http.Client{Timeout: 10 * time.Second}

// gitConfigured reports whether a complete git identity is stored.
func gitConfigured(d Deps) bool {
	var ok bool
	if d.State != nil {
		d.State.View(func(doc *state.Document) { ok = doc.Git.Valid() })
	}
	return ok
}

func validGitBody(b gitBody) bool {
	for _, s := range []string{b.Name, b.Email, b.Token} {
		if s == "" || len(s) > 200 || strings.HasPrefix(s, "-") {
			return false
		}
	}
	return true
}

// decodeGitBody decodes, trims, and validates; on failure it writes the
// 400 and returns false. Shared by test-then-save so the two cannot drift.
func decodeGitBody(w http.ResponseWriter, r *http.Request) (gitBody, bool) {
	var body gitBody
	if !decodeBody(w, r, &body, false) {
		return body, false
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Email = strings.TrimSpace(body.Email)
	body.Token = strings.TrimSpace(body.Token)
	if !validGitBody(body) {
		writeErr(w, http.StatusBadRequest, "name, email, and PAT are all required")
		return body, false
	}
	return body, true
}

// testGitToken live-checks the token against the GitHub API: 200 = good,
// 401/403 = bad. No repo needed, so it works before any project exists.
func testGitToken(r *http.Request, token string) error {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, gitHubUserURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := gitHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("GitHub rejected the token")
	default:
		return fmt.Errorf("GitHub check failed: HTTP %d", res.StatusCode)
	}
}

func handleGetGitConfig(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var g state.GitConfig
		if d.State != nil {
			d.State.View(func(doc *state.Document) {
				if doc.Git != nil {
					g = *doc.Git
				}
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name": g.Name, "email": g.Email,
			"hasToken": g.Token != "", "configured": (&g).Valid(),
		})
	}
}

func handleTestGit(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeGitBody(w, r)
		if !ok {
			return
		}
		if err := testGitToken(r, body.Token); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleSaveGitConfig(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeGitBody(w, r)
		if !ok {
			return
		}
		if err := testGitToken(r, body.Token); err != nil {
			writeErr(w, http.StatusBadGateway, "test call failed: "+err.Error())
			return
		}
		if err := d.State.Mutate(func(doc *state.Document) error {
			doc.Git = &state.GitConfig{Name: body.Name, Email: body.Email, Token: body.Token}
			return nil
		}); err != nil {
			writeInternalErr(w, "save git config", err)
			return
		}
		_, _ = d.Events.Append("git.configured", map[string]any{"name": body.Name, "email": body.Email})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
