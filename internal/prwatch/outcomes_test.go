package prwatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type outcomeRecorder struct {
	mu  sync.Mutex
	got []Outcome
}

func (r *outcomeRecorder) record(_ context.Context, o Outcome) {
	r.mu.Lock()
	r.got = append(r.got, o)
	r.mu.Unlock()
}

func (r *outcomeRecorder) take() []Outcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.got
	r.got = nil
	return out
}

// newOutcomeWatcher is a watcher with no PR-event rule (Kinds empty) that
// tracks prURL's outcome in *state.
func newOutcomeWatcher(t *testing.T, state *string) (*Watcher, *fakeAPI, *outcomeRecorder) {
	t.Helper()
	api := &fakeAPI{routes: map[string]string{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	rec := &outcomeRecorder{}
	w := New(Config{
		Client: &Client{HTTP: srv.Client(), Token: func(string) string { return "tok" },
			BaseURL: func(string) string { return srv.URL }},
		Managed: func() []Managed { return nil },
		Kinds:   func() map[string]bool { return nil },
		Emit:    func(context.Context, Event) { t.Error("outcomes never go through Emit") },
		TrackedOutcomes: func() []Tracked {
			if *state == "" {
				return nil
			}
			return []Tracked{{Managed: Managed{URL: prURL, Identifier: "ENG-7"}, State: *state}}
		},
		Outcome: rec.record,
	})
	return w, api, rec
}

// TestPollOutcomesReportsMergedAndClosed: the outcome poll runs without any
// PR-event rule; an open PR reports nothing, a merged one is reported with
// its merge time, a PR closed unmerged as closed, and a closed one that was
// reopened as open. An untracked ledger reads nothing.
func TestPollOutcomesReportsMergedAndClosed(t *testing.T) {
	state := OutcomeOpen
	w, api, rec := newOutcomeWatcher(t, &state)
	api.set("/repos/acme/app/pulls/7", openPR("aaa", "clean"))
	w.PollOutcomes(t.Context())
	assert.Empty(t, rec.take(), "an open PR reports nothing")

	api.set("/repos/acme/app/pulls/7", `{"state":"closed","merged":true,"merged_at":"2026-11-20T10:00:00Z","closed_at":"2026-11-20T10:00:00Z","number":7}`)
	w.PollOutcomes(t.Context())
	got := rec.take()
	require.Len(t, got, 1)
	assert.Equal(t, Outcome{PRURL: prURL, Identifier: "ENG-7", State: OutcomeMerged,
		At: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), Source: SourcePoll}, got[0])
	w.PollOutcomes(t.Context())
	assert.Len(t, rec.take(), 1, "reported again while the ledger has not taken it")

	api.set("/repos/acme/app/pulls/7", `{"state":"closed","merged":false,"closed_at":"2026-11-21T09:00:00Z","number":7}`)
	w.PollOutcomes(t.Context())
	got = rec.take()
	require.Len(t, got, 1)
	assert.Equal(t, OutcomeClosed, got[0].State)

	state = OutcomeClosed
	w.PollOutcomes(t.Context())
	assert.Empty(t, rec.take(), "the tracked state already says closed")
	api.set("/repos/acme/app/pulls/7", openPR("bbb", "clean"))
	w.PollOutcomes(t.Context())
	got = rec.take()
	require.Len(t, got, 1)
	assert.Equal(t, OutcomeOpen, got[0].State, "reopened")

	state = ""
	before := api.hitCount()
	w.PollOutcomes(t.Context())
	assert.Equal(t, before, api.hitCount(), "nothing tracked, nothing read")
}

// TestWebhookClosedDeliveryReportsOutcome: a signed pull_request "closed"
// delivery for a tracked PR becomes an outcome (merged or closed), handed to
// Config.Outcome by Handler.Run and never to Emit; an untracked PR's
// delivery yields nothing.
func TestWebhookClosedDeliveryReportsOutcome(t *testing.T) {
	const secret = "0123456789abcdef0123"
	state := OutcomeOpen
	w, _, rec := newOutcomeWatcher(t, &state)
	h, err := NewHandler(secret, w)
	require.NoError(t, err)

	merged := []byte(`{"action":"closed","pull_request":{"html_url":"` + prURL + `","number":7,"merged":true,"merged_at":"2026-11-20T10:00:00Z","head":{"sha":"aaa","ref":"feat"}}}`)
	assert.Equal(t, http.StatusAccepted, post(h, "pull_request", sign(secret, merged), merged))
	queued := drain(h)
	require.Len(t, queued, 1)
	assert.Equal(t, kindOutcome, queued[0].Kind)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); h.Run(ctx) }()
	h.queue <- queued[0]
	require.Eventually(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.got) == 1
	}, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done
	got := rec.take()
	assert.Equal(t, Outcome{PRURL: prURL, Identifier: "ENG-7", State: OutcomeMerged,
		At: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC), Source: SourceWebhook}, got[0])

	other := []byte(`{"action":"closed","pull_request":{"html_url":"https://github.com/acme/app/pull/8","number":8,"merged":true}}`)
	assert.Equal(t, http.StatusAccepted, post(h, "pull_request", sign(secret, other), other))
	assert.Empty(t, drain(h), "an untracked PR yields nothing")

	closed := []byte(`{"action":"closed","pull_request":{"html_url":"` + prURL + `","number":7,"merged":false}}`)
	assert.Equal(t, http.StatusAccepted, post(h, "pull_request", sign(secret, closed), closed))
	queued = drain(h)
	require.Len(t, queued, 1)
	assert.Equal(t, OutcomeClosed, queued[0].Outcome)
}
