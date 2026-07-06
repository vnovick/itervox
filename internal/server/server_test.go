package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/server"
)

func baseSnap() server.StateSnapshot {
	return server.StateSnapshot{
		GeneratedAt: time.Now(),
		Running:     []server.RunningRow{},
		Retrying:    []server.RetryRow{},
	}
}

func makeTestConfig(snap server.StateSnapshot) server.Config {
	return server.Config{
		Snapshot:    func() server.StateSnapshot { return snap },
		RefreshChan: make(chan struct{}, 1),
	}
}

func testServer(t *testing.T) *server.Server {
	t.Helper()
	return server.New(makeTestConfig(baseSnap()))
}

func TestStateEndpointReturnsJSON(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, body, "generatedAt")
}

func TestStateEndpointIncludesSupportedAgentActions(t *testing.T) {
	snap := baseSnap()
	snap.SupportedAgentActions = config.SupportedAgentActions()
	srv := server.New(makeTestConfig(snap))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		SupportedAgentActions []string `json:"supportedAgentActions"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, config.SupportedAgentActions(), body.SupportedAgentActions)
}

func TestUnknownRouteReturns404(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nonexistent-route", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRefreshReturns202(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/refresh", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "queued")
}

func TestWrongMethodReturns405(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/state", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestDashboardReturns200HTML(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, strings.HasPrefix(w.Header().Get("Content-Type"), "text/html"))
}

func TestStateEndpointShape(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, body, "generatedAt")
	assert.Contains(t, body, "running")
	assert.Contains(t, body, "retrying")
	assert.Contains(t, body, "counts")
}

// postJSON is a helper that sends a POST with a JSON body and returns the recorder.
func postJSON(t *testing.T, srv *server.Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// patchJSON is a helper that sends a PATCH with a JSON body and returns the recorder.
func patchJSON(t *testing.T, srv *server.Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestSetWorkers(t *testing.T) {
	tests := []struct {
		name           string
		currentWorkers int
		body           string
		wantStatus     int
		wantWorkers    int
	}{
		{
			name:           "absolute value",
			currentWorkers: 1,
			body:           `{"workers":5}`,
			wantStatus:     http.StatusOK,
			wantWorkers:    5,
		},
		{
			name:           "delta positive",
			currentWorkers: 3,
			body:           `{"delta":2}`,
			wantStatus:     http.StatusOK,
			wantWorkers:    5,
		},
		{
			name:           "delta clamps to 1",
			currentWorkers: 2,
			body:           `{"delta":-100}`,
			wantStatus:     http.StatusOK,
			wantWorkers:    1,
		},
		{
			name:           "absolute clamps to 50",
			currentWorkers: 1,
			body:           `{"workers":100}`,
			wantStatus:     http.StatusOK,
			wantWorkers:    50,
		},
		{
			name:           "invalid JSON returns 400",
			currentWorkers: 1,
			body:           `not-json`,
			wantStatus:     http.StatusBadRequest,
			wantWorkers:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var called int
			snap := baseSnap()
			snap.MaxConcurrentAgents = tc.currentWorkers
			cfg := makeTestConfig(snap)
			cfg.Client = &server.FuncClient{
				SetWorkersFn: func(n int) error { called = n; return nil },
				BumpWorkersFn: func(delta int) (int, error) {
					next := snap.MaxConcurrentAgents + delta
					if next < 1 {
						next = 1
					}
					if next > 50 {
						next = 50
					}
					called = next
					return next, nil
				},
			}
			srv := server.New(cfg)

			w := postJSON(t, srv, "/api/v1/settings/workers", tc.body)
			assert.Equal(t, tc.wantStatus, w.Code)

			if tc.wantStatus == http.StatusOK {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, float64(tc.wantWorkers), resp["workers"])
				assert.Equal(t, tc.wantWorkers, called)
			} else {
				assert.Contains(t, w.Body.String(), "error")
			}
		})
	}

	// Persist failure must surface as 500 — the frontend toast plumbing
	// keys off non-2xx to show an error. T-05 added the error return so
	// SetWorkers can no longer silently revert at the next reload.
	t.Run("persist failure returns 500", func(t *testing.T) {
		snap := baseSnap()
		snap.MaxConcurrentAgents = 3
		cfg := makeTestConfig(snap)
		cfg.Client = &server.FuncClient{
			SetWorkersFn: func(int) error {
				return errors.New("disk full")
			},
		}
		srv := server.New(cfg)

		w := postJSON(t, srv, "/api/v1/settings/workers", `{"workers":5}`)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Contains(t, w.Body.String(), "disk full")
	})

	t.Run("bump persist failure returns 500", func(t *testing.T) {
		snap := baseSnap()
		snap.MaxConcurrentAgents = 3
		cfg := makeTestConfig(snap)
		cfg.Client = &server.FuncClient{
			BumpWorkersFn: func(int) (int, error) {
				return 0, errors.New("disk full")
			},
		}
		srv := server.New(cfg)

		w := postJSON(t, srv, "/api/v1/settings/workers", `{"delta":1}`)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Contains(t, w.Body.String(), "disk full")
	})
}

func TestSetIssueProfile(t *testing.T) {
	tests := []struct {
		name           string
		identifier     string
		body           string
		wantStatus     int
		wantProfile    string
		wantIdentifier string
	}{
		{
			name:           "set profile",
			identifier:     "ENG-1",
			body:           `{"profile":"fast"}`,
			wantStatus:     http.StatusOK,
			wantProfile:    "fast",
			wantIdentifier: "ENG-1",
		},
		{
			name:           "clear profile",
			identifier:     "ENG-1",
			body:           `{"profile":""}`,
			wantStatus:     http.StatusOK,
			wantProfile:    "",
			wantIdentifier: "ENG-1",
		},
		{
			name:       "invalid JSON returns 400",
			identifier: "ENG-1",
			body:       `not-json`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotIdentifier, gotProfile string
			cfg := makeTestConfig(baseSnap())
			cfg.Client = &server.FuncClient{
				SetIssueProfileFn: func(identifier, profile string) {
					gotIdentifier = identifier
					gotProfile = profile
				},
			}
			srv := server.New(cfg)

			path := "/api/v1/issues/" + tc.identifier + "/profile"
			w := postJSON(t, srv, path, tc.body)
			assert.Equal(t, tc.wantStatus, w.Code)

			if tc.wantStatus == http.StatusOK {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, true, resp["ok"])
				assert.Equal(t, tc.wantIdentifier, resp["identifier"])
				assert.Equal(t, tc.wantProfile, resp["profile"])
				assert.Equal(t, tc.wantIdentifier, gotIdentifier)
				assert.Equal(t, tc.wantProfile, gotProfile)
			} else {
				assert.Contains(t, w.Body.String(), "error")
			}
		})
	}
}

func TestUpsertProfileIncludesBackend(t *testing.T) {
	var gotName string
	var gotDef server.ProfileDef
	var gotOriginalName string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		UpsertProfileFn: func(name string, def server.ProfileDef, originalName string) error {
			gotName = name
			gotDef = def
			gotOriginalName = originalName
			return nil
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/profiles/codex-fast", bytes.NewBufferString(`{"command":"run-codex-wrapper","prompt":"fast path","soul":"Soul body","instructions":"Instructions body","soulFile":".itervox/agents/codex-fast/SOUL.md","instructionsFile":".itervox/agents/codex-fast/INSTRUCTIONS.md","backend":"codex","enabled":false,"allowedActions":["comment","provide_input"],"originalName":"legacy-fast"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "codex-fast", gotName)
	assert.Equal(t, "run-codex-wrapper", gotDef.Command)
	assert.Equal(t, "fast path", gotDef.Prompt)
	assert.Equal(t, "Soul body", gotDef.Soul)
	assert.Equal(t, "Instructions body", gotDef.Instructions)
	assert.True(t, gotDef.SoulSet)
	assert.True(t, gotDef.InstructionsSet)
	assert.Equal(t, ".itervox/agents/codex-fast/SOUL.md", gotDef.SoulFile)
	assert.Equal(t, ".itervox/agents/codex-fast/INSTRUCTIONS.md", gotDef.InstructionsFile)
	assert.Equal(t, "codex", gotDef.Backend)
	assert.False(t, gotDef.Enabled)
	assert.Equal(t, []string{"comment", "provide_input"}, gotDef.AllowedActions)
	assert.Equal(t, "legacy-fast", gotOriginalName)
}

