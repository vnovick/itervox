package server_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/server"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// CORE-008 — DELETE /api/v1/workspaces clears workspaces on a background
// goroutine after answering 202. A panic in the client call must be
// contained (not crash the daemon) and must still publish the task's
// failure outcome: the same "clear all workspaces failed" log line the error
// path emits, with the panic and a stack.
func TestHandleClearAllWorkspaces_PanicIsContainedAndReported(t *testing.T) {
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ClearAllWorkspacesFn: func() error { panic("clear-all fixture panic") },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)

	require.Eventually(t, func() bool {
		return bytes.Contains([]byte(logs.String()), []byte("clear all workspaces failed"))
	}, 5*time.Second, 10*time.Millisecond, "the failure outcome must be published; logs:\n%s", logs.String())
	out := logs.String()
	assert.Contains(t, out, "clear-all fixture panic")
	assert.Contains(t, out, "stack=")
}
