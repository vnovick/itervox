package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// issuesWithQuestion builds n input-required issues, each carrying an Itervox
// question comment, so the reply-check path has real comment data to walk.
// A fixture without comments would exercise the fetch and then return early,
// which is the "fixtures shaped unlike production" failure this repo has hit
// four times — the test would pass whether or not batching worked.
func issuesWithQuestion(n int) []domain.Issue {
	out := make([]domain.Issue, 0, n)
	created := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		out = append(out, domain.Issue{
			ID:         "id-" + id,
			Identifier: "ENG-" + id,
			State:      "In Progress",
			Comments: []domain.Comment{{
				ID:        "q-" + id,
				Body:      "<!-- itervox:needs-input -->\nWhat now?",
				AuthorID:  "bot",
				CreatedAt: &created,
			}},
		})
	}
	return out
}

func inputRequiredState(issues []domain.Issue) State {
	entries := make(map[string]*InputRequiredEntry, len(issues))
	for _, iss := range issues {
		entries[iss.Identifier] = &InputRequiredEntry{
			IssueID:           iss.ID,
			Identifier:        iss.Identifier,
			QuestionCommentID: iss.Comments[0].ID,
			QuestionAuthorID:  iss.Comments[0].AuthorID,
			QueuedAt:          time.Now().Add(-2 * time.Hour),
		}
	}
	return State{
		InputRequiredIssues: entries,
		PendingInputResumes: map[string]*PendingInputResumeEntry{},
	}
}

// TestCheckTrackerRepliesBatchesDetailFetches pins issue #42's headline fix for
// this path: N input-required entries must cost ONE tracker request, not N.
//
// The assertion that matters is DetailCalls() == 0. A batch that fires
// alongside N per-issue fetches has saved nothing, so counting only the batch
// would pass while the bug was fully present.
func TestCheckTrackerRepliesBatchesDetailFetches(t *testing.T) {
	issues := issuesWithQuestion(3)
	tr := tracker.NewMemoryTracker(issues, []string{"In Progress"}, []string{"Done"})
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 16)}

	o.checkTrackerReplies(context.Background(), inputRequiredState(issues))

	assert.Equal(t, 1, tr.DetailBatchCalls(),
		"three entries must collapse into one batched request")
	assert.Equal(t, 0, tr.DetailCalls(),
		"no per-issue fetch may survive the batch — that is the whole saving")
}

// TestCheckTrackerRepliesFallsBackWhenBatchUnsupported pins that a tracker
// without DetailBatcher keeps working. The optional-interface pattern is only
// safe if the fallback is real, and a silent no-op here would stop every
// input-required issue from ever being resumed.
func TestCheckTrackerRepliesFallsBackWhenBatchUnsupported(t *testing.T) {
	issues := issuesWithQuestion(3)
	tr := &nonBatchingTracker{Tracker: tracker.NewMemoryTracker(issues, []string{"In Progress"}, []string{"Done"})}
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 16)}

	o.checkTrackerReplies(context.Background(), inputRequiredState(issues))

	assert.Equal(t, 3, tr.detailCalls,
		"a tracker that cannot batch must still fetch every entry per-issue")
}

// TestPrefetchDetailsFallsBackOnBatchError pins that a failing batch degrades
// to the per-issue path rather than dropping the tick. Returning the error
// upward would skip the reply check entirely, so an answered question would
// never be noticed — strictly worse than spending the requests.
func TestPrefetchDetailsFallsBackOnBatchError(t *testing.T) {
	tr := tracker.NewMemoryTracker(issuesWithQuestion(3), []string{"In Progress"}, []string{"Done"})
	tr.InjectError(errors.New("linear: 429 rate limited"))

	got := tracker.PrefetchDetails(context.Background(), tr, []string{"id-a", "id-b", "id-c"})

	assert.Nil(t, got, "a failed batch must yield nil so callers take the per-issue path")
}

// TestPrefetchDetailsSkipsSingletonBatch pins that one id does not go through
// the batch query: it costs the same single request as FetchIssueDetail, so
// batching it only adds a second code path to trust.
func TestPrefetchDetailsSkipsSingletonBatch(t *testing.T) {
	tr := tracker.NewMemoryTracker(issuesWithQuestion(1), []string{"In Progress"}, []string{"Done"})

	got := tracker.PrefetchDetails(context.Background(), tr, []string{"id-a"})

	assert.Nil(t, got)
	assert.Equal(t, 0, tr.DetailBatchCalls(), "a single id must not issue a batch request")
}

// TestPrefetchDetailsOmittedIDIsNotDeletion pins the contract that an id absent
// from a batch response must not be reported as anything at all — the caller
// has to fall through to an authoritative single fetch. Treating absence as
// deletion is how the dependency-audit refresh once retired live rows.
func TestPrefetchDetailsOmittedIDIsNotDeletion(t *testing.T) {
	// Only id-a exists; id-zz is asked for but cannot be returned.
	tr := tracker.NewMemoryTracker(issuesWithQuestion(1), []string{"In Progress"}, []string{"Done"})

	got := tracker.PrefetchDetails(context.Background(), tr, []string{"id-a", "id-zz"})

	require.NotNil(t, got)
	assert.NotNil(t, got["id-a"], "a present id is returned")
	_, present := got["id-zz"]
	assert.False(t, present,
		"an absent id must be missing from the map, never a nil entry a caller could read as 'gone'")
}

// nonBatchingTracker wraps a Tracker and hides DetailBatcher, modelling an
// adapter like GitHub whose REST API cannot batch.
type nonBatchingTracker struct {
	tracker.Tracker
	detailCalls int
}

func (n *nonBatchingTracker) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	n.detailCalls++
	return n.Tracker.FetchIssueDetail(ctx, issueID)
}
