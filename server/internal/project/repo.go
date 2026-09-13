package project

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// segmentRe is the charset allowed in an owner or repo name.
var segmentRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// scpRe matches scp-style git URLs: [user@]host:owner/repo(.git).
var scpRe = regexp.MustCompile(`^[^@/:]+@[^:]+:(.+)$`)

// ParseRepoID derives the canonical project id ("owner/repo", lowercased)
// from a repository URL. Only GitHub URLs are accepted: https-style
// (https://github.com/<owner>/<repo>[.git]) and scp-style
// (git@github.com:<owner>/<repo>[.git]). The id asserts uniqueness —
// creating the same repo twice is a conflict.
func ParseRepoID(repoURL string) (string, error) {
	return parseRepoID(repoURL, false)
}

// parseRepoID is ParseRepoID with the test hatch: when allowAny is set,
// any host's last two path segments become the id, so live-engine stacks
// can clone from a local git daemon instead of github.com.
func parseRepoID(repoURL string, allowAny bool) (string, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return "", fmt.Errorf("%w: repoUrl is required — clone a GitHub repository URL", ErrInvalidInput)
	}
	if strings.HasPrefix(repoURL, "-") {
		return "", fmt.Errorf("%w: repo url and branch must not start with \"-\"", ErrInvalidInput)
	}
	owner, repo, isGitHub, err := splitRepoPath(repoURL, allowAny)
	if err != nil {
		return "", err
	}
	if !isGitHub && !allowAny {
		return "", fmt.Errorf("%w: repoUrl must be a GitHub repository URL (https://github.com/<owner>/<repo> or git@github.com:<owner>/<repo>.git)", ErrInvalidInput)
	}
	id := strings.ToLower(owner + "/" + repo)
	return id, nil
}

// splitRepoPath extracts owner, repo, and whether the URL targets
// github.com. In hatch mode any host's last two path segments do.
func splitRepoPath(repoURL string, allowAny bool) (owner, repo string, isGitHub bool, err error) {
	invalid := func() (string, string, bool, error) {
		return "", "", false, fmt.Errorf("%w: repoUrl must look like https://github.com/<owner>/<repo> or git@github.com:<owner>/<repo>.git", ErrInvalidInput)
	}
	var path string
	if m := scpRe.FindStringSubmatch(repoURL); m != nil {
		host := repoURL[:strings.Index(repoURL, ":")]
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
		isGitHub = strings.EqualFold(host, "github.com")
		path = m[1]
	} else {
		withScheme := repoURL
		if !strings.Contains(withScheme, "://") {
			withScheme = "https://" + withScheme
		}
		u, perr := url.Parse(withScheme)
		if perr != nil || u.Host == "" {
			return invalid()
		}
		isGitHub = strings.EqualFold(u.Host, "github.com")
		path = u.Path
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	segs := strings.Split(path, "/")
	// Tolerate a single-segment path in hatch mode only (the local git
	// daemon serves /repo); production always needs owner + repo.
	if len(segs) == 1 && allowAny {
		segs = []string{"local", segs[0]}
	}
	if len(segs) != 2 || segs[0] == "" || segs[1] == "" {
		return invalid()
	}
	if !segmentRe.MatchString(segs[0]) || !segmentRe.MatchString(segs[1]) {
		return invalid()
	}
	return segs[0], segs[1], isGitHub, nil
}

// SanitizeName maps a project id to the docker-safe slug used in container
// and volume names: lowercase, "/" becomes "-", anything outside
// [a-z0-9_.-] becomes "-".
func SanitizeName(id string) string {
	s := strings.ToLower(id)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '/':
			b.WriteByte('-')
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		out = "project"
	}
	return out
}
