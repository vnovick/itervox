package server

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// disableNotifyCoalescing makes every Notify a delivery, so tests and the
// fan-out benchmark do not wait out the 250 ms window.
func disableNotifyCoalescing(s *Server) {
	s.bc.mu.Lock()
	s.bc.interval = 0
	s.bc.mu.Unlock()
}

// TestSSEClientsShareOneEncodingPerVersion (#210): five dashboard streams
// cost one snapshot build and one encode per version, every client writes
// the very same frame, and a frame handed out is never changed afterwards.
func TestSSEClientsShareOneEncodingPerVersion(t *testing.T) {
	var builds atomic.Int64
	s := New(Config{
		Snapshot: func() StateSnapshot {
			return StateSnapshot{MaxConcurrentAgents: int(builds.Add(1))}
		},
		RefreshChan: make(chan struct{}, 1),
	})
	disableNotifyCoalescing(s)
	const clients = 5
	writers, flushed, stop := startFanoutClients(t, s, clients)
	defer stop()

	assert.Equal(t, int64(1), builds.Load(), "the initial frames of one version share one build")
	first := bytes.Clone(writers[0].frame())
	require.Contains(t, string(first), `"maxConcurrentAgents":1`)

	for v := 2; v <= 4; v++ {
		s.Notify()
		for range clients {
			<-flushed
		}
		assert.Equal(t, int64(v), builds.Load(), "version %d: one build for %d clients", v, clients)
		frame := writers[0].frame()
		assert.Contains(t, string(frame), `"maxConcurrentAgents":`+string(rune('0'+v)), "clients get the newest state")
		for i, w := range writers {
			got := w.frame()
			require.True(t, bytes.Equal(frame, got), "client %d wrote a different frame", i)
			assert.Same(t, &frame[0], &got[0], "client %d got its own copy instead of the shared encoding", i)
		}
	}
	assert.Equal(t, uint64(4), s.frames.encodes)
	assert.True(t, bytes.HasPrefix(first, []byte("data: {")) && bytes.HasSuffix(first, []byte("}\n\n")), "an SSE data frame")

	// A frame handed out stays as it was: a later version is a new slice.
	assert.Contains(t, string(first), `"maxConcurrentAgents":1`)
}

// TestSSEFrameCacheExpiresForLateJoiners: a client that connects long after
// the last change gets a freshly built frame, not one built long ago.
func TestSSEFrameCacheExpiresForLateJoiners(t *testing.T) {
	var builds atomic.Int64
	s := New(Config{
		Snapshot:    func() StateSnapshot { builds.Add(1); return StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
	})
	_, err := s.sseFrame()
	require.NoError(t, err)
	_, err = s.sseFrame()
	require.NoError(t, err)
	assert.Equal(t, int64(1), builds.Load(), "same version, fresh frame: reused")

	s.frames.mu.Lock()
	s.frames.builtAt = time.Now().Add(-2 * frameMaxAge)
	s.frames.mu.Unlock()
	_, err = s.sseFrame()
	require.NoError(t, err)
	assert.Equal(t, int64(2), builds.Load(), "an old frame is rebuilt")
}

// TestNotifyCoalescesToFourHzWithTrailingEdge (#210): a burst of state
// changes reaches clients at most four times a second, the first change of
// a quiet period is delivered at once, and the last change of the burst is
// always delivered (trailing edge) within 250 ms of the notify.
func TestNotifyCoalescesToFourHzWithTrailingEdge(t *testing.T) {
	b := newBroadcaster()
	ch := b.subscribe()
	var mu sync.Mutex
	var deliveries []time.Time
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range ch {
			mu.Lock()
			deliveries = append(deliveries, time.Now())
			mu.Unlock()
		}
	}()
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(deliveries) }

	// Leading edge: the first change after a quiet period is not delayed.
	start := time.Now()
	b.notify()
	require.Eventually(t, func() bool { return count() == 1 }, 100*time.Millisecond, time.Millisecond)
	mu.Lock()
	assert.Less(t, deliveries[0].Sub(start), 50*time.Millisecond, "leading edge delivered at once")
	mu.Unlock()

	// A burst: 100 changes over ~1 s.
	for range 100 {
		b.notify()
		time.Sleep(10 * time.Millisecond)
	}
	last := time.Now()
	b.notify()
	lastVersion := b.currentVersion()

	// Trailing edge: the last change is delivered within the window.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return deliveries[len(deliveries)-1].After(last)
	}, notifyInterval+150*time.Millisecond, time.Millisecond, "the last change of a burst is delivered")
	assert.Greater(t, b.currentVersion(), lastVersion, "the trailing delivery starts a new version")

	mu.Lock()
	got := append([]time.Time(nil), deliveries...)
	mu.Unlock()
	elapsed := got[len(got)-1].Sub(start)
	maxAllowed := int(elapsed/notifyInterval) + 1
	assert.LessOrEqual(t, len(got), maxAllowed, "at most 4 deliveries per second (%d over %s)", len(got), elapsed)
	assert.GreaterOrEqual(t, len(got), 4, "a sustained burst keeps delivering, not only at its end")
	for i := 1; i < len(got); i++ {
		assert.GreaterOrEqual(t, got[i].Sub(got[i-1]), notifyInterval-20*time.Millisecond, "deliveries %d and %d too close", i-1, i)
	}

	// Nothing pending: no delivery without a change.
	n := count()
	time.Sleep(2 * notifyInterval)
	assert.Equal(t, n, count(), "no delivery without a change")

	b.close()
	b.unsubscribe(ch)
	close(ch)
	<-done
}
