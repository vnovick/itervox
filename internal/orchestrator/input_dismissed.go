package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// maxDismissedInputQuestions bounds State.DismissedInputQuestions; the
// oldest dismissal is dropped past it.
const maxDismissedInputQuestions = 500

// DismissedInputQuestion identifies the question an operator dismissed: the
// tracker comment (its id, or the idempotency key it was posted under) and,
// only when neither is known, a digest of the question text.
type DismissedInputQuestion struct {
	CommentID     string
	CommentKey    string
	ContextDigest string
	DismissedAt   time.Time
}

// recordDismissedInputQuestion stores the identity of entry's question.
// Event loop.
func recordDismissedInputQuestion(state *State, entry *InputRequiredEntry, now time.Time) {
	if entry == nil || entry.Identifier == "" {
		return
	}
	rec := DismissedInputQuestion{
		CommentID:   entry.QuestionCommentID,
		CommentKey:  entry.QuestionCommentKey,
		DismissedAt: now,
	}
	if rec.CommentID == "" && rec.CommentKey == "" {
		rec.ContextDigest = inputContextDigest(entry.Context)
	}
	if rec.CommentID == "" && rec.CommentKey == "" && rec.ContextDigest == "" {
		return // nothing identifies the question
	}
	if state.DismissedInputQuestions == nil {
		state.DismissedInputQuestions = make(map[string]DismissedInputQuestion)
	}
	state.DismissedInputQuestions[entry.Identifier] = rec
	for len(state.DismissedInputQuestions) > maxDismissedInputQuestions {
		oldest, oldestAt := "", time.Time{}
		for id, r := range state.DismissedInputQuestions {
			if oldest == "" || r.DismissedAt.Before(oldestAt) {
				oldest, oldestAt = id, r.DismissedAt
			}
		}
		delete(state.DismissedInputQuestions, oldest)
	}
}

// matches reports whether question (with its extracted context) is the
// dismissed one. A comment id or key is authoritative; the context digest
// is consulted only when the dismissal recorded neither.
func (d DismissedInputQuestion) matches(question domain.Comment, questionCtx string) bool {
	if d.CommentID != "" && question.ID == d.CommentID {
		return true
	}
	if d.CommentKey != "" && (question.ID == d.CommentKey || tracker.CommentHasKey(question.Body, d.CommentKey)) {
		return true
	}
	if d.CommentID == "" && d.CommentKey == "" && d.ContextDigest != "" {
		return inputContextDigest(questionCtx) == d.ContextDigest
	}
	return false
}

// inputContextDigest is a SHA-256 of the whitespace-normalised question text
// ("" for an empty one), so the dismissal record never stores the text.
func inputContextDigest(ctx string) string {
	norm := strings.Join(strings.Fields(ctx), " ")
	if norm == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}
