package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
)

// TestRehydratedQuestionKeepsItsCommentTime: an input-required entry
// rebuilt from the tracker's question comment (local state lost) waits
// since the comment was posted, so the "Needs you" wait does not restart
// on every daemon restart; without a usable comment time it falls back to
// now.
func TestRehydratedQuestionKeepsItsCommentTime(t *testing.T) {
	posted := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Second)
	issue := domain.Issue{ID: "id-1", Identifier: "ENG-1"}
	detailed := &domain.Issue{ID: "id-1", Identifier: "ENG-1", Comments: []domain.Comment{
		{ID: "c1", Body: itervoxCommentPrefix + "\n\nWhich database?", CreatedAt: &posted, AuthorID: "bot"},
	}}
	entry := recoverInputRequired(issue, detailed, nil)
	require.NotNil(t, entry)
	assert.True(t, entry.QueuedAt.Equal(posted), "waits since the question comment: %v", entry.QueuedAt)

	now := time.Now()
	assert.Equal(t, now, rehydratedQueuedAt(domain.Comment{}, now), "no comment time: now")
	future := now.Add(time.Hour)
	assert.Equal(t, now, rehydratedQueuedAt(domain.Comment{CreatedAt: &future}, now), "a clock-skewed future time: now")
}
