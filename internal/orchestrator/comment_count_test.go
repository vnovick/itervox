package orchestrator

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
)

// T-6 acceptance: BumpCommentCount must be safe under concurrent calls and
// produce a monotonic counter. The orchestrator stores per-identifier counts
// (not per-run) so that consecutive runs on the same issue are merged at
// snapshot time and reset at run-completion time.
func TestBumpCommentCountIsConcurrentSafe(t *testing.T) {
	o := &Orchestrator{}
	const goroutines = 32
	const bumpsEach = 10

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range bumpsEach {
				o.BumpCommentCount("ENG-RACE")
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, goroutines*bumpsEach, o.CommentCountFor("ENG-RACE"),
		"counter must be exact under -race; missing increments indicate a missing lock")
	assert.Zero(t, o.CommentCountFor("ENG-OTHER"),
		"counter is per-identifier, not global")
}

// Empty identifiers are silently dropped to avoid polluting the map with a
// "" entry that masks bugs in upstream code that fails to populate it.
func TestBumpCommentCountIgnoresEmptyIdentifier(t *testing.T) {
	o := &Orchestrator{}
	o.BumpCommentCount("")
	assert.Zero(t, o.CommentCountFor(""), "empty identifier must be ignored")
}

// recordHistory must merge the orchestrator counter into the resulting
// CompletedRun and then reset the counter so the next run starts fresh.
func TestRecordHistoryMergesAndResetsCommentCount(t *testing.T) {
	o := &Orchestrator{}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-7", Title: "Test"}

	// Three comments accrued during the run via the HTTP handler counter.
	o.BumpCommentCount("ENG-7")
	o.BumpCommentCount("ENG-7")
	o.BumpCommentCount("ENG-7")

	live := &RunEntry{
		Issue:     issue,
		Kind:      "reviewer",
		StartedAt: time.Now().Add(-time.Minute),
	}
	o.recordHistory(live, issue, time.Now(), "succeeded")
	hist := o.RunHistory()
	require.Len(t, hist, 1)
	assert.Equal(t, 3, hist[0].CommentCount,
		"recordHistory must merge the orchestrator counter into the completed run")
	assert.Zero(t, o.CommentCountFor("ENG-7"),
		"recordHistory must reset the counter so the next run starts fresh")
}

func TestRecordHistoryPrefersLargerCounterValue(t *testing.T) {
	// If a unit test passes a CommentCount directly on the live RunEntry but
	// the orchestrator counter is also populated (real production case), the
	// helper must take the larger of the two to avoid silently dropping
	// counts.
	o := &Orchestrator{}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-8"}
	o.BumpCommentCount("ENG-8")
	o.BumpCommentCount("ENG-8")
	live := &RunEntry{
		Issue:        issue,
		Kind:         "worker",
		CommentCount: 5,
		StartedAt:    time.Now().Add(-time.Minute),
	}
	o.recordHistory(live, issue, time.Now(), "succeeded")
	hist := o.RunHistory()
	require.Len(t, hist, 1)
	assert.Equal(t, 5, hist[0].CommentCount,
		"larger explicit liveEntry value must win over the orchestrator counter")
}
