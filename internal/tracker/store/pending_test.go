package store_test

import (
	"maps"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker/store"
)

// TestStoreOverlaysQueuedStateMoves (#115): a state move queued for push
// shows on every list read at once — it leaves and joins the candidate list
// and the views by its new state — while the store's own copy stays the
// tracker's (TrackerCopy), a pull does not undo the overlay, and the overlay
// ends when the move leaves the queue.
func TestStoreOverlaysQueuedStateMoves(t *testing.T) {
	up := newCounting(issue("1", "Todo"), issue("2", "In Progress"), issue("4", "Backlog"))
	tr, s, err := store.Wrap(up, storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	_, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)

	var mu sync.Mutex
	queued := map[string]string{"1": "Done", "4": "Todo", "2": "In Progress"}
	s.SetPendingStates(func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(queued)
	})

	cands, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-2", "ENG-4"}, identifiers(cands), "1 moved out of the active states, 4 into them")

	done, err := tr.FetchIssuesByStates(t.Context(), board)
	require.NoError(t, err)
	states := map[string]string{}
	for _, iss := range done {
		states[iss.Identifier] = iss.State
	}
	assert.Equal(t, map[string]string{"ENG-1": "Done", "ENG-2": "In Progress", "ENG-4": "Todo"}, states,
		"the board shows each queued move at once")

	copy1, found := s.TrackerCopy("1")
	require.True(t, found)
	assert.Equal(t, "Todo", copy1.State, "the store's own copy stays the tracker's")

	require.NoError(t, s.RequestSyncNowForTest(t.Context()))
	cands, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-2", "ENG-4"}, identifiers(cands), "a pull of the old tracker state does not undo the overlay")

	mu.Lock()
	queued = map[string]string{}
	mu.Unlock()
	cands, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1", "ENG-2"}, identifiers(cands), "once the move leaves the queue the tracker's state shows again")
}
