package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/server"
)

// fakeDispatch records the calls of the #213 endpoints.
type fakeDispatch struct {
	mu          sync.Mutex
	since       int64
	limit       int
	pauseReason string
	pauseUntil  time.Time
	err         error
}

func (f *fakeDispatch) Feed(since int64, limit int) server.FeedPage {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since, f.limit = since, limit
	return server.FeedPage{Events: []server.FeedEvent{{Seq: since + 1, Kind: "run_started", Identifier: "ENG-1"}}, NextSeq: since + 1, HeadSeq: since + 1}
}

func (f *fakeDispatch) PauseDispatch(_ context.Context, reason string, until time.Time) (*server.DispatchPauseRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauseReason, f.pauseUntil = reason, until
	if f.err != nil {
		return nil, f.err
	}
	row := &server.DispatchPauseRow{Reason: reason}
	if !until.IsZero() {
		row.Until = &until
	}
	return row, nil
}

func (f *fakeDispatch) ResumeDispatch(context.Context) (*server.DispatchPauseRow, error) {
	return nil, f.err
}

func feedRequest(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestFeedEndpointValidatesTheCursor (#213): since and limit are parsed
// strictly and handed to the client; the page is served as is.
func TestFeedEndpointValidatesTheCursor(t *testing.T) {
	fd := &fakeDispatch{}
	cfg := makeTestConfig(server.StateSnapshot{})
	cfg.Dispatch = fd
	h := server.New(cfg)

	w := feedRequest(h, http.MethodGet, "/api/v1/feed?since=41&limit=10", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, int64(41), fd.since)
	assert.Equal(t, 10, fd.limit)
	var page server.FeedPage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	assert.Equal(t, int64(42), page.NextSeq)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	w = feedRequest(h, http.MethodGet, "/api/v1/feed", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, int64(0), fd.since)
	assert.Equal(t, 0, fd.limit, "no limit: the daemon default")

	for _, q := range []string{"since=-1", "since=x", "limit=0", "limit=1001", "limit=x"} {
		assert.Equal(t, http.StatusBadRequest, feedRequest(h, http.MethodGet, "/api/v1/feed?"+q, "").Code, q)
	}
}

// TestDispatchPauseEndpoints (#213): body validation, the applied pause in
// the answer, 400 for a past until and 503 for an unreachable loop.
func TestDispatchPauseEndpoints(t *testing.T) {
	fd := &fakeDispatch{}
	cfg := makeTestConfig(server.StateSnapshot{})
	cfg.Dispatch = fd
	h := server.New(cfg)

	w := feedRequest(h, http.MethodPost, "/api/v1/dispatch/pause", `{"reason":" deploy ","until":"2030-01-02T03:04:05Z"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "deploy", fd.pauseReason)
	assert.True(t, fd.pauseUntil.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)))
	assert.JSONEq(t, `{"dispatchPaused":{"reason":"deploy","until":"2030-01-02T03:04:05Z","pausedAt":"0001-01-01T00:00:00Z"}}`, w.Body.String())

	assert.Equal(t, http.StatusOK, feedRequest(h, http.MethodPost, "/api/v1/dispatch/pause", "").Code, "an empty body pauses with no reason")
	assert.Equal(t, http.StatusBadRequest, feedRequest(h, http.MethodPost, "/api/v1/dispatch/pause", `{"until":"tomorrow"}`).Code)
	assert.Equal(t, http.StatusBadRequest, feedRequest(h, http.MethodPost, "/api/v1/dispatch/pause", `{"reason":"`+strings.Repeat("r", 501)+`"}`).Code)
	assert.Equal(t, http.StatusBadRequest, feedRequest(h, http.MethodPost, "/api/v1/dispatch/pause", `{`).Code)

	w = feedRequest(h, http.MethodPost, "/api/v1/dispatch/resume", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"dispatchPaused":null}`, w.Body.String())

	fd.err = server.ErrPauseUntilPast
	assert.Equal(t, http.StatusBadRequest, feedRequest(h, http.MethodPost, "/api/v1/dispatch/pause", `{}`).Code)
	fd.err = server.ErrBusy
	w = feedRequest(h, http.MethodPost, "/api/v1/dispatch/resume", "")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
}

// TestFeedEndpointsWithoutClientAnswer503: a server without a Dispatch
// client answers 503 on all three routes.
func TestFeedEndpointsWithoutClientAnswer503(t *testing.T) {
	h := server.New(makeTestConfig(server.StateSnapshot{}))
	assert.Equal(t, http.StatusServiceUnavailable, feedRequest(h, http.MethodGet, "/api/v1/feed", "").Code)
	assert.Equal(t, http.StatusServiceUnavailable, feedRequest(h, http.MethodPost, "/api/v1/dispatch/pause", "").Code)
	assert.Equal(t, http.StatusServiceUnavailable, feedRequest(h, http.MethodPost, "/api/v1/dispatch/resume", "").Code)
}
