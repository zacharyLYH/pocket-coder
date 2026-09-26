package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"

	"pcoder/internal/state"
)

func TestAIModelsCRUDRedacted(t *testing.T) {
	d, pinOut := newTestDeps(t)
	d.State = mustOpenState(t)
	probe := func(w http.ResponseWriter, _ map[string]any) {
		writeCompletion(w, "stop", `{"ok":true}`, nil)
	}
	// One responder per probe: create, stored test, explicit test,
	// stored test after rename, model change. Unknown ids and renames
	// must not probe, so they cost nothing here.
	f := newFakeModel(t, probe, probe, probe, probe, probe)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// Empty list.
	rec := authedGet(t, h, cookie, "/api/ai/models")
	var list struct {
		Models []map[string]any `json:"models"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Models) != 0 {
		t.Fatalf("fresh list = %v, want empty", list.Models)
	}

	// Create tests before saving: bad key fails and stores nothing.
	rec = authedPost(t, h, cookie, "/api/ai/models",
		`{"label":"main","baseURL":"http://127.0.0.1:1","apiKey":"k","model":"m"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("bad create: got %d %q, want 502", rec.Code, rec.Body)
	}

	// Good create.
	rec = authedPost(t, h, cookie, "/api/ai/models",
		`{"label":"main","baseURL":`+strconv.Quote(f.srv.URL)+`,"apiKey":"k","model":"m"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d %q, want 201", rec.Code, rec.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == "" {
		t.Fatalf("no id: %s", rec.Body)
	}

	// List redacts the key.
	rec = authedGet(t, h, cookie, "/api/ai/models")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Models) != 1 || list.Models[0]["hasKey"] != true {
		t.Fatalf("list = %v, want one redacted row", list.Models)
	}
	if _, hasKey := list.Models[0]["apiKey"]; hasKey {
		t.Fatalf("list leaks apiKey: %v", list.Models[0])
	}

	// Stored-entry test with an empty body.
	rec = authedPost(t, h, cookie, "/api/ai/models/"+created.ID+"/test", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("stored test: got %d %q, want 200", rec.Code, rec.Body)
	}

	// Per-id test with explicit body.
	rec = authedPost(t, h, cookie, "/api/ai/models/"+created.ID+"/test",
		`{"baseURL":`+strconv.Quote(f.srv.URL)+`,"apiKey":"k","model":"m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("test: got %d %q, want 200", rec.Code, rec.Body)
	}

	// Unknown ids 404 without probing (the fake would fail a 6th call).
	rec = authedMethodBody(t, h, cookie, http.MethodPut, "/api/ai/models/deadbeef",
		`{"label":"alt","baseURL":`+strconv.Quote(f.srv.URL)+`,"apiKey":"k2","model":"m2"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown update: got %d, want 404", rec.Code)
	}
	rec = authedPost(t, h, cookie, "/api/ai/models/deadbeef/test", ``)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown test: got %d, want 404", rec.Code)
	}

	// Rename with an empty secret keeps the stored key and probes
	// nothing (same 5 responders still suffice below).
	rec = authedMethodBody(t, h, cookie, http.MethodPut, "/api/ai/models/"+created.ID,
		`{"label":"renamed","baseURL":`+strconv.Quote(f.srv.URL)+`,"apiKey":"","model":"m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: got %d %q, want 200", rec.Code, rec.Body)
	}
	rec = authedGet(t, h, cookie, "/api/ai/models")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Models) != 1 || list.Models[0]["label"] != "renamed" {
		t.Fatalf("list after rename = %v, want renamed row", list.Models)
	}

	// The stored key survived the rename: an empty-body test still passes.
	rec = authedPost(t, h, cookie, "/api/ai/models/"+created.ID+"/test", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("stored test after rename: got %d %q, want 200", rec.Code, rec.Body)
	}

	// Changing the model probes once and records the update.
	rec = authedMethodBody(t, h, cookie, http.MethodPut, "/api/ai/models/"+created.ID,
		`{"label":"renamed","baseURL":`+strconv.Quote(f.srv.URL)+`,"apiKey":"","model":"m2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d %q, want 200", rec.Code, rec.Body)
	}
	if ev := lastEvent(t, d); ev.Type != "ai.updated" {
		t.Fatalf("last event = %q, want ai.updated", ev.Type)
	}
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/ai/models/"+created.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: got %d %q, want 200", rec.Code, rec.Body)
	}
	if ev := lastEvent(t, d); ev.Type != "ai.removed" {
		t.Fatalf("last event = %q, want ai.removed", ev.Type)
	}
	rec = authedGet(t, h, cookie, "/api/ai/models")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Models) != 0 {
		t.Fatalf("after delete = %v, want empty", list.Models)
	}
}

// Duplicates are allowed in v1: each entry stands alone and the list
// head wins, so creating the same model twice yields two rows.
func TestAIModelsAllowDuplicates(t *testing.T) {
	d, pinOut := newTestDeps(t)
	d.State = mustOpenState(t)
	probe := func(w http.ResponseWriter, _ map[string]any) {
		writeCompletion(w, "stop", `{"ok":true}`, nil)
	}
	f := newFakeModel(t, probe, probe)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	body := `{"label":"m","baseURL":` + strconv.Quote(f.srv.URL) + `,"apiKey":"k","model":"m"}`
	for i := 0; i < 2; i++ {
		if rec := authedPost(t, h, cookie, "/api/ai/models", body); rec.Code != http.StatusCreated {
			t.Fatalf("create %d: got %d %q, want 201", i+1, rec.Code, rec.Body)
		}
	}
	rec := authedGet(t, h, cookie, "/api/ai/models")
	var list struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Models) != 2 || list.Models[0].ID == list.Models[1].ID {
		t.Fatalf("models = %+v, want two distinct rows", list.Models)
	}
}

func TestGitIdentitiesCRUDRedacted(t *testing.T) {
	d, _, pinOut, st := newProjectDeps(t)
	_ = st.Mutate(func(doc *state.Document) error {
		doc.GitIDs = nil
		return nil
	})
	fakeGitHub(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := authedPost(t, h, cookie, "/api/git/identities",
		`{"label":"work","name":"N","email":"n@e.com","token":"bad-token"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("bad create: got %d, want 502", rec.Code)
	}
	rec = authedPost(t, h, cookie, "/api/git/identities",
		`{"label":"work","name":"N","email":"n@e.com","token":"good-token"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d %q, want 201", rec.Code, rec.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = authedGet(t, h, cookie, "/api/git/identities")
	var list struct {
		Identities []map[string]any `json:"identities"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Identities) != 1 || list.Identities[0]["hasToken"] != true {
		t.Fatalf("list = %v", list.Identities)
	}
	if _, ok := list.Identities[0]["token"]; ok {
		t.Fatalf("list leaks token: %v", list.Identities[0])
	}

	// Stored-entry test with an empty body.
	rec = authedPost(t, h, cookie, "/api/git/identities/"+created.ID+"/test", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("stored test: got %d %q", rec.Code, rec.Body)
	}

	rec = authedPost(t, h, cookie, "/api/git/identities/"+created.ID+"/test",
		`{"name":"N","email":"n@e.com","token":"good-token"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("test: got %d %q", rec.Code, rec.Body)
	}

	// Partial bodies never reach the live check.
	rec = authedPost(t, h, cookie, "/api/git/identities/"+created.ID+"/test", `{"name":"N"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("partial test: got %d, want 400", rec.Code)
	}

	// Rename with an empty token keeps the stored one and probes nothing.
	rec = authedMethodBody(t, h, cookie, http.MethodPut, "/api/git/identities/"+created.ID,
		`{"label":"home","name":"N","email":"n@e.com","token":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: got %d %q, want 200", rec.Code, rec.Body)
	}
	rec = authedGet(t, h, cookie, "/api/git/identities")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Identities) != 1 || list.Identities[0]["label"] != "home" {
		t.Fatalf("list after rename = %v, want home row", list.Identities)
	}
	rec = authedPost(t, h, cookie, "/api/git/identities/"+created.ID+"/test", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("stored test after rename: got %d %q", rec.Code, rec.Body)
	}
	if ev := lastEvent(t, d); ev.Type != "git.updated" {
		t.Fatalf("last event = %q, want git.updated", ev.Type)
	}

	// Unknown ids 404 without a live call.
	rec = authedPost(t, h, cookie, "/api/git/identities/deadbeef/test", ``)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown test: got %d, want 404", rec.Code)
	}
	rec = authedMethodBody(t, h, cookie, http.MethodPut, "/api/git/identities/deadbeef",
		`{"label":"w","name":"N","email":"n@e.com","token":"good-token"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown update: got %d, want 404", rec.Code)
	}

	// Update then delete.
	rec = authedMethodBody(t, h, cookie, http.MethodPut, "/api/git/identities/"+created.ID,
		`{"label":"home","name":"N2","email":"n2@e.com","token":"good-token"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d %q", rec.Code, rec.Body)
	}
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/git/identities/"+created.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: got %d %q", rec.Code, rec.Body)
	}
	if ev := lastEvent(t, d); ev.Type != "git.removed" {
		t.Fatalf("last event = %q, want git.removed", ev.Type)
	}
	rec = authedGet(t, h, cookie, "/api/git/identities")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Identities) != 0 {
		t.Fatalf("after delete = %v, want empty", list.Identities)
	}
}

// Delete of an unknown id reports success (idempotent), but a delete that
// cannot persist must 500 — reporting ok while the entry survives on disk
// lies about the outcome.
func TestDeletePersistsOrFails(t *testing.T) {
	d, pinOut := newTestDeps(t)
	st := mustOpenState(t)
	d.State = st
	if err := st.Mutate(func(doc *state.Document) error {
		doc.AIModels = []state.AIModel{{ID: "m1", BaseURL: "https://x", APIKey: "k", Model: "m"}}
		doc.GitIDs = []state.GitIdentity{{ID: "g1", Name: "N", Email: "n@e.com", Token: "t"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Make save fail for everyone (root included): save writes to
	// state.json.tmp before renaming, so occupying that path with a
	// directory breaks every persist.
	if err := os.Mkdir(st.Path()+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(st.Path() + ".tmp") })

	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/ai/models/m1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("ai delete with failed save: got %d, want 500", rec.Code)
	}
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/git/identities/g1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("git delete with failed save: got %d, want 500", rec.Code)
	}

	// Memory agrees with disk: the failed deletes changed nothing, so
	// both rows still list (no resurrect-on-restart divergence).
	rec = authedGet(t, h, cookie, "/api/ai/models")
	var models struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &models)
	if len(models.Models) != 1 || models.Models[0].ID != "m1" {
		t.Fatalf("ai list after failed delete = %+v, want m1", models.Models)
	}
	rec = authedGet(t, h, cookie, "/api/git/identities")
	var ids struct {
		Identities []struct {
			ID string `json:"id"`
		} `json:"identities"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ids)
	if len(ids.Identities) != 1 || ids.Identities[0].ID != "g1" {
		t.Fatalf("git list after failed delete = %+v, want g1", ids.Identities)
	}
}

func TestSSHUpdateAndTest(t *testing.T) {
	d, _, pinOut, _ := newSessionDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := authedPost(t, h, cookie, "/api/ssh-keys",
		`{"publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGITest","label":"a"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	var added struct {
		Fingerprint string `json:"fingerprint"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &added)

	rec = authedMethodBody(t, h, cookie, http.MethodPut, "/api/ssh-keys/"+added.Fingerprint, `{"label":" b "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %q", rec.Code, rec.Body)
	}

	// Unknown fingerprints 404 instead of silently succeeding.
	rec = authedMethodBody(t, h, cookie, http.MethodPut, "/api/ssh-keys/sha256-nope", `{"label":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown update: got %d, want 404", rec.Code)
	}

	// The label stuck.
	rec = authedGet(t, h, cookie, "/api/ssh-keys")
	var keys struct {
		Keys []struct {
			Label string `json:"label"`
		} `json:"keys"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &keys)
	if len(keys.Keys) != 1 || keys.Keys[0].Label != "b" {
		t.Fatalf("keys after update = %+v, want label b", keys.Keys)
	}

	// Test validates without saving.
	rec = authedPost(t, h, cookie, "/api/ssh-keys/test", `{"publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINewKey"}`)
	if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("test: %d %q", rec.Code, rec.Body)
	}
	rec = authedPost(t, h, cookie, "/api/ssh-keys/test", `{"publicKey":"nope"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad test: %d, want 400", rec.Code)
	}
}
