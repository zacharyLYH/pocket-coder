package httpapi

// SSH key endpoint tests — extracted from httpapi_sessions_test.go: these tests cover the SSH key API,
// not session semantics.

import (
	"encoding/json"
	"net/http"
	"testing"

	"pcoder/internal/state/statetest"
)

func TestSSHKeysAPI(t *testing.T) {
	d, _, pinOut, dataDir := newSessionDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// initially empty
	rec := authedGet(t, h, cookie, "/api/ssh-keys")
	var list struct {
		Keys []struct{} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Keys) != 0 {
		t.Fatalf("list empty: %d %v err=%v", rec.Code, rec.Body, err)
	}

	// add a key
	rec = authedPost(t, h, cookie, "/api/ssh-keys",
		`{"publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGITest","label":"test"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	var added struct {
		Fingerprint string `json:"fingerprint"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &added)
	if added.Fingerprint == "" {
		t.Fatal("no fingerprint returned")
	}

	// list shows one key
	rec = authedGet(t, h, cookie, "/api/ssh-keys")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Keys) != 1 {
		t.Fatalf("list after add: %d %v", rec.Code, rec.Body)
	}
	// and the state file carries exactly that key, exactly these fields
	statetest.AssertSection(t, dataDir.Path(), "sshKeys", []any{map[string]any{
		"fingerprint": added.Fingerprint,
		"publicKey":   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGITest",
		"label":       "test",
		"email":       "me@example.com",
	}})

	// duplicate rejected
	rec = authedPost(t, h, cookie, "/api/ssh-keys",
		`{"publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGITest"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate: %d, want 400", rec.Code)
	}

	// invalid key rejected
	rec = authedPost(t, h, cookie, "/api/ssh-keys",
		`{"publicKey":"not-a-key"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid key: %d, want 400", rec.Code)
	}

	// delete
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/ssh-keys/"+added.Fingerprint)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}

	// back to empty — and the section is gone from the state file entirely
	rec = authedGet(t, h, cookie, "/api/ssh-keys")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Keys) != 0 {
		t.Fatalf("list after delete: %d %v", rec.Code, rec.Body)
	}
	statetest.AssertSection(t, dataDir.Path(), "harnesses", map[string]any{
		"fake": wantFakeHarnessEntry,
	})
}

// TestLazyReconciliationViaSessionHandler proves the disk-is-truth
// guarantee: when a container is missing but the project exists on disk,
// EnsureContainer inside the handler recreates it.
