package httpapi

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/project"
	"pcoder/internal/state"
	"pcoder/internal/state/statetest"
)

// TestStateFileIsSingleSourceOfTruth drives the real HTTP surface over the
// real stores (Docker is mocked), then asserts that the on-disk state.json
// is EXACTLY what the API traffic implies — every section, every field,
// nothing more, nothing less. The comparison runs against a generic map, so
// an unexpected key (or a lost one) fails loudly instead of hiding behind
// the typed Document. This is the contract for the source of truth.
func TestStateFileIsSingleSourceOfTruth(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	// mirror production wiring (main.go): identity seeded into a fresh
	// document, key store attached to the project pipeline
	if err := st.Mutate(func(doc *state.Document) error {
		doc.User.Email = "me@example.com"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d.Projects.SetSSHKeys(d.SSHKeys)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// --- 1. server key: generated on boot, shown without the private half ---
	kp, err := d.SSHKeys.EnsureKeypair()
	if err != nil {
		t.Skipf("ssh-keygen unavailable: %v", err)
	}
	rec := authedGet(t, h, cookie, "/api/ssh")
	var shown struct {
		PublicKey   string `json:"publicKey"`
		Fingerprint string `json:"fingerprint"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &shown)
	if rec.Code != 200 || shown.PublicKey != kp.PublicKey || shown.Fingerprint != kp.Fingerprint {
		t.Fatalf("show key: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "PRIVATE") {
		t.Fatalf("show leaks the private key: %q", rec.Body)
	}

	// --- 2. register a harness (native CLI config would ride along) ---
	rec = authedPost(t, h, cookie, "/api/harnesses",
		`{"name":"My Agent","command":"my-agent","install":"npm i -g my-agent"}`)
	if rec.Code != 201 {
		t.Fatalf("add harness: %d %s", rec.Code, rec.Body)
	}

	// --- 3. create a project: project pipeline runs, key injected ---
	md.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	md.EXPECT().InspectImage(mock.Anything, project.ProjectImage).Return(nil)
	md.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	md.EXPECT().Exec(mock.Anything, "cid",
		[]string{"sh", "-c", "mkdir -p /root/.ssh && chmod 700 /root/.ssh"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().WriteFile(mock.Anything, "cid", "/root/.ssh/id_ed25519", mock.Anything).Return(nil)
	md.EXPECT().WriteFile(mock.Anything, "cid", "/root/.ssh/id_ed25519.pub", mock.Anything).Return(nil)
	md.EXPECT().Exec(mock.Anything, "cid",
		[]string{"sh", "-c", "grep -q 'IdentityFile /root/.ssh/id_ed25519' /root/.ssh/config 2>/dev/null"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	md.EXPECT().Exec(mock.Anything, "cid", mock.MatchedBy(func(argv []string) bool {
		return len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" &&
			strings.Contains(argv[2], ">> /root/.ssh/config")
	}), false).Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().WriteFile(mock.Anything, "cid", "/root/.ssh-configured-sha", mock.Anything).Return(nil)
	md.EXPECT().Exec(mock.Anything, "cid",
		[]string{"git", "clone", "git@github.com:x/hello.git", "/workspace/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	rec = authedPost(t, h, cookie, "/api/projects",
		`{"repoUrl":"https://github.com/x/hello.git"}`)
	if rec.Code != 201 {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body)
	}
	var proj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &proj); err != nil || proj.ID == "" {
		t.Fatalf("create resp = %s (%v)", rec.Body, err)
	}

	// everything so far is already persisted — and the file is EXACTLY the
	// sum of what the API did: user, identity, harnesses, server key, project
	// (with its default shortcuts). The private half is asserted present
	// (never asserted by value).
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": "me@example.com"},
		"harnesses": map[string]any{
			"fake":     wantFakeHarnessEntry,
			"my-agent": map[string]any{"id": "my-agent", "name": "My Agent", "command": "my-agent", "install": "npm i -g my-agent"},
		},
		"serverKey": map[string]any{
			"privateKey":  kp.PrivateKey,
			"fingerprint": kp.Fingerprint,
			"publicKey":   kp.PublicKey,
			"createdAt":   kp.CreatedAt,
		},
		"projects": map[string]any{
			proj.ID: map[string]any{"repo": "git@github.com:x/hello.git", "shortcuts": state.DefaultShortcuts()},
		},
	})

	// --- 4. delete the project entirely ---
	cname := project.ContainerName(proj.ID)
	md.EXPECT().Stop(mock.Anything, cname, mock.Anything).Return(nil)
	md.EXPECT().Remove(mock.Anything, cname, true).Return(nil)
	md.EXPECT().RemoveVolume(mock.Anything, cname+"-home").Return(nil)
	md.EXPECT().RemoveVolume(mock.Anything, cname+"-repo").Return(nil)
	rec = authedRequest(t, h, cookie, "DELETE", "/api/projects/"+url.PathEscape(proj.ID)+"?scope=all")
	if rec.Code != 200 {
		t.Fatalf("delete project: %d %s", rec.Code, rec.Body)
	}

	// --- deletions hit the same single file: the project is gone, and
	// ONLY the untouched harnesses, identity, and server key remain ---
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": "me@example.com"},
		"harnesses": map[string]any{
			"fake":     wantFakeHarnessEntry,
			"my-agent": map[string]any{"id": "my-agent", "name": "My Agent", "command": "my-agent", "install": "npm i -g my-agent"},
		},
		"serverKey": map[string]any{
			"privateKey":  kp.PrivateKey,
			"fingerprint": kp.Fingerprint,
			"publicKey":   kp.PublicKey,
			"createdAt":   kp.CreatedAt,
		},
	})
}
