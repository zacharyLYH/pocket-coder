// AI settings endpoints: the single global OpenAI-compatible credential.
// One key only. Missing key disables codemaps in the frontend (via
// configured=false) and the backend (409 on POST codemap).
package httpapi

import (
	"net/http"
	"strings"

	"pcoder/internal/agent"
	"pcoder/internal/state"
)

// aiConfig reads the stored credential into an agent.Config.
func aiConfig(d Deps, fallback aiBody) agent.Config {
	var cfg agent.Config
	if d.State != nil {
		d.State.View(func(doc *state.Document) {
			if doc.AI != nil {
				cfg = agent.Config{BaseURL: doc.AI.BaseURL, APIKey: doc.AI.APIKey, Model: doc.AI.Model}
			}
		})
	}
	if fallback.BaseURL != "" {
		cfg.BaseURL = fallback.BaseURL
	}
	if fallback.APIKey != "" {
		cfg.APIKey = fallback.APIKey
	}
	if fallback.Model != "" {
		cfg.Model = fallback.Model
	}
	cfg.BaseURL = agent.NormalizeBaseURL(cfg.BaseURL)
	return cfg
}

type aiBody struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

func handleGetAIConfig(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := aiConfig(d, aiBody{})
		writeJSON(w, http.StatusOK, map[string]any{
			"baseURL": cfg.BaseURL, "model": cfg.Model, "configured": cfg.Valid(),
		})
	}
}

func handleTestAI(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body aiBody
		if !decodeBody(w, r, &body, false) {
			return
		}
		cfg := aiConfig(d, body)
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

func handleSaveAIConfig(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body aiBody
		if !decodeBody(w, r, &body, false) {
			return
		}
		body.BaseURL = agent.NormalizeBaseURL(strings.TrimSpace(body.BaseURL))
		body.Model = strings.TrimSpace(body.Model)
		if body.BaseURL == "" || body.APIKey == "" || body.Model == "" {
			writeErr(w, http.StatusBadRequest, "base URL, API key, and model are all required")
			return
		}
		cfg := agent.Config{BaseURL: body.BaseURL, APIKey: body.APIKey, Model: body.Model}
		// Test before save: a stored key that never worked helps nobody.
		if err := agent.TestConnection(r.Context(), cfg); err != nil {
			writeErr(w, http.StatusBadGateway, "test call failed: "+err.Error())
			return
		}
		if err := d.State.Mutate(func(doc *state.Document) error {
			doc.AI = &state.AIConfig{BaseURL: body.BaseURL, APIKey: body.APIKey, Model: body.Model}
			return nil
		}); err != nil {
			writeInternalErr(w, "save AI config", err)
			return
		}
		_, _ = d.Events.Append("ai.configured", map[string]any{"baseURL": body.BaseURL, "model": body.Model})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
