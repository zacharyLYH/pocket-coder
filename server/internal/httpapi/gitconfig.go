// Git identity helpers: validation for the name/email-only form.
// Commit authorship needs no token: git-over-SSH uses the server deploy
// key, and every save is local — identity lives per repo (set through
// handleGitIdentity at commit time), never in state.json.
package httpapi

import (
	"net/http"
	"strings"
)

type gitBody struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func validGitBody(b gitBody) bool {
	for _, s := range []string{b.Name, b.Email} {
		if s == "" || len(s) > 200 || strings.HasPrefix(s, "-") {
			return false
		}
	}
	return true
}

// decodeGitBody decodes, trims, and validates; on failure it writes the
// 400 and returns false.
func decodeGitBody(w http.ResponseWriter, r *http.Request) (gitBody, bool) {
	var body gitBody
	if !decodeBody(w, r, &body, false) {
		return body, false
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Email = strings.TrimSpace(body.Email)
	if !validGitBody(body) {
		writeErr(w, http.StatusBadRequest, "name and email are both required")
		return body, false
	}
	return body, true
}
