package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/server"
)

// refreshClient is a minimal OrchestratorClient + ModelRefresher composite
// used to exercise POST /api/v1/settings/models/refresh end-to-end.
// FuncClient does not embed ModelRefresher (optional interface, discovered
// via type assertion), so the test composes its own scaffold.
type refreshClient struct {
	*server.FuncClient
	refreshFn func(ctx context.Context, backend string) (map[string][]server.ModelOption, error)
}

func (c *refreshClient) RefreshAvailableModels(ctx context.Context, backend string) (map[string][]server.ModelOption, error) {
	return c.refreshFn(ctx, backend)
}

func TestHandleRefreshModels_HappyPath(t *testing.T) {
	called := ""
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &refreshClient{
		FuncClient: &server.FuncClient{},
		refreshFn: func(_ context.Context, backend string) (map[string][]server.ModelOption, error) {
			called = backend
			return map[string][]server.ModelOption{
				"claude": {{ID: "claude-sonnet-4-7", Label: "Sonnet 4.7"}},
				"codex":  {{ID: "gpt-5.4-codex", Label: "GPT-5.4-Codex"}},
			}, nil
		},
	}
	srv := server.New(cfg)

	body, _ := json.Marshal(map[string]string{"backend": "all"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/models/refresh", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "all", called, "refresh fn should receive the requested backend")

	var resp struct {
		OK     bool                            `json:"ok"`
		Models map[string][]server.ModelOption `json:"models"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.OK)
	assert.Equal(t, "claude-sonnet-4-7", resp.Models["claude"][0].ID)
	assert.Equal(t, "gpt-5.4-codex", resp.Models["codex"][0].ID)
}

func TestHandleRefreshModels_DefaultsToAll(t *testing.T) {
	got := ""
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &refreshClient{
		FuncClient: &server.FuncClient{},
		refreshFn: func(_ context.Context, backend string) (map[string][]server.ModelOption, error) {
			got = backend
			return map[string][]server.ModelOption{}, nil
		},
	}
	srv := server.New(cfg)

	// Empty body — handler should default backend to "all".
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/models/refresh", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "all", got)
}

func TestHandleRefreshModels_PropagatesRefresherError(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &refreshClient{
		FuncClient: &server.FuncClient{},
		refreshFn: func(context.Context, string) (map[string][]server.ModelOption, error) {
			return nil, errSimulatedFailure
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/models/refresh",
		bytes.NewReader([]byte(`{"backend":"claude"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "refresh_failed")
}

func TestHandleRefreshModels_NotImplementedWhenClientLacksCapability(t *testing.T) {
	// FuncClient alone does NOT implement ModelRefresher → 501.
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/models/refresh",
		bytes.NewReader([]byte(`{"backend":"all"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotImplemented, w.Code)
	assert.Contains(t, w.Body.String(), "not_implemented")
	assert.Contains(t, w.Body.String(), "itervox models refresh")
}

var errSimulatedFailure = &simulatedError{msg: "anthropic 503 (simulated)"}

type simulatedError struct{ msg string }

func (e *simulatedError) Error() string { return e.msg }
