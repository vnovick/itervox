package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// ModelOption represents an available model for a backend.
type ModelOption struct {
	ID    string `json:"id" yaml:"id"`
	Label string `json:"label" yaml:"label"`
}

// DefaultClaudeModels is the hardcoded fallback when API discovery fails.
// Includes all commonly available Claude models so the dropdown is useful
// even without ANTHROPIC_API_KEY for API discovery.
var DefaultClaudeModels = []ModelOption{
	{ID: "claude-haiku-4-5-20251001", Label: "Haiku 4.5 - Fast, cost-effective"},
	{ID: "claude-sonnet-4-5-20251001", Label: "Sonnet 4.5 - Previous gen balanced"},
	{ID: "claude-sonnet-4-6", Label: "Sonnet 4.6 - Balanced"},
	{ID: "claude-opus-4-5-20251001", Label: "Opus 4.5 - Previous gen powerful"},
	{ID: "claude-opus-4-6", Label: "Opus 4.6 - Most powerful"},
}

// DefaultCodexModels is the hardcoded fallback when API discovery fails.
var DefaultCodexModels = []ModelOption{
	{ID: "gpt-5.3-codex", Label: "GPT-5.3-Codex - Frontier coding"},
	{ID: "gpt-5.2-codex", Label: "GPT-5.2-Codex - Long-horizon agentic coding"},
	{ID: "gpt-5.1-codex-max", Label: "GPT-5.1-Codex Max - Deep reasoning"},
	{ID: "gpt-5.1-codex", Label: "GPT-5.1-Codex - Balanced"},
	{ID: "gpt-5.1-codex-mini", Label: "GPT-5.1-Codex Mini - Faster, cheaper"},
	{ID: "gpt-5-codex", Label: "GPT-5-Codex - Stable baseline"},
	{ID: "codex-mini-latest", Label: "codex-mini-latest - Deprecated compatibility alias"},
}

// API endpoint overrides — production code uses the real Anthropic / OpenAI
// URLs; tests override via env vars to point at httptest.Server. This is the
// single seam that lets `itervox models refresh` be exercised end-to-end
// without hitting the real APIs.
const (
	defaultAnthropicAPIBase = "https://api.anthropic.com"
	defaultOpenAIAPIBase    = "https://api.openai.com"
)

func anthropicAPIBase() string {
	if override := os.Getenv("ITERVOX_ANTHROPIC_API_BASE"); override != "" {
		return override
	}
	return defaultAnthropicAPIBase
}

func openAIAPIBase() string {
	if override := os.Getenv("ITERVOX_OPENAI_API_BASE"); override != "" {
		return override
	}
	return defaultOpenAIAPIBase
}

// ListClaudeModels queries the Anthropic API for available models.
// Falls back to DefaultClaudeModels if ANTHROPIC_API_KEY is not set or the API fails.
func ListClaudeModels() []ModelOption {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return DefaultClaudeModels
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anthropicAPIBase()+"/v1/models", nil)
	if err != nil {
		return DefaultClaudeModels
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return DefaultClaudeModels
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Data) == 0 {
		return DefaultClaudeModels
	}

	models := make([]ModelOption, 0, len(result.Data))
	for _, m := range result.Data {
		// Only include Claude models (skip embeddings, etc.)
		if !strings.Contains(m.ID, "claude") {
			continue
		}
		label := m.DisplayName
		if label == "" {
			label = m.ID
		}
		models = append(models, ModelOption{ID: m.ID, Label: label})
	}
	if len(models) == 0 {
		return DefaultClaudeModels
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

// ListCodexModels queries the OpenAI API for available models.
// Falls back to DefaultCodexModels if OPENAI_API_KEY is not set or the API fails.
func ListCodexModels() []ModelOption {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return DefaultCodexModels
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openAIAPIBase()+"/v1/models", nil)
	if err != nil {
		return DefaultCodexModels
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key))

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return DefaultCodexModels
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Data) == 0 {
		return DefaultCodexModels
	}

	models := make([]ModelOption, 0, len(result.Data))
	for _, m := range result.Data {
		// Only include codex-relevant models
		if !strings.Contains(m.ID, "codex") && !strings.Contains(m.ID, "gpt") {
			continue
		}
		models = append(models, ModelOption{ID: m.ID, Label: m.ID})
	}
	if len(models) == 0 {
		return DefaultCodexModels
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}