func TestUpsertProfilePreservesSubmittedEmptyProfileFileFields(t *testing.T) {
	var gotDef server.ProfileDef
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		UpsertProfileFn: func(_ string, def server.ProfileDef, _ string) error {
			gotDef = def
			return nil
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/profiles/qa", bytes.NewBufferString(`{"command":"claude","soul":"","instructions":"","enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, gotDef.Soul)
	assert.Empty(t, gotDef.Instructions)
	assert.True(t, gotDef.SoulSet)
	assert.True(t, gotDef.InstructionsSet)
}

func TestUpdateIssueState(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		body       string
		updaterErr error
		wantStatus int
	}{
		{
			name:       "success",
			identifier: "ENG-1",
			body:       `{"state":"In Progress"}`,
			updaterErr: nil,
			wantStatus: http.StatusOK,
		},
		{
			name:       "updater returns error gives 500",
			identifier: "ENG-1",
			body:       `{"state":"Done"}`,
			updaterErr: errors.New("tracker unavailable"),
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "invalid JSON returns 400",
			identifier: "ENG-1",
			body:       `not-json`,
			updaterErr: nil,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing state field returns 400",
			identifier: "ENG-1",
			body:       `{}`,
			updaterErr: nil,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotSource string
			cfg := makeTestConfig(baseSnap())
			cfg.Client = &server.FuncClient{
				UpdateIssueStateFn: func(ctx context.Context, identifier, stateName string) error {
					gotSource = server.IssueStatusSource(ctx)
					return tc.updaterErr
				},
			}
			srv := server.New(cfg)

			path := "/api/v1/issues/" + tc.identifier + "/state"
			w := patchJSON(t, srv, path, tc.body)
			assert.Equal(t, tc.wantStatus, w.Code)

			if tc.wantStatus == http.StatusOK {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, true, resp["ok"])
				assert.Equal(t, server.IssueStatusSourceDashboard, gotSource)
			} else {
				assert.Contains(t, w.Body.String(), "error")
			}
		})
	}
}

func TestTrackerIssueIncludesStatusChangesJSON(t *testing.T) {
	issue := server.TrackerIssue{
		Identifier:        "ENG-1",
		Title:             "Track status",
		State:             "Done",
		OrchestratorState: "idle",
		StatusChanges: []server.IssueStatusChangeRow{{
			FromState:    "Todo",
			ToState:      "Done",
			Source:       "dashboard",
			AutomationID: "auto-1",
			At:           time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC),
		}},
	}

	data, err := json.Marshal(issue)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"statusChanges"`)
	assert.Contains(t, string(data), `"fromState":"Todo"`)
	assert.Contains(t, string(data), `"source":"dashboard"`)
	assert.Contains(t, string(data), `"automationId":"auto-1"`)
}

// ─── handleIssues ─────────────────────────────────────────────────────────────

func testServerWithFetchIssues(t *testing.T, fn func(ctx context.Context) ([]server.TrackerIssue, error)) *server.Server {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{FetchIssuesFn: fn}
	return server.New(cfg)
}

func TestHandleIssues_ReturnsJSONArray(t *testing.T) {
	issues := []server.TrackerIssue{
		{Identifier: "ENG-1", Title: "Fix bug", State: "In Progress"},
		{Identifier: "ENG-2", Title: "Add feature", State: "Todo"},
	}
	srv := testServerWithFetchIssues(t, func(_ context.Context) ([]server.TrackerIssue, error) {
		return issues, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")

	var got []server.TrackerIssue
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, "ENG-1", got[0].Identifier)
	assert.Equal(t, "ENG-2", got[1].Identifier)
}

func TestHandleIssues_FetchError_Returns500(t *testing.T) {
	srv := testServerWithFetchIssues(t, func(_ context.Context) ([]server.TrackerIssue, error) {
		return nil, errors.New("tracker down")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "error")
}

// ─── handleIssueDetail ────────────────────────────────────────────────────────

func TestHandleIssueDetail_Found_Returns200(t *testing.T) {
	issues := []server.TrackerIssue{
		{Identifier: "ENG-10", Title: "My issue", State: "Todo"},
	}
	srv := testServerWithFetchIssues(t, func(_ context.Context) ([]server.TrackerIssue, error) {
		return issues, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-10", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var got server.TrackerIssue
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "ENG-10", got.Identifier)
	assert.Equal(t, "My issue", got.Title)
}

func TestHandleIssueDetail_NotFound_Returns404(t *testing.T) {
	srv := testServerWithFetchIssues(t, func(_ context.Context) ([]server.TrackerIssue, error) {
		return []server.TrackerIssue{}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-999", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "not_found")
}

func TestHandleIssueDetail_FetchError_Returns500(t *testing.T) {
	srv := testServerWithFetchIssues(t, func(_ context.Context) ([]server.TrackerIssue, error) {
		return nil, errors.New("db error")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── handleCancelIssue ────────────────────────────────────────────────────────

func testServerWithCancel(t *testing.T, fn func(string) bool) *server.Server {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{CancelIssueFn: fn}
	return server.New(cfg)
}

func TestHandleCancelIssue_Found_Returns200(t *testing.T) {
	srv := testServerWithCancel(t, func(id string) bool { return id == "ENG-1" })
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/issues/ENG-1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["cancelled"])
	assert.Equal(t, "ENG-1", resp["identifier"])
}

func TestHandleCancelIssue_NotFound_Returns404(t *testing.T) {
	srv := testServerWithCancel(t, func(id string) bool { return false })
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/issues/ENG-999", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "not_running")
}

// ─── handleResumeIssue ────────────────────────────────────────────────────────

func testServerWithResume(t *testing.T, fn func(string) bool) *server.Server {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{ResumeIssueFn: fn}
	return server.New(cfg)
}

func TestHandleResumeIssue_Found_Returns200(t *testing.T) {
	srv := testServerWithResume(t, func(id string) bool { return id == "ENG-5" })
	w := postJSON(t, srv, "/api/v1/issues/ENG-5/resume", "")

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["resumed"])
	assert.Equal(t, "ENG-5", resp["identifier"])
}

func TestHandleResumeIssue_NotPaused_Returns404(t *testing.T) {
	srv := testServerWithResume(t, func(id string) bool { return false })
	w := postJSON(t, srv, "/api/v1/issues/ENG-5/resume", "")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "not_paused")
}

// ─── handleTerminateIssue ─────────────────────────────────────────────────────

func testServerWithTerminate(t *testing.T, fn func(string) bool) *server.Server {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{TerminateIssueFn: fn}
	return server.New(cfg)
}

func putJSON(t *testing.T, srv *server.Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestHandleTerminateIssue_Success(t *testing.T) {
	var got string
	srv := testServerWithTerminate(t, func(id string) bool { got = id; return true })
	w := postJSON(t, srv, "/api/v1/issues/ENG-5/terminate", "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ENG-5", got)
}

func TestHandleTerminateIssue_NotFound(t *testing.T) {
	srv := testServerWithTerminate(t, func(string) bool { return false })
	w := postJSON(t, srv, "/api/v1/issues/ENG-X/terminate", "")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ─── handleReanalyzeIssue ─────────────────────────────────────────────────────

func testServerWithReanalyze(t *testing.T, fn func(string) bool) *server.Server {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{ReanalyzeIssueFn: fn}
	return server.New(cfg)
}

func TestHandleReanalyzeIssue_Success(t *testing.T) {
	var got string
	srv := testServerWithReanalyze(t, func(id string) bool { got = id; return true })
	w := postJSON(t, srv, "/api/v1/issues/ENG-7/reanalyze", "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ENG-7", got)
	assert.Contains(t, w.Body.String(), "queued")
}

func TestHandleReanalyzeIssue_NotPaused(t *testing.T) {
	srv := testServerWithReanalyze(t, func(string) bool { return false })
	w := postJSON(t, srv, "/api/v1/issues/ENG-7/reanalyze", "")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "not_paused")
}

// ─── handleListProfiles / handleDeleteProfile ─────────────────────────────────

func testServerWithProfiles(t *testing.T) (*server.Server, *map[string]server.ProfileDef) {
	t.Helper()
	defs := map[string]server.ProfileDef{
		"fast": {Command: "codex", Backend: "codex", AllowedActions: []string{"comment"}},
	}
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ProfileDefsFn:   func() map[string]server.ProfileDef { return defs },
		DeleteProfileFn: func(name string) error { delete(defs, name); return nil },
	}
	return server.New(cfg), &defs
}

func TestHandleListProfiles_ReturnsProfiles(t *testing.T) {
	srv, _ := testServerWithProfiles(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/profiles", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "fast")
	assert.Contains(t, w.Body.String(), "allowedActions")
}

func TestHandleDeleteProfile_Success(t *testing.T) {
	srv, defs := testServerWithProfiles(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/profiles/fast", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, *defs, "fast")
}

// ─── handleListModels ──────────────────────────────────────────────────────────

func TestHandleListModels_ReturnsModels(t *testing.T) {
	models := map[string][]server.ModelOption{
		"claude": {{ID: "claude-sonnet-4-6", Label: "Sonnet 4.6"}, {ID: "claude-opus-4-6", Label: "Opus 4.6"}},
		"codex":  {{ID: "gpt-5.2-codex", Label: "GPT-5.2 Codex"}},
	}
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		AvailableModelsFn: func() map[string][]server.ModelOption { return models },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/models", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "claude-sonnet-4-6")
	assert.Contains(t, w.Body.String(), "gpt-5.2-codex")
	assert.Contains(t, w.Body.String(), "Sonnet 4.6")
}

func TestHandleListModels_EmptyReturnsEmptyObject(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/models", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "{}")
}

// ─── handleGetReviewer / handleSetReviewer ─────────────────────────────────────

func TestHandleGetReviewer_ReturnsConfig(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ReviewerConfigFn: func() (string, bool) { return "code-reviewer", true },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/reviewer", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "code-reviewer")
	assert.Contains(t, w.Body.String(), "true")
}

func TestHandleSetReviewer_UpdatesConfig(t *testing.T) {
	var savedProfile string
	var savedAutoReview bool
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetReviewerConfigFn: func(profile string, autoReview bool) error {
			savedProfile = profile
			savedAutoReview = autoReview
			return nil
		},
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/reviewer",
		bytes.NewBufferString(`{"profile":"reviewer","auto_review":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "reviewer", savedProfile)
	assert.True(t, savedAutoReview)
}

