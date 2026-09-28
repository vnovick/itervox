package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestClearAllWorkspacesRejectsConcurrentRequest (CORE-114): DELETE
// /api/v1/workspaces answers 202 and clears in a background goroutine; a
// second DELETE while the first clear is still running used to start another
// goroutine over the same tree. It now answers 409, and a DELETE after the
// first clear finished is accepted again.
func TestClearAllWorkspacesRejectsConcurrentRequest(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	calls := make(chan struct{}, 4)
	srv := newMergeTestServer(t, Config{Client: &FuncClient{ClearAllWorkspacesFn: func() error {
		calls <- struct{}{}
		entered <- struct{}{}
		<-release
		return nil
	}}})
	del := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}

	if w := del(); w.Code != http.StatusAccepted {
		t.Fatalf("first DELETE = %d %s; want 202", w.Code, w.Body.String())
	}
	<-entered
	second := del()
	close(release)
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "clear_in_progress") {
		t.Fatalf("second DELETE while the first runs = %d %s; want 409 clear_in_progress", second.Code, second.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if w := del(); w.Code == http.StatusAccepted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a DELETE after the first clear finished is still refused")
		}
		time.Sleep(10 * time.Millisecond)
	}
	<-entered
	if n := len(calls); n != 2 {
		t.Fatalf("ClearAllWorkspaces ran %d times; want 2 (the refused request must not run it)", n)
	}
}
