package server_test

import (
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

// mutableSnap is a snapshot source a test can change between requests.
type mutableSnap struct {
	mu   sync.Mutex
	snap server.StateSnapshot
}

func (m *mutableSnap) get() server.StateSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap
}

func (m *mutableSnap) update(f func(*server.StateSnapshot)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f(&m.snap)
}

func summaryServer(t *testing.T, src *mutableSnap) http.Handler {
	t.Helper()
	cfg := makeTestConfig(server.StateSnapshot{})
	cfg.Snapshot = src.get
	cfg.APIToken = "summary-test-token-0123456789"
	cfg.Version = "1.2.3"
	return server.New(cfg)
}

func getSummary(h http.Handler, ifNoneMatch string, token bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/summary", nil)
	if token {
		req.Header.Set("Authorization", "Bearer summary-test-token-0123456789")
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestSummaryEndpointETag304 pins the conditional-GET contract the Mac app
// relies on (#212): the summary is behind the bearer token, carries a quoted
// ETag equal to its body's etag, answers If-None-Match with 304 and no body
// while nothing but generatedAt changed, and changes its tag when an
// input-required entry or a failure changes even with equal counts.
func TestSummaryEndpointETag304(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	src := &mutableSnap{snap: server.StateSnapshot{GeneratedAt: t0, Counts: server.Counts{Running: 1}}}
	h := summaryServer(t, src)

	assert.Equal(t, http.StatusUnauthorized, getSummary(h, "", false).Code, "summary is behind the bearer token")

	first := getSummary(h, "", true)
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)
	assert.Equal(t, "no-cache", first.Header().Get("Cache-Control"))
	var body server.Summary
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &body))
	assert.Equal(t, `"`+body.ETag+`"`, etag, "the header is the body's etag, quoted")

	// Unchanged → 304, no body, same tag.
	notModified := getSummary(h, etag, true)
	assert.Equal(t, http.StatusNotModified, notModified.Code)
	assert.Empty(t, notModified.Body.Bytes())
	assert.Equal(t, etag, notModified.Header().Get("ETag"))

	// generatedAt is outside the tag.
	src.update(func(s *server.StateSnapshot) { s.GeneratedAt = t0.Add(time.Minute) })
	assert.Equal(t, http.StatusNotModified, getSummary(h, etag, true).Code)
	// Lists, weak validators and "*" match too.
	assert.Equal(t, http.StatusNotModified, getSummary(h, `"other", W/`+etag, true).Code)
	assert.Equal(t, http.StatusNotModified, getSummary(h, "*", true).Code)
	assert.Equal(t, http.StatusOK, getSummary(h, `"stale"`, true).Code)

	// A new question changes the tag.
	src.update(func(s *server.StateSnapshot) {
		s.InputRequired = []server.InputRequiredRow{{Identifier: "ENG-1", State: "input_required", Context: "Which DB?", QueuedAt: "2026-10-22T10:00:00Z"}}
	})
	changed := getSummary(h, etag, true)
	require.Equal(t, http.StatusOK, changed.Code)
	etag2 := changed.Header().Get("ETag")
	assert.NotEqual(t, etag, etag2)

	// A different question with equal counts changes it as well.
	src.update(func(s *server.StateSnapshot) { s.InputRequired[0].Identifier = "ENG-2" })
	again := getSummary(h, etag2, true)
	require.Equal(t, http.StatusOK, again.Code)
	etag3 := again.Header().Get("ETag")
	assert.NotEqual(t, etag2, etag3)

	// So does a failure whose message changes while the ring length stays.
	src.update(func(s *server.StateSnapshot) {
		s.RecentFailures = []server.FailureRow{{Kind: "worker_failed", Identifier: "ENG-3", Message: "exit 1", OccurredAt: t0}}
	})
	etag4 := getSummary(h, etag3, true).Header().Get("ETag")
	assert.NotEqual(t, etag3, etag4)
	src.update(func(s *server.StateSnapshot) { s.RecentFailures[0].Message = "exit 2" })
	w := getSummary(h, etag4, true)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEqual(t, etag4, w.Header().Get("ETag"))
}

// TestSummaryCountsMatchSnapshot checks every summary count against the
// /api/v1/state body served from the same snapshot.
func TestSummaryCountsMatchSnapshot(t *testing.T) {
	gen := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	src := &mutableSnap{snap: server.StateSnapshot{
		GeneratedAt: gen,
		ProjectName: "web-app",
		Counts:      server.Counts{Running: 2, Retrying: 1, Paused: 3},
		Running:     []server.RunningRow{{Identifier: "A-1"}, {Identifier: "A-2"}},
		InputRequired: []server.InputRequiredRow{
			{Identifier: "A-3", State: "input_required"},
			{Identifier: "A-4", State: "input_required"},
			{Identifier: "A-5", State: "pending_input_resume"},
		},
		RecentFailures: []server.FailureRow{{Kind: "worker_failed"}, {Kind: "tracker_poll"}},
	}}
	h := summaryServer(t, src)

	w := getSummary(h, "", true)
	require.Equal(t, http.StatusOK, w.Code)
	var sum server.Summary
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &sum))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	req.Header.Set("Authorization", "Bearer summary-test-token-0123456789")
	sw := httptest.NewRecorder()
	h.ServeHTTP(sw, req)
	require.Equal(t, http.StatusOK, sw.Code)
	var state server.StateSnapshot
	require.NoError(t, json.Unmarshal(sw.Body.Bytes(), &state))

	waiting, resuming := 0, 0
	for _, r := range state.InputRequired {
		if r.State == "input_required" {
			waiting++
		} else {
			resuming++
		}
	}
	assert.Equal(t, server.SummaryCounts{
		Running:            state.Counts.Running,
		Retrying:           state.Counts.Retrying,
		Paused:             state.Counts.Paused,
		InputRequired:      waiting,
		PendingInputResume: resuming,
		RecentFailures:     len(state.RecentFailures),
	}, sum.Counts)
	assert.Equal(t, server.SummaryCounts{Running: 2, Retrying: 1, Paused: 3, InputRequired: 2, PendingInputResume: 1, RecentFailures: 2}, sum.Counts)
	assert.Equal(t, "1.2.3", sum.Version)
	assert.Equal(t, "web-app", sum.ProjectName)
	assert.False(t, sum.Paused, "no daemon-wide pause until #213")
	assert.True(t, sum.GeneratedAt.Equal(gen))
	assert.Less(t, w.Body.Len(), 512, "the summary stays small")
	assert.True(t, strings.Contains(w.Body.String(), `"paused":false`), "paused is always present")
}
