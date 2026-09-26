// List endpoints for the shared model, git, and key lists. One pattern
// for all three: list, add, update, delete, test. Secrets never render:
// lists carry hasKey/hasToken only. Every save runs a live check first.
package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"pcoder/internal/agent"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
)

func aiListItem(m state.AIModel) map[string]any {
	return map[string]any{"id": m.ID, "label": m.Label, "baseURL": m.BaseURL, "model": m.Model, "hasKey": m.APIKey != ""}
}

func gitListItem(g state.GitIdentity) map[string]any {
	return map[string]any{"id": g.ID, "label": g.Label, "name": g.Name, "email": g.Email, "hasToken": g.Token != ""}
}

// aiIndex/gitIndex locate a list entry by id, -1 when missing. Every
// id-scoped handler checks existence first, so unknown ids 404 before
// any live probe burns provider calls or rate limits.
func aiIndex(doc *state.Document, id string) int {
	for i := range doc.AIModels {
		if doc.AIModels[i].ID == id {
			return i
		}
	}
	return -1
}

func gitIndex(doc *state.Document, id string) int {
	for i := range doc.GitIDs {
		if doc.GitIDs[i].ID == id {
			return i
		}
	}
	return -1
}

func aiExists(d Deps, id string) bool {
	var ok bool
	d.State.View(func(doc *state.Document) { ok = aiIndex(doc, id) >= 0 })
	return ok
}

func gitExists(d Deps, id string) bool {
	var ok bool
	d.State.View(func(doc *state.Document) { ok = gitIndex(doc, id) >= 0 })
	return ok
}

func handleListAIModels(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		d.State.View(func(doc *state.Document) {
			for _, m := range doc.AIModels {
				out = append(out, aiListItem(m))
			}
		})
		writeJSON(w, http.StatusOK, map[string]any{"models": out})
	}
}

func handleCreateAIModel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Label   string `json:"label"`
			BaseURL string `json:"baseURL"`
			APIKey  string `json:"apiKey"`
			Model   string `json:"model"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		body.BaseURL = agent.NormalizeBaseURL(strings.TrimSpace(body.BaseURL))
		body.Model = strings.TrimSpace(body.Model)
		cfg := agent.Config{BaseURL: body.BaseURL, APIKey: body.APIKey, Model: body.Model}
		if !cfg.Valid() {
			writeErr(w, http.StatusBadRequest, "base URL, API key, and model are all required")
			return
		}
		if err := agent.TestConnection(r.Context(), cfg); err != nil {
			writeErr(w, http.StatusBadGateway, "test call failed: "+err.Error())
			return
		}
		id := state.MintID()
		if id == "" {
			writeInternalErr(w, "mint model id", errors.New("mint failed"))
			return
		}
		if err := d.State.Mutate(func(doc *state.Document) error {
			doc.AIModels = append(doc.AIModels, state.AIModel{ID: id, Label: strings.TrimSpace(body.Label), BaseURL: body.BaseURL, APIKey: body.APIKey, Model: body.Model})
			return nil
		}); err != nil {
			writeInternalErr(w, "save AI model", err)
			return
		}
		_, _ = d.Events.Append("ai.configured", map[string]any{"baseURL": body.BaseURL, "model": body.Model})
		writeJSON(w, http.StatusCreated, map[string]any{"id": id})
	}
}

func handleUpdateAIModel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var stored *state.AIModel
		d.State.View(func(doc *state.Document) {
			if i := aiIndex(doc, id); i >= 0 {
				c := doc.AIModels[i]
				stored = &c
			}
		})
		if stored == nil {
			writeErr(w, http.StatusNotFound, "unknown model")
			return
		}
		var body struct {
			Label   string `json:"label"`
			BaseURL string `json:"baseURL"`
			APIKey  string `json:"apiKey"`
			Model   string `json:"model"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		body.BaseURL = agent.NormalizeBaseURL(strings.TrimSpace(body.BaseURL))
		body.Model = strings.TrimSpace(body.Model)
		// Empty secret keeps the stored one, so a rename never asks for
		// the key again — and a rename probes nothing, since the provider
		// sees nothing new.
		if body.APIKey == "" {
			body.APIKey = stored.APIKey
		}
		cfg := agent.Config{BaseURL: body.BaseURL, APIKey: body.APIKey, Model: body.Model}
		if !cfg.Valid() {
			writeErr(w, http.StatusBadRequest, "base URL, API key, and model are all required")
			return
		}
		if body.BaseURL != stored.BaseURL || body.Model != stored.Model || body.APIKey != stored.APIKey {
			if err := agent.TestConnection(r.Context(), cfg); err != nil {
				writeErr(w, http.StatusBadGateway, "test call failed: "+err.Error())
				return
			}
		}
		found := false
		if err := d.State.Mutate(func(doc *state.Document) error {
			if i := aiIndex(doc, id); i >= 0 {
				doc.AIModels[i] = state.AIModel{ID: id, Label: strings.TrimSpace(body.Label), BaseURL: body.BaseURL, APIKey: body.APIKey, Model: body.Model}
				found = true
			}
			return nil
		}); err != nil {
			writeInternalErr(w, "save AI model", err)
			return
		}
		if !found {
			writeErr(w, http.StatusNotFound, "unknown model")
			return
		}
		_, _ = d.Events.Append("ai.updated", map[string]any{"id": id})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleDeleteAIModel removes one model by id. Unknown ids still answer
