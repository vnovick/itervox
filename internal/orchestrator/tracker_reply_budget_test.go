package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSelectTrackerReplyCheckBatch pins the per-tick request budget on the
// input-required reply check.
//
// The loop used to range over every InputRequiredIssues entry and spend one
// FetchIssueDetail on each, every tick, with no budget, no ordering and no
// TTL — so its request rate scaled with the number of stuck issues. At the
// default 30s poll that is 120 ticks/hour, and issue #42 reported a backlog
// of 19 such issues: ~2,280 requests/hour from this loop alone, against
// Linear's documented 2,500/hour ceiling, before any real work.
//
// A fixed budget makes the cost constant in the backlog size, and ordering
// by LastReplyCheckAt keeps it fair: every entry is still reached within
// ceil(N/budget) ticks instead of some entries starving under Go's
// randomized map iteration.
func TestSelectTrackerReplyCheckBatch(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	entries := map[string]*InputRequiredEntry{
		"ENG-1": {Identifier: "ENG-1", LastReplyCheckAt: now.Add(-1 * time.Minute)},
		"ENG-2": {Identifier: "ENG-2"}, // never checked — must go first
		"ENG-3": {Identifier: "ENG-3", LastReplyCheckAt: now.Add(-10 * time.Minute)},
		"ENG-4": {Identifier: "ENG-4", LastReplyCheckAt: now.Add(-5 * time.Minute)},
	}

	batch := selectTrackerReplyCheckBatch(entries, 2)
	require.Len(t, batch, 2, "the budget is a hard cap on requests per tick")
	assert.Equal(t, []string{"ENG-2", "ENG-3"}, batch,
		"never-checked first, then least-recently-checked")

	all := selectTrackerReplyCheckBatch(entries, 10)
	assert.Equal(t, []string{"ENG-2", "ENG-3", "ENG-4", "ENG-1"}, all,
		"a budget above the entry count returns every entry, still ordered")
}

// TestSelectTrackerReplyCheckBatchIsDeterministic guards against Go's
// randomized map iteration leaking into dispatch behaviour: two entries with
// identical timestamps must always come back in the same order.
func TestSelectTrackerReplyCheckBatchIsDeterministic(t *testing.T) {
	same := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	entries := map[string]*InputRequiredEntry{
		"ENG-3": {Identifier: "ENG-3", LastReplyCheckAt: same},
		"ENG-1": {Identifier: "ENG-1", LastReplyCheckAt: same},
		"ENG-2": {Identifier: "ENG-2", LastReplyCheckAt: same},
	}
	for range 20 {
		assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"},
			selectTrackerReplyCheckBatch(entries, 3))
	}
}

// TestSelectTrackerReplyCheckBatchNilEntriesSkipped keeps a nil map value
// (possible after a concurrent delete on an earlier tick) from panicking the
// event loop.
func TestSelectTrackerReplyCheckBatchNilEntriesSkipped(t *testing.T) {
	entries := map[string]*InputRequiredEntry{"ENG-1": nil, "ENG-2": {Identifier: "ENG-2"}}
	assert.Equal(t, []string{"ENG-2"}, selectTrackerReplyCheckBatch(entries, 5))
}
