// List endpoints for the shared model list: list, add, update, delete. Secrets never render: model lists carry hasKey only.
package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"pcoder/internal/agent"
	"pcoder/internal/state"
)

func aiListItem(m state.AIModel) map[string]any {
	return map[string]any{"id": m.ID, "label": m.Label, "baseURL": m.BaseURL, "model": m.Model, "hasKey": m.APIKey != ""}
}

// aiIndex locates a list entry by id, -1 when missing. Every
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

func aiExists(d Deps, id string) bool {
	var ok bool
	d.State.View(func(doc *state.Document) { ok = aiIndex(doc, id) >= 0 })
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

// aiLookup returns a copy of the model with id, false when missing.
// Copies out of the read lock (see Store.View) so callers can use it
// after the lock drops; every id-scoped handler 404s up front so no live
// probe burns provider calls on unknown ids.
func aiLookup(d Deps, id string) (state.AIModel, bool) {
	var out state.AIModel
	var ok bool
	d.State.View(func(doc *state.Document) {
		if i := aiIndex(doc, id); i >= 0 {
			out = doc.AIModels[i]
			ok = true
		}
	})
	return out, ok
}

func handleUpdateAIModel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		stored, ok := aiLookup(d, id)
		if !ok {
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
			stored, ok := aiLookup(d, id)
			if !ok {
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