// ok (delete is idempotent); only a failed persist 500s, because ok while
// the row survives on disk lies about the outcome.
func handleDeleteAIModel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := d.State.Mutate(func(doc *state.Document) error {
			kept := doc.AIModels[:0]
			for _, m := range doc.AIModels {
				if m.ID != id {
					kept = append(kept, m)
				}
			}
			doc.AIModels = kept
			return nil
		}); err != nil {
			writeInternalErr(w, "delete AI model", err)
			return
		}
		_, _ = d.Events.Append("ai.removed", map[string]any{"id": id})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleTestAIModelBody tests unsaved fields without an id: the
// test-then-save probe for the add form.
func handleTestAIModelBody(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body aiBody
		if !decodeBody(w, r, &body, false) {
			return
		}
		cfg := agent.Config{BaseURL: agent.NormalizeBaseURL(body.BaseURL), APIKey: body.APIKey, Model: strings.TrimSpace(body.Model)}
		if !cfg.Valid() {
			writeErr(w, http.StatusBadRequest, "base URL, API key, and model are all required")
			return
		}
		if err := agent.TestConnection(r.Context(), cfg); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleTestAIModel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !aiExists(d, id) {
			writeErr(w, http.StatusNotFound, "unknown model")
			return
		}
		var body aiBody
		if !decodeBody(w, r, &body, true) {
			return
		}
		cfg := agent.Config{BaseURL: agent.NormalizeBaseURL(body.BaseURL), APIKey: body.APIKey, Model: strings.TrimSpace(body.Model)}
		if !cfg.Valid() {
			// Empty body tests the stored entry without resending the key.
			if body.BaseURL != "" || body.APIKey != "" || body.Model != "" {
				writeErr(w, http.StatusBadRequest, "base URL, API key, and model are all required")
				return
			}
			var stored *state.AIModel
			d.State.View(func(doc *state.Document) {
				if i := aiIndex(doc, id); i >= 0 {
					c := doc.AIModels[i]
					stored = &c
				}
			})
			if stored == nil {
				writeErr(w, http.StatusBadRequest, "base URL, API key, and model are all required")
				return
			}
			cfg = agent.Config{BaseURL: stored.BaseURL, APIKey: stored.APIKey, Model: stored.Model}
		}
		if err := agent.TestConnection(r.Context(), cfg); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleListGitIDs(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		d.State.View(func(doc *state.Document) {
			for _, g := range doc.GitIDs {
				out = append(out, gitListItem(g))
			}
		})
		writeJSON(w, http.StatusOK, map[string]any{"identities": out})
	}
}

