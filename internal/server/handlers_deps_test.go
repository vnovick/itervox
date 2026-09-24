package server_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// fakeDepsAnalyzer is the test seam for the DepsAnalyzer interface.
type fakeDepsAnalyzer struct {
	enqueueFn      func(profile, mode string) (string, time.Time, error)
	statusFn       func(jobID string) (server.DepsAnalyzeJobRow, bool)
	defaultProfile string
	lastEnqueueArg string
	lastModeArg    string
	cancelOK       bool
	lastCancelArg  string
}

func (f *fakeDepsAnalyzer) EnqueueAnalysis(profile, mode string) (string, time.Time, error) {
	f.lastEnqueueArg = profile
	f.lastModeArg = mode
	if f.enqueueFn != nil {
		return f.enqueueFn(profile, mode)
	}
	return "job-1", time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC), nil
}

func (f *fakeDepsAnalyzer) Status(jobID string) (server.DepsAnalyzeJobRow, bool) {
	if f.statusFn != nil {
		return f.statusFn(jobID)
	}
	return server.DepsAnalyzeJobRow{}, false
}

func (f *fakeDepsAnalyzer) DefaultProfile() string {
	return f.defaultProfile
}

func (f *fakeDepsAnalyzer) CancelAnalysis(jobID string) bool {
	f.lastCancelArg = jobID
	return f.cancelOK
}

func newDepsTestServer(t *testing.T, da server.DepsAnalyzer) *server.Server {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	cfg.DepsAnalyzer = da
	return server.New(cfg)
}

func TestHandleDepsAnalyze_NoBodyUsesDefaultProfile(t *testing.T) {
	da := &fakeDepsAnalyzer{defaultProfile: "deps-analyzer"}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deps/analyze", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "job-1", got["jobId"])
	assert.Equal(t, "deps-analyzer", got["profile"])
	assert.Equal(t, "deps-analyzer", da.lastEnqueueArg)
}

func TestHandleDepsAnalyze_BodyProfileOverridesDefault(t *testing.T) {
	da := &fakeDepsAnalyzer{defaultProfile: "deps-analyzer"}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deps/analyze",
		bytes.NewReader([]byte(`{"profile":"custom-analyzer"}`)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "custom-analyzer", da.lastEnqueueArg)
}

func TestHandleDepsAnalyze_NoProfileAndNoDefaultReturns422(t *testing.T) {
	da := &fakeDepsAnalyzer{}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deps/analyze", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "deps_analyzer_profile_required")
}

func TestHandleDepsAnalyze_EnqueueErrorReturns422(t *testing.T) {
	da := &fakeDepsAnalyzer{
		defaultProfile: "deps-analyzer",
		enqueueFn: func(string, string) (string, time.Time, error) {
			return "", time.Time{}, errors.New("profile disabled")
		},
	}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deps/analyze", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "profile disabled")
}

func TestHandleDepsAnalyze_NilAnalyzerReturns503(t *testing.T) {
	srv := newDepsTestServer(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deps/analyze", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "deps_analyzer_unavailable")
}

func TestHandleDepsAnalyze_NoModeDefaultsToAuto(t *testing.T) {
	da := &fakeDepsAnalyzer{defaultProfile: "deps-analyzer"}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deps/analyze", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "auto", da.lastModeArg)
}

func TestHandleDepsAnalyze_BodyModeFullThreadsThrough(t *testing.T) {
	da := &fakeDepsAnalyzer{defaultProfile: "deps-analyzer"}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deps/analyze",
		bytes.NewReader([]byte(`{"mode":"full"}`)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "full", da.lastModeArg)
}

func TestHandleDepsAnalyze_UnknownModeReturns400(t *testing.T) {
	da := &fakeDepsAnalyzer{defaultProfile: "deps-analyzer"}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deps/analyze",
		bytes.NewReader([]byte(`{"mode":"turbo"}`)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid_mode")
	assert.Empty(t, da.lastEnqueueArg, "EnqueueAnalysis must not be called on an invalid mode")
}

func TestHandleDepsAnalyze_InvalidJSONReturns400(t *testing.T) {
	da := &fakeDepsAnalyzer{defaultProfile: "deps-analyzer"}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deps/analyze",
		strings.NewReader(`{not json`))
	req.Header.Set("Content-Length", "9")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "bad_body")
}

