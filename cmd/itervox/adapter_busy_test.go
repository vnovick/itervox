package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestRealAdapterMapsFullEventChannelTo503 (G7 server half, CORE-005)
// drives the production chain end to end — HTTP handler → the REAL
// orchestratorAdapter → a REAL orchestrator — with no injected sentinel.
// The orchestrator's event loop is not running, so its buffered event
// channel is filled through the real public send path (DismissInput, a bare
// non-blocking send) until the orchestrator itself reports
// orchestrator.ErrBusy. The handler can only answer 503 + Retry-After if
// the adapter translated that into server.ErrBusy; without the translation
// it falls through to 404.
func TestRealAdapterMapsFullEventChannelTo503(t *testing.T) {
	cfg := &config.Config{Tracker: config.TrackerConfig{
		ActiveStates:   []string{"Todo"},
		TerminalStates: []string{"Done"},
	}}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, notify: func() {}}
	srv := server.New(server.Config{
		Snapshot:    func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		Client:      adapter,
		// httptest.NewRequest addresses example.com; no token => CORE-162 Host guard.
		AllowedHosts: []string{"example.com"},
	})
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}

	// While the channel has room the real chain queues and answers 202.
	if w := post("/api/v1/issues/ENG-1/dismiss-input", ""); w.Code != http.StatusAccepted {
		t.Fatalf("dismiss-input with room in the event channel = %d, want 202: %s", w.Code, w.Body)
	}

	// Fill the channel through the real send path.
	full := false
	for range 10_000 {
		if err := orch.DismissInput("ENG-FILL"); err != nil {
			if !errors.Is(err, orchestrator.ErrBusy) {
				t.Fatalf("filling the event channel: unexpected error %v", err)
			}
			full = true
			break
		}
	}
	if !full {
		t.Fatal("event channel never reported full")
	}

	for _, tc := range []struct{ path, body string }{
		{"/api/v1/issues/ENG-1/dismiss-input", ""},
		{"/api/v1/issues/ENG-1/provide-input", `{"message":"retry me"}`},
	} {
		w := post(tc.path, tc.body)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s with a full event channel = %d, want 503: %s", tc.path, w.Code, w.Body)
		}
		if got := w.Header().Get("Retry-After"); got != "1" {
			t.Fatalf("%s: Retry-After = %q, want \"1\"", tc.path, got)
		}
		if !strings.Contains(w.Body.String(), "orchestrator_busy") {
			t.Fatalf("%s: body should carry orchestrator_busy, got %s", tc.path, w.Body)
		}
	}
}
