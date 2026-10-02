package project

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// segmentRe is the charset allowed in an owner or repo name.
var segmentRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// badRepoURL is the one invalid-URL error, shared by every check below.
func badRepoURL() error {
	return fmt.Errorf("%w: repoUrl must look like https://github.com/<owner>/<repo> or git@github.com:<owner>/<repo>.git", ErrInvalidInput)
}

// scpRe matches scp-style git URLs: [user@]host:owner/repo(.git).
var scpRe = regexp.MustCompile(`^[^@/:]+@[^:]+:(.+)$`)

// parseRepoID derives the canonical project id ("owner/repo", lowercased)
// from a repository URL. Only GitHub URLs are accepted: https-style
// (https://github.com/<owner>/<repo>[.git]) and scp-style
// (git@github.com:<owner>/<repo>[.git]). The id asserts uniqueness —
// creating the same repo twice is a conflict. When allowAny is set (test
// stacks), any host's last two path segments become the id, so live-engine
// stacks can clone from a local git daemon instead of github.com.
func parseRepoID(repoURL string, allowAny bool) (string, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return "", fmt.Errorf("%w: repoUrl is required — clone a GitHub repository URL", ErrInvalidInput)
	}
	if strings.HasPrefix(repoURL, "-") {
		return "", fmt.Errorf("%w: repo url and branch must not start with \"-\"", ErrInvalidInput)
	}
	r, err := splitURL(repoURL, allowAny)
	if err != nil {
		return "", err
	}
	if !r.isGitHub && !allowAny {
		return "", fmt.Errorf("%w: repoUrl must be a GitHub repository URL (https://github.com/<owner>/<repo> or git@github.com:<owner>/<repo>.git)", ErrInvalidInput)
	}
	return strings.ToLower(r.owner + "/" + r.repo), nil
}

// parsedRepo is one parsed repository URL: owner/repo plus where and
// how it was addressed. splitURL parses once so id derivation and SSH
// normalization share the validation.
type parsedRepo struct {
	owner, repo string
	host        string // lowercased hostname, userinfo stripped
	scheme      string // "scp", "https", "ssh", "git", or "" (bare host/path)
	hasPort     bool
	isGitHub    bool
}

// splitURL parses https, ssh://, git://, bare, and scp-style URLs into
// owner/repo + host. Single-segment paths are tolerated in hatch mode
// only (the local git daemon serves /repo).
func splitURL(raw string, allowAny bool) (parsedRepo, error) {
	invalid := func() (parsedRepo, error) {
		return parsedRepo{}, badRepoURL()
	}
	var r parsedRepo
	var path string
	if m := scpRe.FindStringSubmatch(raw); m != nil {
		host := raw[:strings.Index(raw, ":")]
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
		r.scheme = "scp"
		r.host = strings.ToLower(host)
		path = m[1]
	} else {
		withScheme := raw
		if !strings.Contains(withScheme, "://") {
			withScheme = "https://" + withScheme
		}
		u, err := url.Parse(withScheme)
		if err != nil || u.Host == "" {
			return invalid()
		}
		r.scheme = strings.ToLower(u.Scheme)
		r.host = strings.ToLower(u.Hostname())
		r.hasPort = u.Port() != ""
		path = u.Path
	}
	r.isGitHub = r.host == "github.com"
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	segs := strings.Split(path, "/")
	if len(segs) == 1 && allowAny {
		segs = []string{"local", segs[0]}
	}
	if len(segs) != 2 || segs[0] == "" || segs[1] == "" {
		return invalid()
	}
	// Dot segments pass the charset regex but escape the project dir
	// via filepath.Join downstream: "github.com/../.." must not
	// become the id "../..".
	for _, seg := range segs {
		if seg == "." || seg == ".." {
			return invalid()
		}
	}
	if !segmentRe.MatchString(segs[0]) || !segmentRe.MatchString(segs[1]) {
		return invalid()
	}
	r.owner, r.repo = segs[0], segs[1]
	return r, nil
}

// toSSHURL normalizes a repository URL to scp-style SSH form so the
// server deploy key applies.
//  1. Already SSH (or a form with no SSH equivalent)? Return as-is.
//  2. Otherwise rewrite into SSH form.
func toSSHURL(repoURL string, allowAny bool) (string, error) {
	trimmed := strings.TrimSpace(repoURL)
	// splitURL validates every form, including scp — a malformed SSH
	// URL fails here instead of sailing through on a regex match.
	r, err := splitURL(trimmed, allowAny)
	if err != nil {
		return "", err
	}
	// 1. Already SSH (or a form with no SSH equivalent)? Return as-is.
	// git-daemon URLs only pass in hatch mode; custom ports fit no scp
	// shape, and only ssh:// may carry one — an https URL with a port
	// is rejected rather than silently rewritten without it.
	if r.scheme == "scp" {
		return trimmed, nil
	}
	if r.scheme == "git" && allowAny {
		return trimmed, nil
	}
	if r.scheme == "ssh" && r.hasPort {
		return trimmed, nil
	}
	if r.hasPort {
		return "", badRepoURL()
	}
	// 2. Rewrite https:// and ssh:// into scp form.
	if !r.isGitHub && !allowAny {
		return "", fmt.Errorf("%w: repoUrl must be a GitHub repository URL (https://github.com/<owner>/<repo> or git@github.com:<owner>/<repo>.git)", ErrInvalidInput)
	}
	if r.scheme != "https" && r.scheme != "ssh" {
		return "", badRepoURL()
	}
	return fmt.Sprintf("git@%s:%s/%s.git", r.host, r.owner, r.repo), nil
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
