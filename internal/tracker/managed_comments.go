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