func TestHandleDepsAnalyzeStatus_Found(t *testing.T) {
	queuedAt := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	da := &fakeDepsAnalyzer{
		statusFn: func(id string) (server.DepsAnalyzeJobRow, bool) {
			if id != "job-1" {
				return server.DepsAnalyzeJobRow{}, false
			}
			return server.DepsAnalyzeJobRow{
				JobID: "job-1", Profile: "deps-analyzer",
				Status: "succeeded", QueuedAt: queuedAt, EdgesFound: 3,
			}, true
		},
	}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deps/analyze/job-1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got server.DepsAnalyzeJobRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "job-1", got.JobID)
	assert.Equal(t, "succeeded", got.Status)
	assert.Equal(t, 3, got.EdgesFound)
}

func TestHandleDepsAnalyzeStatus_NotFoundReturns404(t *testing.T) {
	da := &fakeDepsAnalyzer{
		statusFn: func(string) (server.DepsAnalyzeJobRow, bool) {
			return server.DepsAnalyzeJobRow{}, false
		},
	}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deps/analyze/missing", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleDepsAnalyzeStatus_NilAnalyzerReturns503(t *testing.T) {
	srv := newDepsTestServer(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deps/analyze/x", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestDepsAnalyzeCancelReturns204(t *testing.T) {
	da := &fakeDepsAnalyzer{defaultProfile: "deps-analyzer", cancelOK: true}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deps/analyze/job-1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "job-1", da.lastCancelArg)
}

func TestDepsAnalyzeCancelUnknownJobReturns404(t *testing.T) {
	da := &fakeDepsAnalyzer{defaultProfile: "deps-analyzer", cancelOK: false}
	srv := newDepsTestServer(t, da)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deps/analyze/nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDepsAnalyzeCancelWithoutAnalyzerReturns503(t *testing.T) {
	srv := newDepsTestServer(t, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/deps/analyze/job-1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// ─── deps-override (unified-dependency-graph Task 6) ──────────────────────

func testServerWithDepsOverride(t *testing.T, fn func(string, bool) bool) *server.Server {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{SetDepsOverrideFn: fn}
	return server.New(cfg)
}

func TestHandleSetDepsOverride_Returns202AndCallsClientWithEnabledTrue(t *testing.T) {
	var gotIdentifier string
	var gotEnabled bool
	srv := testServerWithDepsOverride(t, func(identifier string, enabled bool) bool {
		gotIdentifier = identifier
		gotEnabled = enabled
		return true
	})

	w := postJSON(t, srv, "/api/v1/issues/ENG-2/deps-override", "")

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "ENG-2", gotIdentifier)
	assert.True(t, gotEnabled)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ENG-2", resp["identifier"])
	assert.Equal(t, true, resp["overridden"])
}

func TestHandleClearDepsOverride_Returns202AndCallsClientWithEnabledFalse(t *testing.T) {
	var gotIdentifier string
	var gotEnabled bool
	called := false
	srv := testServerWithDepsOverride(t, func(identifier string, enabled bool) bool {
		called = true
		gotIdentifier = identifier
		gotEnabled = enabled
		return true
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/issues/ENG-2/deps-override", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.True(t, called, "client.SetDepsOverride must be called")
	assert.Equal(t, "ENG-2", gotIdentifier)
	assert.False(t, gotEnabled)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ENG-2", resp["identifier"])
	assert.Equal(t, false, resp["overridden"])
}

func TestHandleSetDepsOverride_ChannelFullReturns503(t *testing.T) {
	srv := testServerWithDepsOverride(t, func(string, bool) bool { return false })

	w := postJSON(t, srv, "/api/v1/issues/ENG-2/deps-override", "")

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "deps_override_queue_full")
}

func TestHandleClearDepsOverride_ChannelFullReturns503(t *testing.T) {
	srv := testServerWithDepsOverride(t, func(string, bool) bool { return false })

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/issues/ENG-2/deps-override", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "deps_override_queue_full")
}