func handleCreateGitID(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Label string `json:"label"`
			Name  string `json:"name"`
			Email string `json:"email"`
			Token string `json:"token"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		gb := gitBody{Name: strings.TrimSpace(body.Name), Email: strings.TrimSpace(body.Email), Token: strings.TrimSpace(body.Token)}
		if !validGitBody(gb) {
			writeErr(w, http.StatusBadRequest, "name, email, and PAT are all required")
			return
		}
		if err := testGitToken(r, gb.Token); err != nil {
			writeErr(w, http.StatusBadGateway, "test call failed: "+err.Error())
			return
		}
		id := state.MintID()
		if id == "" {
			writeInternalErr(w, "mint git id", errors.New("mint failed"))
			return
		}
		if err := d.State.Mutate(func(doc *state.Document) error {
			doc.GitIDs = append(doc.GitIDs, state.GitIdentity{ID: id, Label: strings.TrimSpace(body.Label), Name: gb.Name, Email: gb.Email, Token: gb.Token})
			return nil
		}); err != nil {
			writeInternalErr(w, "save git identity", err)
			return
		}
		_, _ = d.Events.Append("git.configured", map[string]any{"name": gb.Name, "email": gb.Email})
		writeJSON(w, http.StatusCreated, map[string]any{"id": id})
	}
}

func handleUpdateGitID(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var stored *state.GitIdentity
		d.State.View(func(doc *state.Document) {
			if i := gitIndex(doc, id); i >= 0 {
				c := doc.GitIDs[i]
				stored = &c
			}
		})
		if stored == nil {
			writeErr(w, http.StatusNotFound, "unknown identity")
			return
		}
		var body struct {
			Label string `json:"label"`
			Name  string `json:"name"`
			Email string `json:"email"`
			Token string `json:"token"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		// Empty token keeps the stored one, so a rename never asks for
		// the PAT again. A rename probes nothing, since GitHub sees
		// nothing new.
		token := strings.TrimSpace(body.Token)
		if token == "" {
			token = stored.Token
		}
		gb := gitBody{Name: strings.TrimSpace(body.Name), Email: strings.TrimSpace(body.Email), Token: token}
		if !validGitBody(gb) {
			writeErr(w, http.StatusBadRequest, "name, email, and PAT are all required")
			return
		}
		if gb.Name != stored.Name || gb.Email != stored.Email || gb.Token != stored.Token {
			if err := testGitToken(r, gb.Token); err != nil {
				writeErr(w, http.StatusBadGateway, "test call failed: "+err.Error())
				return
			}
		}
		found := false
		if err := d.State.Mutate(func(doc *state.Document) error {
			if i := gitIndex(doc, id); i >= 0 {
				doc.GitIDs[i] = state.GitIdentity{ID: id, Label: strings.TrimSpace(body.Label), Name: gb.Name, Email: gb.Email, Token: gb.Token}
				found = true
			}
			return nil
		}); err != nil {
			writeInternalErr(w, "save git identity", err)
			return
		}
		if !found {
			writeErr(w, http.StatusNotFound, "unknown identity")
			return
		}
		_, _ = d.Events.Append("git.updated", map[string]any{"id": id})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleDeleteGitID mirrors handleDeleteAIModel: idempotent on unknown
// ids, 500 only when the persist itself fails.
func handleDeleteGitID(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := d.State.Mutate(func(doc *state.Document) error {
			kept := doc.GitIDs[:0]
			for _, g := range doc.GitIDs {
				if g.ID != id {
					kept = append(kept, g)
				}
			}
			doc.GitIDs = kept
			return nil
		}); err != nil {
			writeInternalErr(w, "delete git identity", err)
			return
		}
		_, _ = d.Events.Append("git.removed", map[string]any{"id": id})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleTestGitBody tests unsaved fields without an id.
func handleTestGitBody(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeGitBody(w, r)
		if !ok {
			return
		}
		if err := testGitToken(r, body.Token); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleTestGitID(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !gitExists(d, id) {
			writeErr(w, http.StatusNotFound, "unknown identity")
			return
		}
		var raw gitBody
		if !decodeBody(w, r, &raw, true) {
			return
		}
		raw.Name = strings.TrimSpace(raw.Name)
		raw.Email = strings.TrimSpace(raw.Email)
		raw.Token = strings.TrimSpace(raw.Token)
		if raw.Name == "" && raw.Email == "" && raw.Token == "" {
			var token string
			d.State.View(func(doc *state.Document) {
				if i := gitIndex(doc, id); i >= 0 {
					token = doc.GitIDs[i].Token
				}
			})
			if token == "" {
				writeErr(w, http.StatusBadRequest, "name, email, and PAT are all required")
				return
			}
			if err := testGitToken(r, token); err != nil {
				writeErr(w, http.StatusBadGateway, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if !validGitBody(raw) {
			writeErr(w, http.StatusBadRequest, "name, email, and PAT are all required")
			return
		}
		if err := testGitToken(r, raw.Token); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleUpdateSSHKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Label string `json:"label"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		fp := r.PathValue("fingerprint")
		if !d.SSHKeys.Exists(d.Auth.Email(r), fp) {
			writeErr(w, http.StatusNotFound, "unknown key")
			return
		}
		if err := d.SSHKeys.Update(d.Auth.Email(r), fp, strings.TrimSpace(body.Label)); err != nil {
			writeInternalErr(w, "update ssh key", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleTestSSHKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PublicKey string `json:"publicKey"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		fp, err := sshkeys.Check(body.PublicKey)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "fingerprint": fp})
	}
}