func TestHandleSetReviewer_DisableReviewer(t *testing.T) {
	var savedProfile string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetReviewerConfigFn: func(profile string, autoReview bool) error {
			savedProfile = profile
			return nil
		},
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/reviewer",
		bytes.NewBufferString(`{"profile":"","auto_review":false}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "", savedProfile)
}

func TestHandleSetReviewer_InvalidAutoClearCombinationReturns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetReviewerConfigFn: func(string, bool) error {
			return config.ErrAutoClearAutoReviewConflict
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/reviewer", `{"profile":"reviewer","auto_review":true}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "auto_clear")
}

func TestHandleSetReviewer_MissingReviewerProfileReturns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetReviewerConfigFn: func(string, bool) error {
			return config.ErrAutoReviewRequiresReviewerProfile
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/reviewer", `{"profile":"","auto_review":true}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "reviewer_profile")
}

func TestHandleSetReviewer_InvalidProfileReturns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetReviewerConfigFn: func(string, bool) error {
			return config.ErrReviewerProfileNotFound
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/reviewer", `{"profile":"missing","auto_review":false}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid_profile")
}

// ─── handleListProjects / handleGetProjectFilter / handleSetProjectFilter ─────

type fakeProjectManager struct {
	projects []server.Project
	filter   []string
}

func (f *fakeProjectManager) FetchProjects(_ context.Context) ([]server.Project, error) {
	return f.projects, nil
}
func (f *fakeProjectManager) GetProjectFilter() []string  { return f.filter }
func (f *fakeProjectManager) SetProjectFilter(s []string) { f.filter = s }

func testServerWithProjects(t *testing.T) (*server.Server, *fakeProjectManager) {
	t.Helper()
	pm := &fakeProjectManager{projects: []server.Project{{Name: "Alpha"}}}
	cfg := makeTestConfig(baseSnap())
	cfg.ProjectManager = pm
	return server.New(cfg), pm
}

func TestHandleListProjects_NotConfigured(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

func TestHandleListProjects_ReturnsProjects(t *testing.T) {
	srv, _ := testServerWithProjects(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Alpha")
}

func TestHandleGetProjectFilter_ReturnsFilter(t *testing.T) {
	srv, pm := testServerWithProjects(t)
	pm.filter = []string{"proj-1"}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/filter", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "proj-1")
}

func TestHandleSetProjectFilter_SetsSlugs(t *testing.T) {
	srv, pm := testServerWithProjects(t)
	w := putJSON(t, srv, "/api/v1/projects/filter", `{"slugs":["s1","s2"]}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"s1", "s2"}, pm.filter)
}

func TestHandleSetProjectFilter_NullSlugsResetsFilter(t *testing.T) {
	srv, pm := testServerWithProjects(t)
	pm.filter = []string{"old"}
	w := putJSON(t, srv, "/api/v1/projects/filter", `{}`)
	assert.Equal(t, http.StatusOK, w.Code)
	// nil slugs = reset to WORKFLOW.md default (nil)
	assert.Nil(t, pm.filter)
}

// ─── handleUpdateTrackerStates ────────────────────────────────────────────────

func TestHandleUpdateTrackerStates_Success(t *testing.T) {
	var gotActive, gotTerminal []string
	var gotCompletion string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		UpdateTrackerStatesFn: func(active, terminal []string, completion string) error {
			gotActive = active
			gotTerminal = terminal
			gotCompletion = completion
			return nil
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/tracker/states",
		`{"activeStates":["Todo","In Progress"],"terminalStates":["Done"],"completionState":"Done"}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"Todo", "In Progress"}, gotActive)
	assert.Equal(t, []string{"Done"}, gotTerminal)
	assert.Equal(t, "Done", gotCompletion)
}

func TestHandleUpdateTrackerStates_InvalidJSON(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{UpdateTrackerStatesFn: func(_, _ []string, _ string) error { return nil }}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/tracker/states", `{bad json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ─── Notify / broadcaster ─────────────────────────────────────────────────────

func TestNotifyDoesNotPanicWithNoSubscribers(t *testing.T) {
	srv := testServer(t)
	// Must not panic; no-op when broadcaster has no subscribers.
	assert.NotPanics(t, func() {
		srv.Notify()
	})
}

// ─── handleIssueLogs / handleClearIssueLogs ───────────────────────────────────

func testServerWithIssueLogs(t *testing.T, fetchLogs func(string) []string) *server.Server {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{FetchLogsFn: fetchLogs}
	return server.New(cfg)
}

func TestHandleIssueLogs_ReturnsEntries(t *testing.T) {
	srv := testServerWithIssueLogs(t, func(id string) []string {
		return []string{`{"level":"INFO","msg":"claude: text","time":"10:00:00","text":"something happened"}`}
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/logs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var entries []any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &entries))
	// Non-skipped lines get parsed.
	assert.NotEmpty(t, entries)
}

func TestHandleIssueLogs_EmptyLogs(t *testing.T) {
	srv := testServerWithIssueLogs(t, func(string) []string { return nil })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/logs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w.Body.String()))
}

