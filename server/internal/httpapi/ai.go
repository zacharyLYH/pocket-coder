// AI model lookup: the shared model list head. Empty list disables
// codemaps in the frontend and the backend (409 on POST codemap).
package httpapi

import (
	"pcoder/internal/agent"
	"pcoder/internal/state"
)

// aiConfig reads the first stored model into an agent.Config.
func aiConfig(d Deps) agent.Config {
	var cfg agent.Config
	if d.State != nil {
		d.State.View(func(doc *state.Document) {
			if m := state.FirstAIModel(doc); m != nil {
				cfg = agent.Config{BaseURL: m.BaseURL, APIKey: m.APIKey, Model: m.Model}
			}
		})
	}
	cfg.BaseURL = agent.NormalizeBaseURL(cfg.BaseURL)
	return cfg
}

type aiBody struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}
