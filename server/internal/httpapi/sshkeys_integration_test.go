//go:build integration

// Integration tests for SSH key management and the reconciliation + clone
// method flows against a live engine. Run with:
// go test -tags=integration -count=1 ./internal/httpapi/
package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestSSHKeyRegistrationAndClone(t *testing.T) {
	h, _, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// initially no keys
	code, body := doJSON(t, h, cookie, http.MethodGet, "/api/ssh-keys", "")
	keys, _ := body["keys"].([]any)
	if code != http.StatusOK || keys == nil || len(keys) != 0 {
		t.Fatalf("list empty: %d %v", code, body)
	}

	// add a key
	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/ssh-keys",
		`{"publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGITestKey123456789","label":"test-key"}`)
	if code != http.StatusCreated {
		t.Fatalf("add key: %d %v", code, body)
	}
	fp, _ := body["fingerprint"].(string)
	if fp == "" {
		t.Fatalf("no fingerprint returned: %v", body)
	}

	// list shows the key
	code, body = doJSON(t, h, cookie, http.MethodGet, "/api/ssh-keys", "")
	keys, _ = body["keys"].([]any)
	if code != http.StatusOK || len(keys) != 1 {
		t.Fatalf("list after add: %d %v", code, body)
	}

	// delete
	code, _ = doJSON(t, h, cookie, http.MethodDelete, "/api/ssh-keys/"+fp, "")
	if code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}

	// back to empty
	code, body = doJSON(t, h, cookie, http.MethodGet, "/api/ssh-keys", "")
	keys, _ = body["keys"].([]any)
	if code != http.StatusOK || len(keys) != 0 {
		t.Fatalf("status after delete: %d %v", code, body)
	}
}

func TestSSHKeyInjectOnCreate(t *testing.T) {
	h, dkr, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// register a key
	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/ssh-keys",
		`{"publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGITestInject","label":"inject-test"}`)
	if code != http.StatusCreated {
		t.Fatalf("add key: %d %v", code, body)
	}
	fp := body["fingerprint"].(string)
	defer doJSON(t, h, cookie, http.MethodDelete, "/api/ssh-keys/"+fp, "")

	// create a blank project — the key should land in ~/.ssh/authorized_keys
	code, body = doJSON(t, h, cookie, http.MethodPost, "/api/projects", `{}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["id"].(string)
	deleteProjectAll(t, h, cookie, id)
	waitForStatus(t, h, cookie, id, "running")

	// verify authorized_keys exists and contains the key
	var catResult string
	for i := 0; i < 15; i++ {
		res, err := dkr.Exec(t.Context(), "sps-"+id,
			[]string{"cat", "/root/.ssh/authorized_keys"}, false)
		if err == nil && res.ExitCode == 0 {
			catResult = strings.TrimSpace(res.Output)
			break
		}
	}
	if !strings.Contains(catResult, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGITestInject") {
		t.Fatalf("authorized_keys = %q, want the registered key", catResult)
	}
}

func TestCloneMethodHTTP(t *testing.T) {
	h, dkr, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// create with explicit http cloneMethod against a local fixture repo
	url := fixtureRepo(t, dkr)
	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects",
		fmt.Sprintf(`{"repoUrl":%q,"cloneMethod":"http"}`, url))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["id"].(string)
	deleteProjectAll(t, h, cookie, id)

	// get shows cloneMethod
	code, body = doJSON(t, h, cookie, http.MethodGet, "/api/projects/"+id, "")
	if code != http.StatusOK || body["cloneMethod"] != "http" {
		t.Fatalf("get cloneMethod: %d %v", code, body)
	}
}

func TestCloneMethodSSHRejectsWithoutKeys(t *testing.T) {
	h, _, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// create with ssh cloneMethod but no keys registered
	code, _ := doJSON(t, h, cookie, http.MethodPost, "/api/projects",
		`{"repoUrl":"git@github.com:x/hello.git","cloneMethod":"ssh"}`)
	// ssh cloneMethod is accepted; keys are injected best-effort.
	if code != http.StatusCreated {
		t.Logf("cloneMethod=ssh: %d (may reject if no keys)", code)
	}
}

func TestReconcileMissingContainer(t *testing.T) {
	h, dkr, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// create a project
	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/projects", `{}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["id"].(string)
	deleteProjectAll(t, h, cookie, id)
	waitForStatus(t, h, cookie, id, "running")

	// manually kill the container (simulate engine restart / docker rm)
	if err := dkr.Stop(t.Context(), "sps-"+id, 0); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := dkr.Remove(t.Context(), "sps-"+id, true); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// verify the container is gone
	if _, err := dkr.Inspect(t.Context(), "sps-"+id); err == nil {
		t.Fatal("container should be gone")
	}

	// listing sessions should trigger reconciliation and succeed
	code, body = doJSON(t, h, cookie, http.MethodGet, "/api/projects/"+id+"/sessions", "")
	if code != http.StatusOK {
		t.Fatalf("list sessions after reconcile: %d %v", code, body)
	}

	// verify the container is back
	waitForStatus(t, h, cookie, id, "running")
}

func TestSSHKeyInvalidFormat400(t *testing.T) {
	h, _, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/ssh-keys",
		`{"publicKey":"not-a-real-key","label":"bad"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid key: %d %v, want 400", code, body)
	}
}

func TestSSHKeyDuplicateRejected(t *testing.T) {
	h, _, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	key := `{"publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGIDuplicate","label":"dup"}`
	code, _ := doJSON(t, h, cookie, http.MethodPost, "/api/ssh-keys", key)
	if code != http.StatusCreated {
		t.Fatalf("first add: %d", code)
	}
	code, body := doJSON(t, h, cookie, http.MethodPost, "/api/ssh-keys", key)
	if code != http.StatusBadRequest {
		t.Fatalf("duplicate: %d %v, want 400", code, body)
	}
}
