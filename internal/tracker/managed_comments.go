package tracker

import (
	"strings"

	"github.com/vnovick/itervox/internal/domain"
)

const ManagedCommentMarker = "<!-- itervox:managed -->"

func MarkManagedComment(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || strings.Contains(trimmed, ManagedCommentMarker) {
		return trimmed
	}
	return trimmed + "\n\n" + ManagedCommentMarker
}

func IsManagedComment(comment domain.Comment) bool {
	return strings.Contains(comment.Body, ManagedCommentMarker)
}

// commentKeyMarkerPrefix is the hidden marker that carries a comment's
// idempotency key in its body. Used by adapters whose API cannot accept a
// client-supplied comment id (GitHub); Linear sends the key as the comment's
// own id instead and never needs this.
const commentKeyMarkerPrefix = "<!-- itervox:ck:"

// CommentKeyMarker renders the hidden marker for key.
func CommentKeyMarker(key string) string {
	return commentKeyMarkerPrefix + key + " -->"
}

// MarkCommentKey appends key's hidden marker to body unless it is already
// present. Idempotent, and order-independent with MarkManagedComment — a
// body may carry both markers.
func MarkCommentKey(body, key string) string {
	if key == "" {
		return body
	}
	marker := CommentKeyMarker(key)
	if strings.Contains(body, marker) {
		return body
	}
	return strings.TrimSpace(body) + "\n\n" + marker
}

// CommentHasKey reports whether body carries key's hidden marker.
func CommentHasKey(body, key string) bool {
	if key == "" {
		return false
	}
	return strings.Contains(body, CommentKeyMarker(key))
}
