package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker"
)

func feedSeqs(events []FeedEvent) []int64 {
	out := make([]int64, 0, len(events))
	for _, e := range events {
		out = append(out, e.Seq)
	}
	return out
}

// TestFeedSeqMonotonicAndSinceReturnsOnlyNewer (#213): only the loop
// assigns seq — in-loop emits and off-loop RecordFeedEvent alike — seqs
// increase by one per event, and a cursor read returns exactly the events
// after since, paged by limit with a resumable nextSeq.
func TestFeedSeqMonotonicAndSinceReturnsOnlyNewer(t *testing.T) {
	cfg := automationBaseCfg()
	o := New(cfg, tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), nil, nil)
	state := NewState(cfg)
	for _, id := range []string{"ENG-1", "ENG-2", "ENG-3"} {
		o.emitFeed(&state, FeedRunStarted, id, FeedPayload{RunID: "r-" + id})
	}
	// An off-loop producer only sends; the loop assigns the seq.
	require.True(t, o.RecordFeedEvent(FeedEvent{Seq: 999, Kind: FeedPRMerged, Identifier: "ENG-1", Payload: FeedPayload{PRURL: "https://github.com/a/b/pull/1"}}))
	assert.Equal(t, int64(3), state.FeedSeq, "RecordFeedEvent must not touch State")
	state = o.handleEvent(context.Background(), state, <-o.events)
	o.emitFeed(&state, FeedRunFinished, "ENG-2", FeedPayload{Outcome: "succeeded"})
	o.storeSnap(state)

	all := o.Feed(0, 0)
	assert.Equal(t, []int64{1, 2, 3, 4, 5}, feedSeqs(all.Events))
	assert.Equal(t, FeedPRMerged, all.Events[3].Kind)
	assert.Equal(t, int64(4), all.Events[3].Seq, "the producer's seq is ignored")
	for i := 1; i < len(all.Events); i++ {
		assert.False(t, all.Events[i].TS.Before(all.Events[i-1].TS), "ts never goes backwards")
	}
	assert.Equal(t, int64(5), all.NextSeq)
	assert.Equal(t, int64(5), all.HeadSeq)
	assert.False(t, all.Truncated)
	assert.False(t, all.HasMore)

	newer := o.Feed(3, 0)
	assert.Equal(t, []int64{4, 5}, feedSeqs(newer.Events), "only events after since")
	assert.Equal(t, int64(5), newer.NextSeq)

	caughtUp := o.Feed(5, 0)
	assert.Empty(t, caughtUp.Events)
	assert.NotNil(t, caughtUp.Events, "always an array")
	assert.Equal(t, int64(5), caughtUp.NextSeq)
	assert.False(t, caughtUp.Truncated)

	// Paging: limit cuts the page, nextSeq resumes it.
	p1 := o.Feed(0, 2)
	assert.Equal(t, []int64{1, 2}, feedSeqs(p1.Events))
	assert.True(t, p1.HasMore)
	p2 := o.Feed(p1.NextSeq, 2)
	assert.Equal(t, []int64{3, 4}, feedSeqs(p2.Events))
	p3 := o.Feed(p2.NextSeq, 2)
	assert.Equal(t, []int64{5}, feedSeqs(p3.Events))
	assert.False(t, p3.HasMore)
	assert.Len(t, o.Feed(0, 5000).Events, 5, "limit is capped, not an error")
}

// TestFeedTruncatedWhenCursorOlderThanWindow (#213): the ring keeps the
// newest FeedCap events younger than FeedMaxAge; a cursor older than the
// window (or ahead of the head) is answered with truncated:true.
func TestFeedTruncatedWhenCursorOlderThanWindow(t *testing.T) {
	cfg := automationBaseCfg()
	state := NewState(cfg)
	now := time.Now()
	for range FeedCap + 10 {
		appendFeed(&state, FeedEvent{Kind: FeedRunStarted, Identifier: "ENG-1"}, now)
	}
	require.Len(t, state.Feed, FeedCap)
	assert.Equal(t, int64(11), state.Feed[0].Seq, "the oldest 10 left the window")
	head := state.FeedSeq

	assert.True(t, readFeed(state.Feed, head, 0, 0).Truncated, "events 1..10 are gone")
	assert.True(t, readFeed(state.Feed, head, 9, 0).Truncated, "event 10 is gone")
	at := readFeed(state.Feed, head, 10, 0)
	assert.False(t, at.Truncated, "nothing after 10 is missing")
	assert.Equal(t, int64(11), at.Events[0].Seq)
	assert.Equal(t, int64(11), at.OldestSeq)
	assert.True(t, readFeed(state.Feed, head, head+5, 0).Truncated, "a cursor from a feed this daemon no longer has")

	// Age: events older than FeedMaxAge leave the window on the next append.
	aged := NewState(cfg)
	appendFeed(&aged, FeedEvent{Kind: FeedRunStarted, TS: now.Add(-FeedMaxAge - time.Hour)}, now.Add(-FeedMaxAge-time.Hour))
	appendFeed(&aged, FeedEvent{Kind: FeedRunStarted, TS: now.Add(-FeedMaxAge - time.Minute)}, now.Add(-FeedMaxAge-time.Minute))
	appendFeed(&aged, FeedEvent{Kind: FeedRunFinished}, now)
	assert.Equal(t, []int64{3}, feedSeqs(aged.Feed))
	page := readFeed(aged.Feed, aged.FeedSeq, 1, 0)
	assert.True(t, page.Truncated)
	assert.Equal(t, []int64{3}, feedSeqs(page.Events))

	// Every event aged out: the seq survives, a stale cursor is truncated.
	empty := NewState(cfg)
	empty.FeedSeq = 40
	page = readFeed(empty.Feed, empty.FeedSeq, 12, 0)
	assert.True(t, page.Truncated)
	assert.Equal(t, int64(12), page.NextSeq)
	assert.False(t, readFeed(empty.Feed, empty.FeedSeq, 40, 0).Truncated)
}