func TestHandleClearIssueLogs_Success(t *testing.T) {
	var cleared string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{ClearLogsFn: func(id string) error { cleared = id; return nil }}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/issues/ENG-9/logs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ENG-9", cleared)
}

// ─── handleAIReview ───────────────────────────────────────────────────────────

func TestHandleAIReview_Success(t *testing.T) {
	var got string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{DispatchReviewerFn: func(id string) error { got = id; return nil }}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/issues/ENG-42/ai-review", "")
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "ENG-42", got)
}

func TestHandleAIReview_DispatchError(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{DispatchReviewerFn: func(string) error { return errors.New("reviewer busy") }}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/issues/ENG-1/ai-review", "")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleSetIssueBackend(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		body       string
		wantCode   int
	}{
		{"set codex", "PROJ-1", `{"backend":"codex"}`, 200},
		{"set claude", "PROJ-1", `{"backend":"claude"}`, 200},
		{"clear", "PROJ-1", `{"backend":""}`, 200},
		{"bad json", "PROJ-1", `{invalid`, 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := baseSnap()
			srv := server.New(makeTestConfig(snap))
			path := "/api/v1/issues/" + tc.identifier + "/backend"
			req := httptest.NewRequest("POST", path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != tc.wantCode {
				t.Errorf("got %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}

// ─── handleHealth ────────────────────────────────────────────────────────────

func TestHandleHealth_Returns200(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestHandleHealth_NoAuthRequired(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.APIToken = "secret-token"
	srv := server.New(cfg)

	// Health endpoint should succeed WITHOUT a bearer token.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}

// ─── Bearer auth middleware ──────────────────────────────────────────────────

func TestBearerAuth_MissingToken_Returns401(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.APIToken = "my-secret"
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "unauthorized")
}

func TestBearerAuth_WrongToken_Returns401(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.APIToken = "my-secret"
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestBearerAuth_CorrectToken_Returns200(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.APIToken = "my-secret"
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	req.Header.Set("Authorization", "Bearer my-secret")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestBearerAuth_MissingBearerPrefix_Returns401(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.APIToken = "my-secret"
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	req.Header.Set("Authorization", "my-secret") // no "Bearer " prefix
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ─── SSE endpoint ────────────────────────────────────────────────────────────

func TestHandleEvents_ReturnsSSEContentType(t *testing.T) {
	srv := testServer(t)
	// Create a request with a cancellable context so the SSE handler returns.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so handler exits after initial event
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), "data:")
}

// ─── handleIssueDetail via FetchIssue fast path ──────────────────────────────

func TestHandleIssueDetail_FetchIssueFastPath_Found(t *testing.T) {
	issue := &server.TrackerIssue{Identifier: "ENG-42", Title: "Fast path", State: "Done"}
	cfg := makeTestConfig(baseSnap())
	cfg.FetchIssue = func(_ context.Context, id string) (*server.TrackerIssue, error) {
		if id == "ENG-42" {
			return issue, nil
		}
		return nil, nil
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-42", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ENG-42")
	assert.Contains(t, w.Body.String(), "Fast path")
}

func TestHandleIssueDetail_FetchIssueFastPath_NotFound(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.FetchIssue = func(_ context.Context, id string) (*server.TrackerIssue, error) {
		return nil, nil
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/NOPE-1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "not_found")
}

func TestHandleIssueDetail_FetchIssueFastPath_Error(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.FetchIssue = func(_ context.Context, id string) (*server.TrackerIssue, error) {
		return nil, errors.New("tracker timeout")
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "fetch_failed")
}

// ─── handleSetInlineInput ────────────────────────────────────────────────────

func TestHandleSetInlineInput_InvalidJSON_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/inline-input", `not-json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSetInlineInput_NoopClient_Returns500(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{} // SetInlineInput returns errNotConfigured
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/inline-input", `{"enabled":true}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleSetInlineInput_Success(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	called := false
	cfg.Client = &server.FuncClient{
		SetInlineInputFn: func(enabled bool) error {
			called = true
			assert.True(t, enabled)
			return nil
		},
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/inline-input", `{"enabled":true}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, called)
}

// ─── handleSetDispatchStrategy ───────────────────────────────────────────────

func TestHandleSetDispatchStrategy_ValidStrategies(t *testing.T) {
	tests := []struct {
		strategy string
		wantCode int
	}{
		{"round-robin", http.StatusOK},
		{"least-loaded", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.strategy, func(t *testing.T) {
			cfg := makeTestConfig(baseSnap())
			cfg.Client = &server.FuncClient{
				SetDispatchStrategyFn: func(s string) error { return nil },
			}
			srv := server.New(cfg)
			w := putJSON(t, srv, "/api/v1/settings/dispatch-strategy", `{"strategy":"`+tc.strategy+`"}`)
			assert.Equal(t, tc.wantCode, w.Code)
		})
	}
}

func TestHandleSetDispatchStrategy_InvalidStrategy_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetDispatchStrategyFn: func(s string) error { return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/dispatch-strategy", `{"strategy":"random"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "bad_request")
}

func TestHandleSetDispatchStrategy_InvalidJSON_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetDispatchStrategyFn: func(s string) error { return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/dispatch-strategy", `not-json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSetDispatchStrategy_ServerError(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetDispatchStrategyFn: func(s string) error { return errors.New("disk full") },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/dispatch-strategy", `{"strategy":"round-robin"}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── handleSetAutoClearWorkspace ─────────────────────────────────────────────

func TestHandleSetAutoClearWorkspace_Enable(t *testing.T) {
	var gotVal bool
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetAutoClearWorkspaceFn: func(enabled bool) error { gotVal = enabled; return nil },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/workspace/auto-clear", `{"enabled":true}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, gotVal)
}

func TestHandleSetAutoClearWorkspace_Disable(t *testing.T) {
	var gotVal bool
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetAutoClearWorkspaceFn: func(enabled bool) error { gotVal = enabled; return nil },
	}
	srv := server.New(cfg)
	gotVal = true // ensure it gets set to false
	w := postJSON(t, srv, "/api/v1/settings/workspace/auto-clear", `{"enabled":false}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.False(t, gotVal)
}

func TestHandleSetAutoClearWorkspace_MissingEnabled_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetAutoClearWorkspaceFn: func(bool) error { return nil },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/workspace/auto-clear", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "enabled field is required")
}

func TestHandleSetAutoClearWorkspace_InvalidJSON_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetAutoClearWorkspaceFn: func(bool) error { return nil },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/workspace/auto-clear", `not-json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSetAutoClearWorkspace_ServerError(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetAutoClearWorkspaceFn: func(bool) error { return errors.New("write failed") },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/workspace/auto-clear", `{"enabled":true}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleSetAutoClearWorkspace_InvalidReviewerCombinationReturns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetAutoClearWorkspaceFn: func(bool) error { return config.ErrAutoClearAutoReviewConflict },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/workspace/auto-clear", `{"enabled":true}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "auto_review")
}

// ─── handleClearAllWorkspaces ────────────────────────────────────────────────

func TestHandleClearAllWorkspaces_Returns202(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ClearAllWorkspacesFn: func() error { return nil },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}

// ─── handleAddSSHHost / handleRemoveSSHHost ──────────────────────────────────

func TestHandleAddSSHHost_Success(t *testing.T) {
	var gotHost, gotDesc string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		AddSSHHostFn: func(host, desc string) error { gotHost = host; gotDesc = desc; return nil },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/ssh-hosts", `{"host":"worker-1","description":"fast box"}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "worker-1", gotHost)
	assert.Equal(t, "fast box", gotDesc)
}

func TestHandleAddSSHHost_EmptyHost_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		AddSSHHostFn: func(string, string) error { return nil },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/ssh-hosts", `{"host":"","description":"no host"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleAddSSHHost_WhitespaceHost_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		AddSSHHostFn: func(string, string) error { return nil },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/ssh-hosts", `{"host":"   ","description":"spaces"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleAddSSHHost_InvalidJSON_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		AddSSHHostFn: func(string, string) error { return nil },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/ssh-hosts", `not-json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleAddSSHHost_ServerError(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		AddSSHHostFn: func(string, string) error { return errors.New("duplicate host") },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/settings/ssh-hosts", `{"host":"w1"}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleRemoveSSHHost_Success(t *testing.T) {
	var gotHost string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		RemoveSSHHostFn: func(host string) error { gotHost = host; return nil },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/ssh-hosts/worker-1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "worker-1", gotHost)
}

func TestHandleRemoveSSHHost_ServerError(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		RemoveSSHHostFn: func(string) error { return errors.New("not found") },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/ssh-hosts/nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── handleSubLogs ───────────────────────────────────────────────────────────

func TestHandleSubLogs_ReturnsEntries(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		FetchSubLogsFn: func(_ context.Context, id string) ([]domain.IssueLogEntry, error) {
			return []domain.IssueLogEntry{{Event: "text", Message: "hello"}}, nil
		},
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/sublogs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "hello")
}

func TestHandleSubLogs_EmptyReturnsEmptyArray(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		FetchSubLogsFn: func(context.Context, string) ([]domain.IssueLogEntry, error) { return nil, nil },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/sublogs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w.Body.String()))
}

func TestHandleSubLogs_Error_Returns500(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		FetchSubLogsFn: func(context.Context, string) ([]domain.IssueLogEntry, error) { return nil, errors.New("io error") },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/sublogs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleSubLogs_UsesRequestContext(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg.Client = &server.FuncClient{
		FetchSubLogsFn: func(ctx context.Context, _ string) ([]domain.IssueLogEntry, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				t.Fatal("FetchSubLogs did not receive the request context")
				return nil, nil
			}
		},
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/sublogs", nil).WithContext(reqCtx)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestHandleSubLogStream_ResumesFromLastEventID pins the T-18 contract: a
// reconnect carrying Last-Event-ID: N causes the server to skip the first
// N entries and stream only events N+1..end. Without this, every reconnect
// re-delivers the entire buffer (duplicate-line spam in the dashboard).
func TestHandleSubLogStream_ResumesFromLastEventID(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	ctx, cancel := context.WithCancel(context.Background())
	entries := []domain.IssueLogEntry{
		{Event: "text", Message: "one"},
		{Event: "text", Message: "two"},
		{Event: "text", Message: "three"},
		{Event: "text", Message: "four"},
		{Event: "text", Message: "five"},
	}
	cfg.Client = &server.FuncClient{
		FetchSubLogsFn: func(context.Context, string) ([]domain.IssueLogEntry, error) {
			defer cancel()
			return entries, nil
		},
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/sublog-stream", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "3")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	body := w.Body.String()
	assert.NotContains(t, body, `"message":"one"`, "events 1-3 must be skipped after Last-Event-ID: 3")
	assert.NotContains(t, body, `"message":"two"`)
	assert.NotContains(t, body, `"message":"three"`)
	assert.Contains(t, body, `"message":"four"`)
	assert.Contains(t, body, `"message":"five"`)
	// Server stamps each emitted event with id: <cursor>.
	assert.Contains(t, body, "id: 4")
	assert.Contains(t, body, "id: 5")
}

// TestHandleSubLogStream_StaleLastEventIDReplaysFromStart guards against a
// stale cursor pointing past the current buffer (e.g. server restart): the
// server must reset to 0 and replay everything, not silently drop events.
func TestHandleSubLogStream_StaleLastEventIDReplaysFromStart(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	ctx, cancel := context.WithCancel(context.Background())
	cfg.Client = &server.FuncClient{
		FetchSubLogsFn: func(context.Context, string) ([]domain.IssueLogEntry, error) {
			defer cancel()
			return []domain.IssueLogEntry{{Event: "text", Message: "first"}}, nil
		},
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/sublog-stream", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "999")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Contains(t, w.Body.String(), `"message":"first"`)
}

func TestHandleSubLogStream_StreamsInitialEntries(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	ctx, cancel := context.WithCancel(context.Background())
	cfg.Client = &server.FuncClient{
		FetchSubLogsFn: func(context.Context, string) ([]domain.IssueLogEntry, error) {
			defer cancel()
			return []domain.IssueLogEntry{{Event: "text", Message: "hello"}}, nil
		},
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/sublog-stream", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), "event: sublog")
	assert.Contains(t, w.Body.String(), "hello")
}

// ─── handleClearAllLogs ──────────────────────────────────────────────────────

func TestHandleClearAllLogs_Success(t *testing.T) {
	called := false
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{ClearAllLogsFn: func() error { called = true; return nil }}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/logs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, called)
}

func TestHandleClearAllLogs_Error(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{ClearAllLogsFn: func() error { return errors.New("rm failed") }}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/logs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── handleClearIssueSubLogs ─────────────────────────────────────────────────

func TestHandleClearIssueSubLogs_Success(t *testing.T) {
	var gotID string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{ClearIssueSubLogsFn: func(id string) error { gotID = id; return nil }}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/issues/ENG-5/sublogs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ENG-5", gotID)
}

func TestHandleClearIssueSubLogs_Error(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{ClearIssueSubLogsFn: func(string) error { return errors.New("fail") }}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/issues/ENG-5/sublogs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── handleClearSessionSublog ────────────────────────────────────────────────

func TestHandleClearSessionSublog_Success(t *testing.T) {
	var gotID, gotSession string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ClearSessionSublogFn: func(id, sess string) error { gotID = id; gotSession = sess; return nil },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/issues/ENG-3/sublogs/sess-abc", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ENG-3", gotID)
	assert.Equal(t, "sess-abc", gotSession)
}

func TestHandleClearSessionSublog_Error(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ClearSessionSublogFn: func(string, string) error { return errors.New("not found") },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/issues/ENG-3/sublogs/sess-abc", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── handleLogIdentifiers ────────────────────────────────────────────────────

func TestHandleLogIdentifiers_ReturnsIDs(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		FetchLogIdentifiersFn: func() []string { return []string{"ENG-1", "ENG-2"} },
	}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/identifiers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ENG-1")
	assert.Contains(t, w.Body.String(), "ENG-2")
}

func TestHandleLogIdentifiers_EmptyReturnsEmptyArray(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{} // FetchLogIdentifiersFn is nil -> returns nil
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/identifiers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w.Body.String()))
}

// ─── handleSetReviewer edge cases ────────────────────────────────────────────

func TestHandleSetReviewer_InvalidJSON_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{SetReviewerConfigFn: func(string, bool) error { return nil }}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/reviewer", bytes.NewBufferString(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSetReviewer_ServerError(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetReviewerConfigFn: func(string, bool) error { return errors.New("write failed") },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/reviewer", `{"profile":"rev","auto_review":true}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── handleGetReviewer defaults ──────────────────────────────────────────────

func TestHandleGetReviewer_DefaultsWhenNoFn(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{} // ReviewerConfigFn nil -> returns "", false
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/reviewer", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "", resp["profile"])
	assert.Equal(t, false, resp["auto_review"])
}

// ─── handleProvideInput / handleDismissInput ─────────────────────────────────

func TestHandleProvideInput_NotFound(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{} // ProvideInput always returns false
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/issues/ENG-1/provide-input", `{"message":"fix it"}`)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleProvideInput_EmptyMessage_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/issues/ENG-1/provide-input", `{"message":""}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleProvideInput_InvalidJSON_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/issues/ENG-1/provide-input", `not-json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDismissInput_NotFound(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{} // DismissInput always returns false
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/issues/ENG-1/dismiss-input", "")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleAgentComment_Success(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionComment}, "", time.Minute)
	require.NoError(t, err)

	var gotIdentifier, gotBody string
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{
		CommentOnIssueFn: func(_ context.Context, identifier, body string) error {
			gotIdentifier = identifier
			gotBody = body
			return nil
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/comment", bytes.NewBufferString(`{"body":"hello from agent"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ENG-1", gotIdentifier)
	// Agent comments are wrapped with the managed marker so they don't trigger
	// tracker_comment_added automations (prevents infinite comment-chain loops).
	assert.Contains(t, gotBody, "hello from agent")
	assert.Contains(t, gotBody, "<!-- itervox:managed -->", "agent comments must be marked managed to prevent automation loops")
}

// Gap D — happy path: a profile granted AgentActionCommentPR can post
// structured findings; the rendered Markdown is what reaches CommentOnIssue.
func TestHandleAgentCommentPR_Success(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionCommentPR}, "", time.Minute)
	require.NoError(t, err)

	var gotIdentifier, gotBody string
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{
		CommentOnIssueFn: func(_ context.Context, identifier, body string) error {
			gotIdentifier = identifier
			gotBody = body
			return nil
		},
	}
	srv := server.New(cfg)

	body := `{
		"summary": "PR review: 1 issue found.",
		"findings": [
			{"path": "internal/foo.go", "line": 42, "severity": "error", "body": "potential nil deref"}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/comment_pr", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ENG-1", gotIdentifier)
	assert.Contains(t, gotBody, "🤖 Itervox review")
	assert.Contains(t, gotBody, "internal/foo.go:42")
	assert.Contains(t, gotBody, "potential nil deref")
	assert.Contains(t, gotBody, "<!-- itervox:managed -->", "comment_pr must be marked managed to prevent automation loops")
}

// Gap D — comment_pr with no summary AND no findings must reject as 400
// before the tracker call so an agent that produced an empty review gets a
// clear "you sent nothing" error rather than a silent no-op comment.
func TestHandleAgentCommentPR_EmptySubmissionRejected(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionCommentPR}, "", time.Minute)
	require.NoError(t, err)

	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{
		CommentOnIssueFn: func(context.Context, string, string) error {
			t.Fatal("CommentOnIssue must not be called for an empty submission")
			return nil
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/comment_pr", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "summary or at least one finding")
}

// Gap D — a profile granted only AgentActionComment must NOT be allowed to
// call comment_pr. The two scopes are separate so reviewer profiles can be
// authorised for structured findings without granting freeform comment.
func TestHandleAgentCommentPR_ForbiddenWithoutCommentPRScope(t *testing.T) {
	store := agentactions.NewStore()
	// Note: only AgentActionComment, not AgentActionCommentPR.
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionComment}, "", time.Minute)
	require.NoError(t, err)

	var called bool
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{
		CommentOnIssueFn: func(context.Context, string, string) error {
			called = true
			return nil
		},
	}
	srv := server.New(cfg)

	body := `{"findings":[{"path":"a.go","line":1,"severity":"info","body":"x"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/comment_pr", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, called)
}

func TestHandleAgentProvideInput_ForbiddenWithoutPermission(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionComment}, "", time.Minute)
	require.NoError(t, err)

	var called bool
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{
		ProvideInputFn: func(string, string) bool {
			called = true
			return true
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/provide-input", bytes.NewBufferString(`{"message":"continue"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, called)
	assert.Contains(t, w.Body.String(), "agent_action_denied")
}

func TestHandleAgentMoveState_MissingTokenReturns401(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = agentactions.NewStore()
	cfg.Client = &server.FuncClient{
		UpdateIssueStateFn: func(context.Context, string, string) error { return nil },
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/move-state", bytes.NewBufferString(`{"state":"Done"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "unauthorized")
}

func TestHandleAgentMoveState_QueuesRefresh(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionMoveState}, "", time.Minute)
	require.NoError(t, err)

	refresh := make(chan struct{}, 1)
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.RefreshChan = refresh
	cfg.Client = &server.FuncClient{
		UpdateIssueStateFn: func(context.Context, string, string) error { return nil },
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/move-state", bytes.NewBufferString(`{"state":"Done"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	select {
	case <-refresh:
	default:
		t.Fatal("expected agent move-state to queue refresh")
	}
}

func TestHandleAgentMoveState_RejectsStateOutsideGrant(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.IssueScoped("ENG-1", "run-1", []string{config.AgentActionMoveState}, "", "Todo", time.Minute)
	require.NoError(t, err)

	called := false
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{
		UpdateIssueStateFn: func(context.Context, string, string) error {
			called = true
			return nil
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/move-state", bytes.NewBufferString(`{"state":"Done"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, called)
	assert.Contains(t, w.Body.String(), "agent_action_denied")
}

func TestStateSnapshotIncludesAutomationQueueAndDependencyRowsJSON(t *testing.T) {
	snap := server.StateSnapshot{
		GeneratedAt:         time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC),
		Counts:              server.Counts{},
		Running:             []server.RunningRow{},
		Retrying:            []server.RetryRow{},
		Paused:              []string{},
		MaxConcurrentAgents: 1,
		MaxRetries:          5,
		AutomationQueue: []server.AutomationQueueRow{{
			ID:           "automation:nightly:cron:ENG-1",
			AutomationID: "nightly",
			TriggerType:  "cron",
			Identifier:   "ENG-1",
			Profile:      "pm",
			Status:       "queued",
			Reason:       "no_slots",
			QueuedAt:     time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC),
			FiredAt:      time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC),
			AttemptCount: 1,
		}},
		DependencyAudit: []server.DependencyAuditRow{{
			Identifier: "ENG-2",
			IssueState: "Backlog",
			Status:     "blocked",
			BlockedBy:  []server.BlockerRefRow{{Identifier: "ENG-1", State: "Todo"}},
			WasBlocked: true,
		}},
		DependencyGraphNodes: []server.DependencyGraphNodeRow{{
			ID:         "ENG-1",
			Identifier: "ENG-1",
			Running:    false,
			Queued:     true,
			Terminal:   false,
		}},
		DependencyGraphEdges: []server.DependencyGraphEdgeRow{{
			ID:               "ENG-1->ENG-2",
			SourceIdentifier: "ENG-1",
			TargetIdentifier: "ENG-2",
			Resolved:         false,
			SourceKnown:      true,
		}},
	}

	data, err := json.Marshal(snap)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"automationQueue"`)
	assert.Contains(t, string(data), `"dependencyAudit"`)
	assert.Contains(t, string(data), `"dependencyGraphNodes"`)
	assert.Contains(t, string(data), `"dependencyGraphEdges"`)
}

func TestHandleAgentCreateIssue_Success(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionCreateIssue}, "Todo", time.Minute)
	require.NoError(t, err)

	var gotIdentifier string
	var gotTitle string
	var gotBody string
	var gotState string
	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{
		CreateIssueFn: func(_ context.Context, identifier, title, body, state string) (*domain.Issue, error) {
			gotIdentifier = identifier
			gotTitle = title
			gotBody = body
			gotState = state
			return &domain.Issue{Identifier: "ENG-2", Title: title, State: state}, nil
		},
	}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/create-issue", bytes.NewBufferString(`{"title":"Follow-up","body":"Add regression coverage","state":"Todo"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ENG-1", gotIdentifier)
	assert.Equal(t, "Follow-up", gotTitle)
	assert.Equal(t, "Add regression coverage", gotBody)
	assert.Equal(t, "Todo", gotState)
	assert.Contains(t, w.Body.String(), "ENG-2")
}

func TestHandleAgentCreateIssue_MissingConfiguredStateReturns400(t *testing.T) {
	store := agentactions.NewStore()
	token, err := store.Issue("ENG-1", "run-1", []string{config.AgentActionCreateIssue}, "", time.Minute)
	require.NoError(t, err)

	cfg := makeTestConfig(baseSnap())
	cfg.ActionTokenStore = store
	cfg.Client = &server.FuncClient{}
	srv := server.New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/ENG-1/create-issue", bytes.NewBufferString(`{"title":"Follow-up"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "create issue state is not configured")
}

// ─── handleUpsertProfile edge cases ──────────────────────────────────────────

func TestHandleUpsertProfile_MissingCommand_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{UpsertProfileFn: func(string, server.ProfileDef, string) error { return nil }}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/profiles/test", `{"prompt":"hi"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "command field required")
}

func TestHandleUpsertProfile_InvalidJSON_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{UpsertProfileFn: func(string, server.ProfileDef, string) error { return nil }}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/profiles/test", `not-json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleUpsertProfile_InvalidAllowedActions_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{UpsertProfileFn: func(string, server.ProfileDef, string) error { return nil }}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/profiles/test", `{"command":"claude","allowedActions":["comment","hack_the_daemon"]}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid_allowed_actions")
}

func TestHandleUpsertProfile_CreateIssueRequiresState(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{UpsertProfileFn: func(string, server.ProfileDef, string) error { return nil }}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/profiles/test", `{"command":"claude","allowedActions":["create_issue"]}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "createIssueState")
}

func TestHandleUpsertProfile_ServerError(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		UpsertProfileFn: func(string, server.ProfileDef, string) error { return errors.New("disk full") },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/profiles/test", `{"command":"claude"}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandleUpsertProfile_ConflictReturns409(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		UpsertProfileFn: func(string, server.ProfileDef, string) error {
			return errors.New(`profile "pm" already exists`)
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/profiles/pm", `{"command":"claude","originalName":"qa"}`)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "already exists")
}

func TestHandleSetAutomations_InvalidCronReturns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ProfileDefsFn: func() map[string]server.ProfileDef {
			return map[string]server.ProfileDef{
				"reviewer": {Command: "claude", Enabled: true},
			}
		},
		SetAutomationsFn: func([]server.AutomationDef) error { return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/automations", `{"automations":[{"id":"nightly","enabled":true,"profile":"reviewer","trigger":{"type":"cron","cron":"not-a-cron"},"filter":{}}]}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid_cron")
}

func TestHandleSetAutomations_InputRequiredAcceptsNoCron(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	called := false
	cfg.Client = &server.FuncClient{
		ProfileDefsFn: func() map[string]server.ProfileDef {
			return map[string]server.ProfileDef{
				"input-responder": {Command: "claude", Enabled: true},
			}
		},
		SetAutomationsFn: func(entries []server.AutomationDef) error {
			called = true
			require.Len(t, entries, 1)
			assert.Equal(t, "input_required", entries[0].Trigger.Type)
			assert.Equal(t, "input-responder", entries[0].Profile)
			assert.Equal(t, "continue|branch", entries[0].Filter.InputContextRegex)
			return nil
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/automations", `{"automations":[{"id":"input-responder","enabled":true,"profile":"input-responder","instructions":"Answer blocked-run questions.","trigger":{"type":"input_required"},"filter":{"inputContextRegex":"continue|branch"}}]}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, called)
}

func TestHandleSetAutomations_IssueEnteredStateRequiresTriggerState(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ProfileDefsFn: func() map[string]server.ProfileDef {
			return map[string]server.ProfileDef{
				"qa": {Command: "claude", Enabled: true},
			}
		},
		SetAutomationsFn: func([]server.AutomationDef) error { return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/automations", `{"automations":[{"id":"qa-entry","enabled":true,"profile":"qa","trigger":{"type":"issue_entered_state"}}]}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "trigger.state")
}

func TestHandleSetAutomations_AcceptsExpandedTriggersAndMatchMode(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	called := false
	cfg.Client = &server.FuncClient{
		ProfileDefsFn: func() map[string]server.ProfileDef {
			return map[string]server.ProfileDef{
				"pm":       {Command: "claude", Enabled: true},
				"qa":       {Command: "claude", Enabled: true},
				"reviewer": {Command: "claude", Enabled: true},
			}
		},
		SetAutomationsFn: func(entries []server.AutomationDef) error {
			called = true
			require.Len(t, entries, 4)
			assert.Equal(t, "tracker_comment_added", entries[0].Trigger.Type)
			assert.Equal(t, "any", entries[0].Filter.MatchMode)
			assert.Equal(t, "issue_entered_state", entries[1].Trigger.Type)
			assert.Equal(t, "Ready for QA", entries[1].Trigger.State)
			assert.Equal(t, "issue_moved_to_backlog", entries[2].Trigger.Type)
			assert.Equal(t, "run_failed", entries[3].Trigger.Type)
			return nil
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/automations", `{"automations":[
		{"id":"comment-watch","enabled":true,"profile":"pm","trigger":{"type":"tracker_comment_added"},"filter":{"matchMode":"any","labelsAny":["triage"]}},
		{"id":"qa-entry","enabled":true,"profile":"qa","trigger":{"type":"issue_entered_state","state":"Ready for QA"}},
		{"id":"backlog-watch","enabled":true,"profile":"pm","trigger":{"type":"issue_moved_to_backlog"}},
		{"id":"failed-run","enabled":true,"profile":"reviewer","trigger":{"type":"run_failed"}}
	]}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, called)
}

func TestHandleSetAutomations_RejectsDuplicateIDs(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetAutomationsFn: func([]server.AutomationDef) error {
			t.Fatal("SetAutomations should not be called when duplicate IDs are submitted")
			return nil
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/automations", `{"automations":[
		{"id":"duplicate","enabled":true,"profile":"pm","trigger":{"type":"tracker_comment_added"}},
		{"id":"duplicate","enabled":true,"profile":"qa","trigger":{"type":"run_failed"}}
	]}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "duplicate automation id")
}

func TestHandleSetAutomations_RejectsInvalidRegex(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ProfileDefsFn: func() map[string]server.ProfileDef {
			return map[string]server.ProfileDef{
				"pm": {Command: "claude", Enabled: true},
			}
		},
		SetAutomationsFn: func([]server.AutomationDef) error {
			t.Fatal("SetAutomations should not be called for invalid regex")
			return nil
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/automations", `{"automations":[
		{"id":"comment-watch","enabled":true,"profile":"pm","trigger":{"type":"tracker_comment_added"},"filter":{"identifierRegex":"["}}
	]}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid_regex")
}

func TestHandleSetAutomations_RejectsDisabledProfile(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		ProfileDefsFn: func() map[string]server.ProfileDef {
			return map[string]server.ProfileDef{
				"pm": {Command: "claude", Enabled: false},
			}
		},
		SetAutomationsFn: func([]server.AutomationDef) error {
			t.Fatal("SetAutomations should not be called for disabled profiles")
			return nil
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/automations", `{"automations":[
		{"id":"comment-watch","enabled":true,"profile":"pm","trigger":{"type":"tracker_comment_added"}}
	]}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "disabled profile")
}

