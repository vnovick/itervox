package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// ─── outbox surfaces (write-ahead-outbox design, Task 4) ──────────────────

func testServerWithOutboxClient(t *testing.T, retryFn func(string) bool, dropFn func(string)) *server.Server {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{RetryOutboxEntryFn: retryFn, DropOutboxEntryFn: dropFn}
	return server.New(cfg)
}

func TestHandleRetryOutboxEntry_Returns202AndCallsClient(t *testing.T) {
	var gotID string
	srv := testServerWithOutboxClient(t, func(id string) bool {
		gotID = id
		return true
	}, nil)

	w := postJSON(t, srv, "/api/v1/outbox/entry-1/retry", "")

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "entry-1", gotID)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "entry-1", resp["id"])
	assert.Equal(t, true, resp["retried"])
}

func TestHandleRetryOutboxEntry_UnknownIDReturns404(t *testing.T) {
	srv := testServerWithOutboxClient(t, func(string) bool { return false }, nil)

	w := postJSON(t, srv, "/api/v1/outbox/missing/retry", "")

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "outbox_entry_not_found")
}

func TestHandleDropOutboxEntry_Returns202AndCallsClient(t *testing.T) {
	var gotID string
	srv := testServerWithOutboxClient(t, nil, func(id string) {
		gotID = id
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/outbox/entry-1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "entry-1", gotID)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "entry-1", resp["id"])
	assert.Equal(t, true, resp["dropped"])
}

// TestHandleDropOutboxEntry_UnknownIDStillReturns202 verifies the
// idempotent-discard contract (Task 1's Outbox.Drop is a documented no-op
// on an unknown id, not an error): the HTTP layer must not invent a 404 for
// a case the underlying primitive doesn't distinguish.
func TestHandleDropOutboxEntry_UnknownIDStillReturns202(t *testing.T) {
	called := false
	srv := testServerWithOutboxClient(t, nil, func(string) {
		called = true
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/outbox/does-not-exist", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.True(t, called, "client.DropOutboxEntry must still be called for an unknown id")
}
