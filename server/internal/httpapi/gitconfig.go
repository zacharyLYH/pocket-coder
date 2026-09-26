// Git identity helpers: validation plus the live token check against the
// GitHub API. List CRUD lives in lists.go; every save tests first.
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
		d.State.View(func(doc *state.Document) {
			if g := state.FirstGit(doc); g != nil {
				ok = g.Name != "" && g.Email != "" && g.Token != ""
			}
		})
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
