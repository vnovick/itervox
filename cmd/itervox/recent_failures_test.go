package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-046 (cmd/itervox side): the snapshot's recentFailures wire field, the
// outbox flusher as a ring producer, and the reload carry-over.

func TestRecentFailureRowsAlwaysArrayAndRedacted(t *testing.T) {
	empty := recentFailureRows(orchestrator.State{})
	require.NotNil(t, empty, "never null on the wire: jq '.recentFailures | type' must be \"array\"")
	data, err := json.Marshal(server.StateSnapshot{RecentFailures: empty})
	require.NoError(t, err)
	assert.Contains(t, string(data), `"recentFailures":[]`)

	secret := "sk-proj-" + strings.Repeat("Ab1", 15)
	at := time.Date(2026, 9, 26, 1, 2, 3, 0, time.UTC)
	rows := recentFailureRows(orchestrator.State{RecentFailures: []orchestrator.FailureRecord{{
		Kind: orchestrator.FailureKindOutbox, Identifier: "ENG-1", Source: "comment",
		Message: "post failed: key " + secret, OccurredAt: at, RecordedAt: at.Add(time.Second), Count: 2,
	}}})
	require.Len(t, rows, 1)
	assert.NotContains(t, rows[0].Message, secret, "the wire field never carries an unredacted secret")
	assert.Equal(t, "outbox", rows[0].Kind)
	assert.Equal(t, "ENG-1", rows[0].Identifier)
	assert.Equal(t, 2, rows[0].Count)
	assert.Equal(t, at, rows[0].OccurredAt)
}

type recordingFailureRefresher struct {
	fakeRefresher
	mu       sync.Mutex
	failures []orchestrator.FailureRecord
}

func (r *recordingFailureRefresher) RecordFailure(f orchestrator.FailureRecord) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, f)
	return true
}

type rateLimitedUpdateTracker struct{ *recordingFlusherTracker }

func (r rateLimitedUpdateTracker) UpdateIssueState(context.Context, string, string) error {
	return &tracker.RateLimitedError{ResetAt: time.Now().Add(time.Minute)}
}

func TestOutboxFlushFailureRecordsRecentFailure(t *testing.T) {
	ob := newTestOutbox(t)
	tr := newRecordingFlusherTracker([]domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "In Review"}})
	tr.failNextUpdate("id-a", 1)
	rec := &recordingFailureRefresher{}
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	runOutboxFlusherTick(context.Background(), ob, tr, rec, time.Now())
	require.Len(t, rec.failures, 1)
	f := rec.failures[0]
	assert.Equal(t, orchestrator.FailureKindOutbox, f.Kind)
	assert.Equal(t, "ENG-A", f.Identifier)
	assert.Equal(t, string(outbox.KindUpdateState), f.Source)
	assert.Contains(t, f.Message, "injected update failure")
}

func TestOutboxRateLimitDeferralIsNotAFailure(t *testing.T) {
	ob := newTestOutbox(t)
	tr := rateLimitedUpdateTracker{newRecordingFlusherTracker([]domain.Issue{{ID: "id-a", Identifier: "ENG-A"}})}
	rec := &recordingFailureRefresher{}
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	runOutboxFlusherTick(context.Background(), ob, tr, rec, time.Now())
	assert.Empty(t, rec.failures, "a rate-limit deferral is by design not a failure")
}

func TestRecentFailuresCarryAcrossReload(t *testing.T) {
	var c recentFailuresCarry
	none, noAcks := c.take()
	assert.Nil(t, none)
	assert.Nil(t, noAcks)
	ackAt := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	c.store([]orchestrator.FailureRecord{{Kind: orchestrator.FailureKindPersist, Message: "a"}}, map[string]time.Time{"ENG-1": ackAt})
	got, acks := c.take()
	assert.Equal(t, map[string]time.Time{"ENG-1": ackAt}, acks, "the acks travel with the ring (M6-close V3)")
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Message)
	again, againAcks := c.take()
	assert.Nil(t, again, "take hands the ring over once")
	assert.Nil(t, againAcks)
}

func TestClientErrorReporterFeedsRecentFailures(t *testing.T) {
	rec := &recordingFailureRefresher{}
	ok := clientErrorReporter(rec)(server.ClientErrorReport{Kind: "render", Message: "boom", Route: "/settings"})
	require.True(t, ok)
	require.Len(t, rec.failures, 1)
	f := rec.failures[0]
	assert.Equal(t, orchestrator.FailureKindClient, f.Kind)
	assert.Equal(t, "render", f.Source)
	assert.Equal(t, "boom (route /settings)", f.Message)
}
