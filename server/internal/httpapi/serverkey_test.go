package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
)

// The probe verdict rides on GitHub's output, not the exit code (1 on
// success): pin the parser without network.
func TestParseProbeResult(t *testing.T) {
	user, ok := parseProbeResult("Hi octocat! You've successfully authenticated, but GitHub does not provide shell access.\n")
	if !ok || user != "octocat" {
		t.Fatalf("success = %q, %v", user, ok)
	}
	if _, ok := parseProbeResult("git@github.com: Permission denied (publickey).\n"); ok {
		t.Fatal("denied parsed as success")
	}
	if _, ok := parseProbeResult(""); ok {
		t.Fatal("empty parsed as success")
	}
}

// The probe endpoint is the home gate: a good probe answers 200 with the
// authed user, a rejected key 502 with the fix, a missing keypair 409.
// The probe itself is stubbed — no network in unit tests.
func TestServerKeyTestHandler(t *testing.T) {
	d, _, pinOut, st := newProjectDeps(t)
	d.SSHKeys = sshkeys.New(st)
	if err := st.Mutate(func(doc *state.Document) error {
		doc.ServerKey = &state.ServerSSHKey{PublicKey: "ssh-ed25519 AAAA", Fingerprint: "SHA256:test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	orig := probeGitHubSSH
	t.Cleanup(func() { probeGitHubSSH = orig })

	probeGitHubSSH = func(context.Context, sshkeys.Key) (string, error) { return "octocat", nil }
	rec := authedPost(t, h, cookie, "/api/ssh/test", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"user":"octocat"`) {
		t.Fatalf("probe ok: %d %q, want 200 with user", rec.Code, rec.Body)
	}

	probeGitHubSSH = func(context.Context, sshkeys.Key) (string, error) {
		return "", errors.New("GitHub rejected the key — add the public half to GitHub first")
	}
	rec = authedPost(t, h, cookie, "/api/ssh/test", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "rejected the key") {
		t.Fatalf("probe rejected: %d %q, want 502 with the fix", rec.Code, rec.Body)
	}
}

// No keypair generated yet: 409 before any probe runs.
func TestServerKeyTestWithoutKeypair(t *testing.T) {
	d, _, pinOut, st := newProjectDeps(t)
	d.SSHKeys = sshkeys.New(st)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := authedPost(t, h, cookie, "/api/ssh/test", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("no keypair: %d %q, want 409", rec.Code, rec.Body)
	}
}
