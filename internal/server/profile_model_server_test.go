package server_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// TestUpsertProfileModelServer: modelServer reaches the client with its
// "set" flag only when the body carries it ("" clears), and a value that
// is not an http(s) URL or a whole $VAR is a 400 before the client runs.
func TestUpsertProfileModelServer(t *testing.T) {
	var calls int
	var got server.ProfileDef
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		UpsertProfileFn: func(_ string, def server.ProfileDef, _ string) error {
			calls++
			got = def
			return nil
		},
	}
	srv := server.New(cfg)
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/profiles/local", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}

	w := put(`{"command":"claude","modelServer":" http://127.0.0.1:8000 "}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "http://127.0.0.1:8000", got.ModelServer)
	assert.True(t, got.ModelServerSet)

	w = put(`{"command":"claude","modelServer":"$LOCAL_MODEL_URL"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "$LOCAL_MODEL_URL", got.ModelServer)

	w = put(`{"command":"claude","modelServer":""}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, got.ModelServer)
	assert.True(t, got.ModelServerSet, "an empty value clears")

	w = put(`{"command":"claude"}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.False(t, got.ModelServerSet, "absent keeps the saved value")

	before := calls
	w = put(`{"command":"claude","modelServer":"127.0.0.1:8000"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid_model_server")
	assert.Equal(t, before, calls, "refused before the client runs")
}
