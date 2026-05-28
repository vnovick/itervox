package tracker_test

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

func makeIssue(id, identifier, state string) domain.Issue {
	return domain.Issue{
		ID:         id,
		Identifier: identifier,
		Title:      "Issue " + identifier,
		State:      state,
	}
}

func TestMemoryTrackerFetchCandidateIssues(t *testing.T) {
	issues := []domain.Issue{
		makeIssue("1", "ENG-1", "Todo"),
		makeIssue("2", "ENG-2", "In Progress"),
		makeIssue("3", "ENG-3", "Done"),
	}
	mem := tracker.NewMemoryTracker(issues, []string{"Todo", "In Progress"}, []string{"Done"})

	candidates, err := mem.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, candidates, 2)
	ids := []string{candidates[0].ID, candidates[1].ID}
	assert.ElementsMatch(t, []string{"1", "2"}, ids)
}

func TestMemoryTrackerFetchIssuesByStates(t *testing.T) {
	issues := []domain.Issue{
		makeIssue("1", "ENG-1", "Todo"),
		makeIssue("2", "ENG-2", "Done"),
		makeIssue("3", "ENG-3", "Cancelled"),
	}
	mem := tracker.NewMemoryTracker(issues, []string{"Todo"}, []string{"Done", "Cancelled"})

	result, err := mem.FetchIssuesByStates(context.Background(), []string{"Done", "Cancelled"})
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestMemoryTrackerFetchIssuesByStatesEmptyReturnsEmpty(t *testing.T) {
	mem := tracker.NewMemoryTracker(nil, nil, nil)
	result, err := mem.FetchIssuesByStates(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestMemoryTrackerFetchIssueStatesByIDs(t *testing.T) {
	issues := []domain.Issue{
		makeIssue("id-1", "ENG-1", "Todo"),
		makeIssue("id-2", "ENG-2", "In Progress"),
	}
	mem := tracker.NewMemoryTracker(issues, []string{"Todo", "In Progress"}, nil)

	result, err := mem.FetchIssueStatesByIDs(context.Background(), []string{"id-1"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "id-1", result[0].ID)
	assert.Equal(t, "Todo", result[0].State)
}

func TestMemoryTrackerFetchIssueStatesByIDsEmptyReturnsEmpty(t *testing.T) {
	mem := tracker.NewMemoryTracker(nil, nil, nil)
	result, err := mem.FetchIssueStatesByIDs(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestMemoryTrackerUpdateIssueState(t *testing.T) {
	issues := []domain.Issue{
		makeIssue("id-1", "ENG-1", "Todo"),
	}
	mem := tracker.NewMemoryTracker(issues, []string{"Todo", "In Progress"}, []string{"Done"})

	mem.SetIssueState("id-1", "Done")

	result, err := mem.FetchIssueStatesByIDs(context.Background(), []string{"id-1"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "Done", result[0].State)

	// No longer a candidate
	candidates, err := mem.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Empty(t, candidates)
}

func TestMemoryTrackerFetchCandidateIssuesCaseInsensitive(t *testing.T) {
	issues := []domain.Issue{
		makeIssue("1", "ENG-1", "todo"),
		makeIssue("2", "ENG-2", "IN PROGRESS"),
	}
	mem := tracker.NewMemoryTracker(issues, []string{"Todo", "In Progress"}, nil)
	candidates, err := mem.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, candidates, 2)
}

func TestMemoryTrackerInjectError(t *testing.T) {
	mem := tracker.NewMemoryTracker(nil, nil, nil)
	mem.InjectError(assert.AnError)
	_, err := mem.FetchCandidateIssues(context.Background())
	assert.Error(t, err)
	_, err = mem.FetchIssuesByStates(context.Background(), []string{"Todo"})
	assert.Error(t, err)
	_, err = mem.FetchIssueStatesByIDs(context.Background(), []string{"id-1"})
	assert.Error(t, err)
}

func TestMemoryTrackerCreateComment(t *testing.T) {
	mem := tracker.NewMemoryTracker(nil, nil, nil)
	comment, err := mem.CreateComment(context.Background(), "id-1", "a comment")
	require.NoError(t, err)
	require.NotNil(t, comment)
	assert.NotEmpty(t, comment.ID)
	assert.Equal(t, "a comment", comment.Body)
	assert.Equal(t, "memory-tracker", comment.AuthorID)
}

func TestMemoryTrackerUpdateIssueStateViaMethod(t *testing.T) {
	issues := []domain.Issue{makeIssue("id-1", "ENG-1", "Todo")}
	mem := tracker.NewMemoryTracker(issues, []string{"Todo"}, []string{"Done"})

	err := mem.UpdateIssueState(context.Background(), "id-1", "Done")
	require.NoError(t, err)

	result, err := mem.FetchIssueStatesByIDs(context.Background(), []string{"id-1"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "Done", result[0].State)
}

func TestMemoryTrackerSetIssueBranch(t *testing.T) {
	issues := []domain.Issue{makeIssue("id-1", "ENG-1", "In Progress")}
	mem := tracker.NewMemoryTracker(issues, []string{"In Progress"}, nil)

	err := mem.SetIssueBranch(context.Background(), "id-1", "feature/branch")
	require.NoError(t, err)

	// Verify via FetchIssueDetail.
	issue, err := mem.FetchIssueDetail(context.Background(), "id-1")
	require.NoError(t, err)
	require.NotNil(t, issue.BranchName)
	assert.Equal(t, "feature/branch", *issue.BranchName)
}

func TestMemoryTrackerSetIssueBranchUnknownID(t *testing.T) {
	mem := tracker.NewMemoryTracker(nil, nil, nil)
	// Non-fatal: should not error when issue is not found.
	err := mem.SetIssueBranch(context.Background(), "missing", "branch")
	require.NoError(t, err)
}

func TestMemoryTrackerCreateIssue(t *testing.T) {
	issues := []domain.Issue{makeIssue("id-1", "ENG-1", "In Progress")}
	mem := tracker.NewMemoryTracker(issues, []string{"Todo", "In Progress"}, []string{"Done"})

	created, err := mem.CreateIssue(context.Background(), "id-1", "Follow-up", "Add regression test", "Todo")
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "Follow-up", created.Title)
	assert.Equal(t, "Todo", created.State)
	require.NotNil(t, created.Description)
	assert.Equal(t, "Add regression test", *created.Description)

	fetched, err := mem.FetchIssueByIdentifier(context.Background(), created.Identifier)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, created.Identifier, fetched.Identifier)
	assert.Equal(t, "Todo", fetched.State)
}

func TestMemoryTrackerCreateIssueUsesMaxExistingSuffix(t *testing.T) {
	issues := []domain.Issue{
		makeIssue("id-2", "ENG-2", "Todo"),
		makeIssue("id-9", "ENG-9", "Todo"),
	}
	mem := tracker.NewMemoryTracker(issues, []string{"Todo"}, []string{"Done"})

	created, err := mem.CreateIssue(context.Background(), "id-9", "Follow-up", "", "Todo")
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "id-10", created.ID)
	assert.Equal(t, "ENG-10", created.Identifier)
}

func TestMemoryTrackerCreateCommentPersistsOnIssue(t *testing.T) {
	issues := []domain.Issue{makeIssue("id-1", "ENG-1", "Todo")}
	mem := tracker.NewMemoryTracker(issues, nil, nil)

	comment, err := mem.CreateComment(context.Background(), "id-1", "a comment")
	require.NoError(t, err)
	require.NotNil(t, comment)

	issue, err := mem.FetchIssueDetail(context.Background(), "id-1")
	require.NoError(t, err)
	require.Len(t, issue.Comments, 1)
	assert.Equal(t, comment.ID, issue.Comments[0].ID)
	assert.Equal(t, "a comment", issue.Comments[0].Body)
}

func TestMemoryTrackerFetchIssueDetail(t *testing.T) {
	issues := []domain.Issue{makeIssue("id-1", "ENG-1", "Todo")}
	mem := tracker.NewMemoryTracker(issues, nil, nil)

	issue, err := mem.FetchIssueDetail(context.Background(), "id-1")
	require.NoError(t, err)
	assert.Equal(t, "id-1", issue.ID)
	assert.Equal(t, "ENG-1", issue.Identifier)
}

func TestMemoryTrackerFetchIssueDetailNotFound(t *testing.T) {
	mem := tracker.NewMemoryTracker(nil, nil, nil)
	_, err := mem.FetchIssueDetail(context.Background(), "missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, tracker.ErrNotFound))
}

// --- ParseTime and ToIntVal tests ---

func TestParseTimeValidRFC3339(t *testing.T) {
	s := "2024-01-15T10:30:00Z"
	got := tracker.ParseTime(s)
	require.NotNil(t, got)
	assert.Equal(t, 2024, got.Year())
	assert.Equal(t, time.January, got.Month())
	assert.Equal(t, 15, got.Day())
}

func TestParseTimeNilForNonString(t *testing.T) {
	assert.Nil(t, tracker.ParseTime(nil))
	assert.Nil(t, tracker.ParseTime(42))
	assert.Nil(t, tracker.ParseTime(3.14))
}

func TestParseTimeNilForEmptyString(t *testing.T) {
	assert.Nil(t, tracker.ParseTime(""))
}

func TestParseTimeNilForMalformed(t *testing.T) {
	assert.Nil(t, tracker.ParseTime("not-a-timestamp"))
}

func TestToIntValInt(t *testing.T) {
	v, ok := tracker.ToIntVal(int(7))
	assert.True(t, ok)
	assert.Equal(t, 7, v)
}

func TestToIntValInt64(t *testing.T) {
	v, ok := tracker.ToIntVal(int64(42))
	assert.True(t, ok)
	assert.Equal(t, 42, v)
}

func TestToIntValFloat64(t *testing.T) {
	v, ok := tracker.ToIntVal(float64(3))
	assert.True(t, ok)
	assert.Equal(t, 3, v)
}

func TestToIntValFalseForString(t *testing.T) {
	_, ok := tracker.ToIntVal("7")
	assert.False(t, ok)
}

func TestToIntValFalseForNil(t *testing.T) {
	_, ok := tracker.ToIntVal(nil)
	assert.False(t, ok)
}

// v0.2.0 audit P3-2 — FetchIssueDetail / FetchIssueByIdentifier must
// deep-copy slice fields so callers cannot mutate the in-memory store.
// Without the deep-copy, a test appending to the returned issue's Labels
// slice would corrupt the store for subsequent fetches in the same test.
func TestMemoryTrackerFetchIssueDetailDeepCopies(t *testing.T) {
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{{
			ID:         "id-1",
			Identifier: "ENG-1",
			State:      "Todo",
			Labels:     []string{"bug"},
			Comments:   []domain.Comment{{ID: "c-1", AuthorID: "alice", Body: "first"}},
		}},
		[]string{"Todo"},
		[]string{"Done"},
	)

	first, err := mt.FetchIssueDetail(context.Background(), "id-1")
	require.NoError(t, err)
	require.NotNil(t, first)

	first.Labels = append(first.Labels, "tampered")
	first.Comments = append(first.Comments, domain.Comment{ID: "c-2", AuthorID: "mallory", Body: "tamper"})

	second, err := mt.FetchIssueDetail(context.Background(), "id-1")
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, []string{"bug"}, second.Labels, "store Labels should be unaffected by caller mutation")
	assert.Len(t, second.Comments, 1, "store Comments should be unaffected by caller mutation")
}

func TestMemoryTrackerFetchIssueByIdentifierDeepCopies(t *testing.T) {
	blockerID := "blk-1"
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{{
			ID:         "id-1",
			Identifier: "ENG-1",
			State:      "Todo",
			BlockedBy:  []domain.BlockerRef{{ID: &blockerID}},
		}},
		[]string{"Todo"},
		[]string{"Done"},
	)

	first, err := mt.FetchIssueByIdentifier(context.Background(), "ENG-1")
	require.NoError(t, err)
	tamperedID := "tampered"
	first.BlockedBy = append(first.BlockedBy, domain.BlockerRef{ID: &tamperedID})

	second, err := mt.FetchIssueByIdentifier(context.Background(), "ENG-1")
	require.NoError(t, err)
	assert.Len(t, second.BlockedBy, 1, "store BlockedBy should be unaffected by caller mutation")
}
