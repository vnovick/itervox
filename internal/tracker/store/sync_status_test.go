package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker/store"
)

// TestSyncStatusTracksPullsAndFailureRuns pins #117's store half: the last
// good pull time, the error of the newest failed pull with its time, and the
// count of failures since the last good pull, which a good pull resets.
func TestSyncStatusTracksPullsAndFailureRuns(t *testing.T) {
	up := newCounting(issue("1", "Todo"))
	cfg := storeConfig(t, t.TempDir())
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	cfg.Now = func() time.Time { return now }
	_, s, err := store.Wrap(up, cfg)
	require.NoError(t, err)

	st := s.SyncStatus()
	assert.True(t, st.SyncedAt.IsZero(), "a cold store has never pulled")
	assert.Equal(t, time.Hour, st.Interval)

	require.NoError(t, s.RequestSyncNowForTest(t.Context()))
	pulledAt := now
	st = s.SyncStatus()
	assert.Equal(t, pulledAt, st.SyncedAt)
	assert.NoError(t, st.LastErr)
	assert.Zero(t, st.Failures)

	up.fail.Store(true)
	now = now.Add(time.Minute)
	require.Error(t, s.RequestSyncNowForTest(t.Context()))
	now = now.Add(time.Minute)
	require.Error(t, s.RequestSyncNowForTest(t.Context()))
	st = s.SyncStatus()
	assert.Equal(t, pulledAt, st.SyncedAt, "a failed pull keeps the last good pull time")
	assert.ErrorIs(t, st.LastErr, errDown)
	assert.Equal(t, now, st.LastErrAt)
	assert.Equal(t, 2, st.Failures)

	up.fail.Store(false)
	now = now.Add(time.Minute)
	require.NoError(t, s.RequestSyncNowForTest(t.Context()))
	st = s.SyncStatus()
	assert.Equal(t, now, st.SyncedAt)
	assert.NoError(t, st.LastErr)
	assert.Zero(t, st.Failures, "a good pull ends the failure run")
}