// TestFeedSurvivesRestart (#213): the feed persists to feed.jsonl through
// the ledger writer; a new orchestrator's Run loads it, and seq continues
// after the last persisted one — also when every event has aged out.
func TestFeedSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feed.jsonl")
	cfg := automationBaseCfg()
	cfg.Polling.IntervalMs = 20
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	first := New(cfg, mt, nil, nil)
	first.SetFeedFile(path)
	state := NewState(cfg)
	first.emitFeed(&state, FeedRunStarted, "ENG-1", FeedPayload{RunID: "r1", Profile: "coder"})
	first.emitFeed(&state, FeedRunFinished, "ENG-1", FeedPayload{RunID: "r1", Outcome: "succeeded"})
	first.emitFeed(&state, FeedDispatchPaused, "", FeedPayload{Reason: "deploy"})
	require.NoError(t, first.ledger(ledgerFeed).flush())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 4, "a header and one line per event")
	assert.JSONEq(t, `{"version":1,"lastSeq":3}`, lines[0])

	second := New(cfg, mt, nil, nil)
	second.SetFeedFile(path)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = second.Run(ctx) }()
	require.Eventually(t, func() bool { return second.Feed(0, 0).HeadSeq == 3 }, 5*time.Second, 5*time.Millisecond)
	page := second.Feed(0, 0)
	assert.Equal(t, []int64{1, 2, 3}, feedSeqs(page.Events))
	assert.Equal(t, "deploy", page.Events[2].Payload.Reason)
	assert.Equal(t, "coder", page.Events[0].Payload.Profile)
	assert.False(t, page.Truncated)
	require.True(t, second.RecordFeedEvent(FeedEvent{Kind: FeedPRMerged, Identifier: "ENG-1"}))
	require.Eventually(t, func() bool { return second.Feed(0, 0).HeadSeq == 4 }, 5*time.Second, 5*time.Millisecond,
		"seq continues after the persisted one")
	cancel()
	<-done
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"seq":4`, "the shutdown flush wrote the new event")

	// Aged-out events are dropped on load; the seq continues after them.
	old := time.Now().Add(-FeedMaxAge - time.Hour).UTC().Format(time.RFC3339Nano)
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"lastSeq":41}`+"\n"+
		`{"seq":41,"ts":"`+old+`","kind":"run_started"}`+"\n"+"not json\n"), 0o644))
	third := New(cfg, mt, nil, nil)
	third.SetFeedFile(path)
	st := third.loadFeedFromDisk(NewState(cfg))
	assert.Empty(t, st.Feed)
	assert.Equal(t, int64(41), st.FeedSeq)
	third.emitFeed(&st, FeedRunStarted, "ENG-9", FeedPayload{})
	assert.Equal(t, int64(42), st.Feed[0].Seq)

	// A file written after every event aged out holds only the header: it
	// alone keeps the seq.
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"lastSeq":57}`+"\n"), 0o644))
	st = third.loadFeedFromDisk(NewState(cfg))
	assert.Empty(t, st.Feed)
	assert.Equal(t, int64(57), st.FeedSeq, "the header keeps the seq")
}

// TestFeedCloneSharesNoWritableIndex backs cloneAllowlist["State.Feed"]:
// a snapshot that shares the ring never sees a write, because the loop only
// appends past every published length and trims by re-slicing.
func TestFeedCloneSharesNoWritableIndex(t *testing.T) {
	cfg := automationBaseCfg()
	state := NewState(cfg)
	now := time.Now()
	for range 4 {
		appendFeed(&state, FeedEvent{Kind: FeedRunStarted}, now)
	}
	snap := state.Clone()
	before := append([]FeedEvent(nil), snap.Feed...)
	for range FeedCap + 3 { // appends, reallocations and front trims
		appendFeed(&state, FeedEvent{Kind: FeedRunFinished}, now)
	}
	assert.Equal(t, before, snap.Feed, "the published ring is unchanged")
}

// TestFeedPayloadIsSanitized: free text is redacted and bounded, labels are
// bounded, whatever a producer hands in.
func TestFeedPayloadIsSanitized(t *testing.T) {
	state := NewState(automationBaseCfg())
	ev := appendFeed(&state, FeedEvent{Kind: FeedDispatchPaused, Identifier: strings.Repeat("x", 500),
		Payload: FeedPayload{Reason: "token ghp_abcdefghijklmnopqrstuvwxyz0123456789 " + strings.Repeat("y", 4000)}}, time.Now())
	assert.LessOrEqual(t, len(ev.Identifier), 128)
	assert.NotContains(t, ev.Payload.Reason, "ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	assert.Less(t, len(ev.Payload.Reason), 4000)
}
