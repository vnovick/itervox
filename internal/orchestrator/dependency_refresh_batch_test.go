package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// countingBatchTracker records how many requests each path costs.
type countingBatchTracker struct {
	tracker.Tracker
	batchCalls  int
	detailCalls int
	batchIDs    []string
	issues      map[string]domain.Issue
	batchErr    error
	// detailErr, when set, makes the per-issue confirmation fail too — the
	// realistic shape when a tracker is having a bad time, as opposed to a
	// single deleted row.
	detailErr error
}

func (c *countingBatchTracker) FetchIssueStatesByIDs(_ context.Context, ids []string) ([]domain.Issue, error) {
	c.batchCalls++
	c.batchIDs = append(c.batchIDs, ids...)
	if c.batchErr != nil {
		return nil, c.batchErr
	}
	out := make([]domain.Issue, 0, len(ids))
	for _, id := range ids {
		if issue, ok := c.issues[id]; ok {
			out = append(out, issue)
		}
	}
	return out, nil
}

func (c *countingBatchTracker) FetchIssueDetail(_ context.Context, id string) (*domain.Issue, error) {
	c.detailCalls++
	if c.detailErr != nil {
		return nil, c.detailErr
	}
	if issue, ok := c.issues[id]; ok {
		return &issue, nil
	}
	return nil, tracker.ErrNotFound
}

// TestDependencyRefreshBatchesIDTargets pins the request reduction. The audit
// was the largest per-issue consumer of the tracker budget — issue #42
// measured it at ~28% of Linear traffic on a deployment that spent 35 minutes
// of every hour at zero remaining budget.
func TestDependencyRefreshBatchesIDTargets(t *testing.T) {
	tr := &countingBatchTracker{issues: map[string]domain.Issue{
		"id-1": {ID: "id-1", Identifier: "ENG-1", State: "Todo"},
		"id-2": {ID: "id-2", Identifier: "ENG-2", State: "Done"},
		"id-3": {ID: "id-3", Identifier: "ENG-3", State: "Todo"},
	}}
	o := &Orchestrator{tracker: tr}
	result := &DependencyRefreshResult{}

	o.fetchDependencyRefreshBatched(context.Background(), []dependencyRefreshTarget{
		{Key: "k1", IssueID: "id-1"}, {Key: "k2", IssueID: "id-2"}, {Key: "k3", IssueID: "id-3"},
	}, result)

	assert.Equal(t, 1, tr.batchCalls, "three rows must cost ONE request, not three")
	assert.Zero(t, tr.detailCalls, "the batch path must not fall back to per-issue fetches")
	require.Len(t, result.Issues, 3)
	assert.Empty(t, result.MissingKeys)
	assert.Empty(t, result.FailedKeys)
}

// TestDependencyRefreshBatchOmittedIDIsMissing is the load-bearing
// classification: an ID the tracker omits is DELETED, and its audit row must
// retire. Filing it as failed instead is what made the observed DEMO-* rows
// retry every refresh interval forever.
func TestDependencyRefreshBatchOmittedIDIsMissing(t *testing.T) {
	tr := &countingBatchTracker{issues: map[string]domain.Issue{
		"id-1": {ID: "id-1", Identifier: "ENG-1", State: "Todo"},
	}}
	o := &Orchestrator{tracker: tr}
	result := &DependencyRefreshResult{}

	o.fetchDependencyRefreshBatched(context.Background(), []dependencyRefreshTarget{
		{Key: "k1", IssueID: "id-1"}, {Key: "gone", IssueID: "demo-id-5"},
	}, result)

	assert.Equal(t, []string{"gone"}, result.MissingKeys,
		"a confirmed-missing ID must retire the row, not be retried forever")
	assert.Equal(t, 1, tr.detailCalls,
		"omission alone is not proof of deletion — it must be confirmed by an "+
			"authoritative single fetch before the row is retired")
	assert.Empty(t, result.FailedKeys)
	require.Len(t, result.Issues, 1)
}

// TestDependencyRefreshBatchErrorIsTransient — the opposite direction. A
// whole-batch failure (network, rate limit, auth) must NOT retire live rows.
func TestDependencyRefreshBatchErrorIsTransient(t *testing.T) {
	tr := &countingBatchTracker{batchErr: errors.New("rate limited"), detailErr: errors.New("rate limited")}
	o := &Orchestrator{tracker: tr}
	result := &DependencyRefreshResult{}

	o.fetchDependencyRefreshBatched(context.Background(), []dependencyRefreshTarget{
		{Key: "k1", IssueID: "id-1"}, {Key: "k2", IssueID: "id-2"},
	}, result)

	assert.ElementsMatch(t, []string{"k1", "k2"}, result.FailedKeys)
	assert.Empty(t, result.MissingKeys, "a transient failure must never delete an audit row")
	assert.Equal(t, 2, tr.detailCalls,
		"a whole-batch error must degrade to per-target fetches, not fail every row "+
			"together — on GitHub the batch fans out to single requests and returns on "+
			"the first failure, so one bad row would otherwise delay the whole batch")
}

// TestPartitionRefreshTargetsIsATruePartition — completeness proof: every
// target lands in exactly one bucket, so no row is fetched twice or dropped.
func TestPartitionRefreshTargetsIsATruePartition(t *testing.T) {
	targets := []dependencyRefreshTarget{
		{Key: "a", IssueID: "id-a"},
		{Key: "b", Identifier: "ENG-B"},
		{Key: "c", IssueID: "id-c", Identifier: "ENG-C"},
		{Key: "d"},
	}
	batched, perIssue := partitionRefreshTargets(targets)

	assert.Len(t, batched, 2, "targets with an IssueID batch")
	assert.Len(t, perIssue, 2, "identifier-only and empty targets do not")
	assert.Equal(t, len(targets), len(batched)+len(perIssue), "the buckets must sum to the input")

	seen := map[string]int{}
	for _, t2 := range append(append([]dependencyRefreshTarget{}, batched...), perIssue...) {
		seen[t2.Key]++
	}
	for _, tgt := range targets {
		assert.Equal(t, 1, seen[tgt.Key], "target %q must appear exactly once", tgt.Key)
	}
}

