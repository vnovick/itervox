package tracker_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

func TestMarkCommentKeyIsIdempotent(t *testing.T) {
	once := tracker.MarkCommentKey("body", "k1")
	twice := tracker.MarkCommentKey(once, "k1")
	assert.Equal(t, once, twice, "marking twice must not append a second marker")
	assert.True(t, tracker.CommentHasKey(once, "k1"))
	assert.False(t, tracker.CommentHasKey(once, "k2"))
}

func TestMarkCommentKeyCoexistsWithManagedMarker(t *testing.T) {
	body := tracker.MarkManagedComment(tracker.MarkCommentKey("body", "k1"))
	assert.True(t, tracker.CommentHasKey(body, "k1"))
	assert.True(t, tracker.IsManagedComment(domain.Comment{Body: body}))
}