// ─── handleUpdateTrackerStates edge cases ────────────────────────────────────

func TestHandleUpdateTrackerStates_EmptyActiveStates_Returns400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{UpdateTrackerStatesFn: func(_, _ []string, _ string) error { return nil }}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/tracker/states",
		`{"activeStates":[],"terminalStates":["Done"],"completionState":"Done"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "activeStates must not be empty")
}

func TestHandleUpdateTrackerStates_ServerError(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		UpdateTrackerStatesFn: func(_, _ []string, _ string) error { return errors.New("write error") },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/tracker/states",
		`{"activeStates":["Todo"],"terminalStates":["Done"],"completionState":"Done"}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── handleClearIssueLogs error path ─────────────────────────────────────────

func TestHandleClearIssueLogs_Error(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{ClearLogsFn: func(string) error { return errors.New("rm failed") }}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/issues/ENG-1/logs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── handleDeleteProfile error path ──────────────────────────────────────────

func TestHandleDeleteProfile_Error(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{DeleteProfileFn: func(string) error { return errors.New("not found") }}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/profiles/missing", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── handleGetProjectFilter / handleSetProjectFilter without ProjectManager ──

func TestHandleGetProjectFilter_NotConfigured(t *testing.T) {
	srv := testServer(t) // no ProjectManager
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/filter", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

func TestHandleSetProjectFilter_NotConfigured(t *testing.T) {
	srv := testServer(t) // no ProjectManager
	w := putJSON(t, srv, "/api/v1/projects/filter", `{"slugs":["a"]}`)
	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

func TestHandleSetProjectFilter_InvalidJSON_Returns400(t *testing.T) {
	srv, _ := testServerWithProjects(t)
	w := putJSON(t, srv, "/api/v1/projects/filter", `not-json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ─── Validate ────────────────────────────────────────────────────────────────

func TestValidate_MissingSnapshot(t *testing.T) {
	cfg := server.Config{RefreshChan: make(chan struct{}, 1)}
	srv := server.New(cfg)
	err := srv.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Snapshot")
}

func TestValidate_MissingRefreshChan(t *testing.T) {
	cfg := server.Config{Snapshot: func() server.StateSnapshot { return baseSnap() }}
	srv := server.New(cfg)
	err := srv.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RefreshChan")
}

func TestValidate_AllPresent(t *testing.T) {
	srv := testServer(t)
	assert.NoError(t, srv.Validate())
}

// ─── handleIssueLogs with skipped entries ────────────────────────────────────

func TestHandleIssueLogs_SkipsDebugAndLifecycleEntries(t *testing.T) {
	srv := testServerWithIssueLogs(t, func(string) []string {
		return []string{
			`{"level":"DEBUG","msg":"internal detail"}`,
			`{"level":"INFO","msg":"claude: session started"}`,
			`{"level":"INFO","msg":"claude: text","text":"visible line"}`,
			`not-json-line`,
		}
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/ENG-1/logs", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var entries []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &entries))
	// Only the "claude: text" entry should survive; DEBUG, lifecycle, and non-JSON are skipped.
	require.Len(t, entries, 1)
	assert.Equal(t, "text", entries[0]["event"])
	assert.Equal(t, "visible line", entries[0]["message"])
}

// ─── POST /api/v1/issues/{id}/cancel alias ───────────────────────────────────

func TestHandleCancelIssue_PostAlias(t *testing.T) {
	srv := testServerWithCancel(t, func(id string) bool { return id == "ENG-1" })
	w := postJSON(t, srv, "/api/v1/issues/ENG-1/cancel", "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "cancelled")
}

// ─── POST /api/v1/automations/{id}/test (T-10) ───────────────────────────────

func TestHandleTestAutomation_Success(t *testing.T) {
	var gotAutomationID, gotIdentifier string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		TestAutomationFn: func(_ context.Context, automationID, identifier string) error {
			gotAutomationID = automationID
			gotIdentifier = identifier
			return nil
		},
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/automations/cron-nightly/test", `{"identifier":"ENG-1"}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "cron-nightly", gotAutomationID)
	assert.Equal(t, "ENG-1", gotIdentifier)
	assert.Contains(t, w.Body.String(), "ok")
}

func TestHandleTestAutomation_MissingIdentifier_400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		TestAutomationFn: func(context.Context, string, string) error { return nil },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/automations/cron/test", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "identifier")
}

func TestHandleTestAutomation_BlankIdentifier_400(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		TestAutomationFn: func(context.Context, string, string) error { return nil },
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/automations/cron/test", `{"identifier":"   "}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTestAutomation_BackendError_500(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		TestAutomationFn: func(context.Context, string, string) error {
			return errors.New("rule not found")
		},
	}
	srv := server.New(cfg)
	w := postJSON(t, srv, "/api/v1/automations/missing/test", `{"identifier":"ENG-1"}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "rule not found")
}

// PUT /settings/agent/max-retries — happy path round-trips the integer to the
// orchestrator client and surfaces it in the JSON response.
func TestHandleSetMaxRetries_OK(t *testing.T) {
	var captured int
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetMaxRetriesFn: func(n int) error { captured = n; return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/agent/max-retries", `{"maxRetries":7}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 7, captured)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(7), resp["maxRetries"])
}

