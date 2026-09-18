package bot

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"hermes-tele/config"
)

type ModelInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type ModelResolver struct {
	gatewayURL string
	gatewayKey string
	cached     []ModelInfo
	lastFetch  time.Time
	mu         sync.RWMutex
}

func NewModelResolver(gatewayURL, gatewayKey string) *ModelResolver {
	if gatewayURL == "" {
		gatewayURL = "http://127.0.0.1:8080"
	}
	return &ModelResolver{
		gatewayURL: gatewayURL,
		gatewayKey: gatewayKey,
	}
}

// GetModels returns dynamically discovered models, prioritizing default and clean names
func (r *ModelResolver) GetModels(cfg *config.Config, currentModel string) []ModelInfo {
	// 1. If custom models are explicitly defined in config, use them directly
	if cfg != nil && len(cfg.Hermes.Models) > 0 {
		var res []ModelInfo
		for _, m := range cfg.Hermes.Models {
			lbl := m.Label
			if lbl == "" {
				lbl = FormatModelLabel(m.Name)
			}
			res = append(res, ModelInfo{ID: m.Name, Label: lbl})
		}
		return ensureCurrentModel(res, currentModel)
	}

	// 2. Check in-memory cache (30s TTL)
	r.mu.RLock()
	if time.Since(r.lastFetch) < 30*time.Second && len(r.cached) > 0 {
		cached := append([]ModelInfo(nil), r.cached...)
		r.mu.RUnlock()
		return ensureCurrentModel(cached, currentModel)
	}
	r.mu.RUnlock()

	// 3. Fetch dynamically from Gateway API
	models := r.fetchFromGateway(cfg)
	if len(models) == 0 {
		// Dynamic minimal fallback based on current configuration
		defModel := "smart-assistant"
		if cfg != nil && cfg.Hermes.DefaultModel != "" {
			defModel = cfg.Hermes.DefaultModel
		}
		models = []ModelInfo{
			{ID: defModel, Label: FormatModelLabel(defModel)},
		}
	}

	r.mu.Lock()
	r.cached = models
	r.lastFetch = time.Now()
	r.mu.Unlock()

	return ensureCurrentModel(models, currentModel)
}

func (r *ModelResolver) fetchFromGateway(cfg *config.Config) []ModelInfo {
	client := http.Client{Timeout: 1500 * time.Millisecond}
	req, err := http.NewRequest("GET", strings.TrimRight(r.gatewayURL, "/")+"/v1/models", nil)
	if err != nil {
		return nil
	}
	apiKey := r.gatewayKey
	if apiKey == "" && cfg != nil {
		apiKey = cfg.Hermes.GatewayKey
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil
	}

	var primaries []ModelInfo
	seen := make(map[string]bool)

	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true

		// Filter clean models (avoid oc/ or mimo/ duplicate upstream prefixes in primary keyboard)
		if !strings.Contains(id, "/") {
			primaries = append(primaries, ModelInfo{
				ID:    id,
				Label: FormatModelLabel(id),
			})
		}
	}

	// Sort: smart-assistant first, then others alphabetically
	sort.SliceStable(primaries, func(i, j int) bool {
		if primaries[i].ID == "smart-assistant" {
			return true
		}
		if primaries[j].ID == "smart-assistant" {
			return false
		}
		return primaries[i].ID < primaries[j].ID
	})

	// Limit keyboard display to top 8 clean primary models for clean Telegram UI
	if len(primaries) > 8 {
		primaries = primaries[:8]
	}

	return primaries
}

func FormatModelLabel(id string) string {
	lower := strings.ToLower(id)
	if strings.Contains(lower, "smart") {
		return "🚀 " + id + " (Default Combo)"
	}
	if strings.Contains(lower, "gemini") {
		return "⚡ " + id
	}
	if strings.Contains(lower, "nemotron") || strings.Contains(lower, "deepseek") {
		return "🧠 " + id
	}
	if strings.Contains(lower, "free") || strings.Contains(lower, "mimo") {
		return "✨ " + id
	}
	if strings.Contains(lower, "router") {
		return "🔄 " + id
	}
	return "🤖 " + id
}

func ensureCurrentModel(list []ModelInfo, currentModel string) []ModelInfo {
	if currentModel == "" {
		return list
	}
	for _, m := range list {
		if m.ID == currentModel {
			return list
		}
	}
	// Prepend active model if not in default list
	item := ModelInfo{
		ID:    currentModel,
		Label: FormatModelLabel(currentModel) + " (Active)",
	}
	return append([]ModelInfo{item}, list...)
}
