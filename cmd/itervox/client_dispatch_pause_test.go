package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientPauseAllDispatch (#213): `itervox pause` without an issue
// pauses all dispatch through POST /api/v1/dispatch/pause, with the reason
// and an until of now + --for; without flags it sends an empty body.
func TestClientPauseAllDispatch(t *testing.T) {
	until := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	d := newFakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, map[string]any{"dispatchPaused": map[string]any{"reason": "deploy", "until": until}})
	})
	before := time.Now()
	code, out, errOut := runClient(t, t.Context(), d, "pause", "--reason", "deploy", "--for", "2h")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, "Paused all dispatch; running agents continue, nothing new starts until "+
		until.Local().Format("15:04 Mon Jan 2")+".\n", out)
	calls := d.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, "POST", calls[0].method)
	assert.Equal(t, "/api/v1/dispatch/pause", calls[0].path)
	assert.Equal(t, "Bearer "+clientTestToken, calls[0].auth)
	var body struct{ Reason, Until string }
	require.NoError(t, json.Unmarshal([]byte(calls[0].body), &body))
	assert.Equal(t, "deploy", body.Reason)
	sent, err := time.Parse(time.RFC3339, body.Until)
	require.NoError(t, err)
	assert.WithinDuration(t, before.Add(2*time.Hour), sent, 5*time.Second, "until is now + --for")

	d2 := newFakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, map[string]any{"dispatchPaused": map[string]any{}})
	})
	code, out, errOut = runClient(t, t.Context(), d2, "pause")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, "Paused all dispatch; running agents continue, nothing new starts until itervox resume.\n", out)
	require.Len(t, d2.recorded(), 1)
	assert.JSONEq(t, `{}`, d2.recorded()[0].body)
}

// TestClientResumeAllDispatch (#213): `itervox resume` without an issue
// resumes all dispatch; with an issue it still resumes that issue.
func TestClientResumeAllDispatch(t *testing.T) {
	d := newFakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, map[string]any{"dispatchPaused": nil})
	})
	code, out, errOut := runClient(t, t.Context(), d, "resume")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, "Resumed dispatch; waiting issues start as slots allow.\n", out)
	code, _, errOut = runClient(t, t.Context(), d, "resume", "ENG-1")
	require.Equal(t, 0, code, errOut)
	calls := d.recorded()
	require.Len(t, calls, 2)
	assert.Equal(t, "/api/v1/dispatch/resume", calls[0].path)
	assert.Equal(t, "/api/v1/issues/ENG-1/resume", calls[1].path, "an issue argument keeps the per-issue resume")
}

// TestClientPauseAllDispatchRefused: a daemon error is reported, exit 1.
func TestClientPauseAllDispatchRefused(t *testing.T) {
	d := newFakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"until must be in the future"}}`))
	})
	code, out, errOut := runClient(t, t.Context(), d, "pause", "--for", "1s")
	assert.Equal(t, 1, code)
	assert.Empty(t, out)
	assert.Equal(t, "itervox pause: until must be in the future (400)\n", errOut)
}

// TestWatchShowsDispatchPause (#213): watch names a daemon-wide pause and
// its reason under the header; an unpaused daemon shows no such line.
func TestWatchShowsDispatchPause(t *testing.T) {
	var s watchState
	assert.NotContains(t, renderWatch(s, time.Now(), false), "PAUSED")
	require.NoError(t, json.Unmarshal([]byte(`{"dispatchPaused":{"reason":"deploy freeze"}}`), &s))
	assert.Contains(t, renderWatch(s, time.Now(), false),
		"ALL DISPATCH PAUSED — running agents continue, nothing new starts: deploy freeze (itervox resume)\n")
}