// Negative max_retries is meaningless — the handler must reject it BEFORE
// hitting the persist layer to avoid a misleading 500 from the WORKFLOW.md
// patcher.
func TestHandleSetMaxRetries_NegativeRejected(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetMaxRetriesFn: func(int) error {
			t.Fatal("setter should not have been called for negative input")
			return nil
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/agent/max-retries", `{"maxRetries":-1}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Missing maxRetries field is a malformed body — must 400, not silently
// default to 0 (which would clobber the operator's previous value).
func TestHandleSetMaxRetries_MissingField(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetMaxRetriesFn: func(int) error { return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/agent/max-retries", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// PUT /settings/tracker/failed-state — happy path with a non-empty state.
func TestHandleSetFailedState_OK(t *testing.T) {
	var captured string
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetFailedStateFn: func(s string) error { captured = s; return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/tracker/failed-state", `{"failedState":"Backlog"}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Backlog", captured)
}

// Empty string round-trip — operator picks "Pause (do not move)" — must
// reach the setter as "" so the orchestrator clears the field.
func TestHandleSetFailedState_EmptyMeansPause(t *testing.T) {
	captured := "untouched"
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetFailedStateFn: func(s string) error { captured = s; return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/tracker/failed-state", `{"failedState":""}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "", captured, "empty string must reach the setter, not be rejected as bad_request")
}

// Gap E §4.7 — switch-cap settings handlers.
//
// PUT /settings/agent/max-switches-per-issue-per-window — happy path round-trips.
func TestHandleSetMaxSwitches_OK(t *testing.T) {
	var captured int
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetMaxSwitchesPerIssuePerWindowFn: func(n int) error { captured = n; return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/agent/max-switches-per-issue-per-window",
		`{"maxSwitchesPerIssuePerWindow":3}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 3, captured)
	assert.Contains(t, w.Body.String(), `"maxSwitchesPerIssuePerWindow":3`)
}

func TestHandleSetMaxSwitches_NegativeRejected(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetMaxSwitchesPerIssuePerWindowFn: func(int) error {
			t.Fatal("setter should not be called for negative input")
			return nil
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/agent/max-switches-per-issue-per-window",
		`{"maxSwitchesPerIssuePerWindow":-1}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSetMaxSwitches_MissingField(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetMaxSwitchesPerIssuePerWindowFn: func(int) error { return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/agent/max-switches-per-issue-per-window", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// PUT /settings/agent/switch-window-hours — happy path round-trips.
func TestHandleSetSwitchWindowHours_OK(t *testing.T) {
	var captured int
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetSwitchWindowHoursFn: func(h int) error { captured = h; return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/agent/switch-window-hours",
		`{"switchWindowHours":12}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 12, captured)
	assert.Contains(t, w.Body.String(), `"switchWindowHours":12`)
}

func TestHandleSetSwitchWindowHours_MissingField(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetSwitchWindowHoursFn: func(int) error { return nil },
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/agent/switch-window-hours", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Setter validation failure (e.g. unknown state) must surface as 400, not
// 500 — it's user error.
func TestHandleSetFailedState_UnknownStateRejected(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{
		SetFailedStateFn: func(string) error {
			return errors.New(`failed_state "Garbage" is not in tracker.active_states / terminal_states / backlog_states`)
		},
	}
	srv := server.New(cfg)
	w := putJSON(t, srv, "/api/v1/settings/tracker/failed-state", `{"failedState":"Garbage"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "Garbage")
}
