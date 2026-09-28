package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListClaudeModels_HitsAnthropicEndpoint exercises the real GET path
// against a fake httptest server pointed at via ITERVOX_ANTHROPIC_API_BASE.
// Asserts the headers Anthropic requires AND the response-shape parser.
func TestListClaudeModels_HitsAnthropicEndpoint(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		if r.URL.Path != "/v1/models" {
			t.Errorf("expected GET /v1/models; got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing x-api-key header; got %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic-version header")
		}
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "claude-sonnet-4-7", "display_name": "Sonnet 4.7 - Future"},
				{"id": "claude-opus-4-7",   "display_name": "Opus 4.7 - Future"},
				{"id": "voyage-3",          "display_name": "Voyage embeddings"}
			]
		}`))
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ITERVOX_ANTHROPIC_API_BASE", srv.URL)

	got := ListClaudeModels()
	if !hit {
		t.Fatal("fake Anthropic endpoint was never called")
	}
	if len(got) != 2 {
		t.Errorf("expected 2 claude-named models; got %d: %v", len(got), got)
	}
	ids := map[string]bool{}
	for _, m := range got {
		ids[m.ID] = true
	}
	if !ids["claude-sonnet-4-7"] || !ids["claude-opus-4-7"] {
		t.Errorf("expected sonnet-4-7 + opus-4-7; got %v", got)
	}
	if ids["voyage-3"] {
		t.Error("non-claude IDs must be filtered out")
	}
}

// TestListClaudeModels_APIErrorFallsBackToDefaults — 500 from upstream
// returns the hardcoded catalog, not an empty slice. Protects the
// dashboard model picker from going empty after a transient API outage.
func TestListClaudeModels_APIErrorFallsBackToDefaults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ITERVOX_ANTHROPIC_API_BASE", srv.URL)

	got := ListClaudeModels()
	if len(got) == 0 {
		t.Error("API failure must fall back to defaults, not return empty")
	}
}

// TestListCodexModels_HitsOpenAIEndpoint — codex side uses Bearer auth.
func TestListCodexModels_HitsOpenAIEndpoint(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		if r.URL.Path != "/v1/models" {
			t.Errorf("expected GET /v1/models; got %s %s", r.Method, r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("expected Bearer auth; got %q", auth)
		}
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "gpt-5.4-codex"},
				{"id": "gpt-5.3-codex"},
				{"id": "text-embedding-3-small"}
			]
		}`))
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ITERVOX_OPENAI_API_BASE", srv.URL)

	got := ListCodexModels()
	if !hit {
		t.Fatal("fake OpenAI endpoint was never called")
	}
	ids := map[string]bool{}
	for _, m := range got {
		ids[m.ID] = true
	}
	if !ids["gpt-5.4-codex"] || !ids["gpt-5.3-codex"] {
		t.Errorf("expected gpt-5.4-codex + gpt-5.3-codex; got %v", got)
	}
	if ids["text-embedding-3-small"] {
		t.Errorf("non-codex/gpt IDs must be filtered out; got %v", got)
	}
}

func TestListClaudeModels_FallsBackWithoutKey(t *testing.T) {
	// When ANTHROPIC_API_KEY is not set, should return the default list.
	t.Setenv("ANTHROPIC_API_KEY", "")
	models := ListClaudeModels()
	assert.Equal(t, DefaultClaudeModels, models, "should return default models when no API key")
	assert.True(t, len(models) > 0, "default list should not be empty")
}

func TestListCodexModels_FallsBackWithoutKey(t *testing.T) {
	// When OPENAI_API_KEY is not set, should return the default list.
	t.Setenv("OPENAI_API_KEY", "")
	models := ListCodexModels()
	assert.Equal(t, DefaultCodexModels, models, "should return default models when no API key")
	assert.True(t, len(models) > 0, "default list should not be empty")
}

func TestListClaudeModels_FallsBackOnInvalidKey(t *testing.T) {
	// With an invalid key, the API returns 401 and we fall back to defaults.
	// Point at a fake server rather than the real Anthropic endpoint (CORE-132):
	// without the override this test hit the real internet and passed on ANY
	// failure, network-down included.
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		http.Error(w, `{"error":"invalid_api_key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_API_KEY", "invalid-key-xxx")
	t.Setenv("ITERVOX_ANTHROPIC_API_BASE", srv.URL)

	models := ListClaudeModels()
	if !hit {
		t.Fatal("fake Anthropic endpoint was never called")
	}
	assert.Equal(t, DefaultClaudeModels, models, "should return default models on API failure")
}

func TestListCodexModels_FallsBackOnInvalidKey(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		http.Error(w, `{"error":"invalid_api_key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "invalid-key-xxx")
	t.Setenv("ITERVOX_OPENAI_API_BASE", srv.URL)

	models := ListCodexModels()
	if !hit {
		t.Fatal("fake OpenAI endpoint was never called")
	}
	assert.Equal(t, DefaultCodexModels, models, "should return default models on API failure")
}

func TestDefaultClaudeModels_HasExpectedEntries(t *testing.T) {
	ids := make([]string, len(DefaultClaudeModels))
	for i, m := range DefaultClaudeModels {
		ids[i] = m.ID
	}
	assert.Contains(t, ids, "claude-sonnet-4-6")
	assert.Contains(t, ids, "claude-opus-4-6")
}

func TestDefaultCodexModels_HasExpectedEntries(t *testing.T) {
	ids := make([]string, len(DefaultCodexModels))
	for i, m := range DefaultCodexModels {
		ids[i] = m.ID
	}
	assert.Contains(t, ids, "gpt-5.2-codex")
}

// TestModelOption_JSONFieldNames guards the wire contract with the
// dashboard: web/src/types/schemas.ts's ModelOptionSchema expects lowercase
// "id"/"label" keys (it feeds z.record(z.string(), z.array(ModelOptionSchema))
// on Snapshot.availableModels). The previous version of this test
// (TestModelOption_Fields) only asserted the struct literal back at itself
// and would not have caught a renamed or mis-tagged JSON field.
func TestModelOption_JSONFieldNames(t *testing.T) {
	m := ModelOption{ID: "test-model", Label: "Test Model"}

	raw, err := json.Marshal(m)
	require.NoError(t, err)

	var asMap map[string]any
	require.NoError(t, json.Unmarshal(raw, &asMap))

	assert.Equal(t, "test-model", asMap["id"], "JSON key must be lowercase \"id\" to match web/src/types/schemas.ts's ModelOptionSchema")
	assert.Equal(t, "Test Model", asMap["label"], "JSON key must be lowercase \"label\" to match web/src/types/schemas.ts's ModelOptionSchema")
}