// TestDependencyRefreshBatchNotFoundConfirmsPerTarget covers the whole-batch
// ErrNotFound branch, which was live, load-bearing, and completely
// unprotected: reverting it to the original blanket-retire bug left the ENTIRE
// orchestrator suite green while every live audit row in a batch was silently
// deleted.
//
// Reachable in production: Linear's decodeError returns NotFoundError, and
// fetchByIDsPage propagates it as a whole-batch error. A not-found from a
// batch does not say WHICH id was bad, so it must never be applied to the
// whole set.
func TestDependencyRefreshBatchNotFoundConfirmsPerTarget(t *testing.T) {
	tr := &countingBatchTracker{
		batchErr: tracker.ErrNotFound,
		issues: map[string]domain.Issue{
			"id-live": {ID: "id-live", Identifier: "ENG-1", State: "Todo"},
		},
	}
	o := &Orchestrator{tracker: tr}
	result := &DependencyRefreshResult{}

	o.fetchDependencyRefreshBatched(context.Background(), []dependencyRefreshTarget{
		{Key: "live", IssueID: "id-live"},
		{Key: "dead", IssueID: "demo-id-5"},
	}, result)

	assert.Equal(t, []string{"dead"}, result.MissingKeys,
		"only the genuinely absent id may retire")
	require.Len(t, result.Issues, 1,
		"a batch-level not-found must NOT retire the rows that still exist")
	assert.Equal(t, "id-live", result.Issues[0].Issue.ID)
	assert.Empty(t, result.FailedKeys)
}

// TestDependencyRefreshDegradationIsBounded pins the request cap.
//
// Nothing in this path can tell a per-row failure from a systemic one, so an
// unbounded degradation turned one failed batch into one doomed request per
// target — 101 at the default batch size of 100 — every refresh cycle,
// against the tracker this batching exists to stop over-polling.
func TestDependencyRefreshDegradationIsBounded(t *testing.T) {
	tr := &countingBatchTracker{
		batchErr:  errors.New("revoked token"),
		detailErr: errors.New("revoked token"),
	}
	o := &Orchestrator{tracker: tr}
	result := &DependencyRefreshResult{}

	targets := make([]dependencyRefreshTarget, 100)
	for i := range targets {
		targets[i] = dependencyRefreshTarget{
			Key:     fmt.Sprintf("k%d", i),
			IssueID: fmt.Sprintf("id-%d", i),
		}
	}
	o.fetchDependencyRefreshBatched(context.Background(), targets, result)

	assert.Equal(t, dependencyRefreshDegradeBudget, tr.detailCalls,
		"a systemic failure must not fire one request per target")
	assert.Len(t, result.FailedKeys, len(targets),
		"every target is still accounted for and retried next cycle")
	assert.Empty(t, result.MissingKeys, "a systemic failure must never retire a row")
}

// TestDependencyRefreshNotFoundDegradationIsBounded pins the cap on the
// ErrNotFound branch specifically.
//
// The budget was applied only to the generic-error branch, leaving this one
// firing one confirmation per target — 101 requests at the default batch size
// of 100. Worse than its sibling, it does not converge: when the
// confirmations fail transiently every key returns to FailedKeys and the
// identical burst repeats next refresh cycle. Reachable in production —
// Linear maps a batch-level `Entity not found` to ErrNotFound.
func TestDependencyRefreshNotFoundDegradationIsBounded(t *testing.T) {
	tr := &countingBatchTracker{
		batchErr:  tracker.ErrNotFound,
		detailErr: errors.New("tracker unavailable"),
	}
	o := &Orchestrator{tracker: tr}
	result := &DependencyRefreshResult{}

	targets := make([]dependencyRefreshTarget, 100)
	for i := range targets {
		targets[i] = dependencyRefreshTarget{Key: fmt.Sprintf("k%d", i), IssueID: fmt.Sprintf("id-%d", i)}
	}
	o.fetchDependencyRefreshBatched(context.Background(), targets, result)

	assert.Equal(t, dependencyRefreshDegradeBudget, tr.detailCalls,
		"a batch-level not-found must not fire one confirmation per target")
	assert.Len(t, result.FailedKeys, len(targets), "every target is still accounted for")
	assert.Empty(t, result.MissingKeys, "a failed confirmation must never retire a row")
}

// TestBoundRefreshDegradationPartitions is the completeness proof for the
// shared helper: the two slices sum to the input and preserve order, so no
// target is confirmed twice or silently dropped.
func TestBoundRefreshDegradationPartitions(t *testing.T) {
	mk := func(n int) []dependencyRefreshTarget {
		out := make([]dependencyRefreshTarget, n)
		for i := range out {
			out[i] = dependencyRefreshTarget{Key: fmt.Sprintf("k%d", i)}
		}
		return out
	}
	for _, n := range []int{0, 1, dependencyRefreshDegradeBudget, dependencyRefreshDegradeBudget + 1, 100} {
		confirm, deferred := boundRefreshDegradation(mk(n))
		assert.LessOrEqual(t, len(confirm), dependencyRefreshDegradeBudget, "n=%d", n)
		assert.Equal(t, n, len(confirm)+len(deferred), "buckets must sum to the input, n=%d", n)
	}
}
