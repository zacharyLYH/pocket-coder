package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
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

	keyBody := "ssh-ed25519 AAAA-mykey"
	sum := sha256.Sum256([]byte(keyBody))
	wantFp := "sha256-" + base64.RawURLEncoding.EncodeToString(sum[:16])

	// --- 1. register an SSH key ---
	rec := authedPost(t, h, cookie, "/api/ssh-keys",
		`{"publicKey":"`+keyBody+`","label":"laptop"}`)
	if rec.Code != 201 || rec.Body.String() != `{"fingerprint":"`+wantFp+`"}`+"\n" {
		t.Fatalf("add ssh key: %d %s (want fp %s)", rec.Code, rec.Body, wantFp)
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
	md.EXPECT().WriteFile(mock.Anything, "cid", "/root/.ssh/authorized_keys",
		[]byte(keyBody+"\n")).Return(nil)
	md.EXPECT().Exec(mock.Anything, "cid",
		[]string{"git", "clone", "https://github.com/x/hello.git", "/workspace/repo"}, false).
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
	// sum of what the API did: one user, two harnesses, one key, one project
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": "me@example.com"},
		"harnesses": map[string]any{
			"fake":     wantFakeHarnessEntry,
			"my-agent": map[string]any{"id": "my-agent", "name": "My Agent", "command": "my-agent", "install": "npm i -g my-agent"},
		},
		"sshKeys": []any{map[string]any{
			"fingerprint": wantFp,
			"publicKey":   keyBody,
			"label":       "laptop",
			"email":       "me@example.com",
		}},
		"projects": map[string]any{
			proj.ID: map[string]any{"repo": "https://github.com/x/hello.git", "cloneMethod": "http"},
		},
	})

	// --- 4. delete the ssh key ---
	rec = authedRequest(t, h, cookie, "DELETE", "/api/ssh-keys/"+wantFp)
	if rec.Code != 200 {
		t.Fatalf("delete ssh key: %d %s", rec.Code, rec.Body)
	}

	// --- 5. delete the project entirely ---
	cname := project.ContainerName(proj.ID)
	md.EXPECT().Stop(mock.Anything, cname, mock.Anything).Return(nil)
	md.EXPECT().Remove(mock.Anything, cname, true).Return(nil)
	md.EXPECT().RemoveVolume(mock.Anything, cname+"-home").Return(nil)
	md.EXPECT().RemoveVolume(mock.Anything, cname+"-repo").Return(nil)
	rec = authedRequest(t, h, cookie, "DELETE", "/api/projects/"+url.PathEscape(proj.ID)+"?scope=all")
	if rec.Code != 200 {
		t.Fatalf("delete project: %d %s", rec.Code, rec.Body)
	}

	// --- deletions hit the same single file: the key and the project are
	// gone, and ONLY the untouched harnesses remain ---
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": "me@example.com"},
		"harnesses": map[string]any{
			"fake":     wantFakeHarnessEntry,
			"my-agent": map[string]any{"id": "my-agent", "name": "My Agent", "command": "my-agent", "install": "npm i -g my-agent"},
		},
	})
}
