package localsync

import (
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// localCommentHash identifies a file comment by what the file keeps of it:
// its time to the second, its author and its text.
func localCommentHash(c local.SyncComment) string {
	author := strings.TrimSpace(strings.ReplaceAll(c.Author, "\n", " "))
	return hashText(c.At.UTC().Truncate(time.Second).Format(time.RFC3339) + "\x00" + author + "\x00" + strings.TrimSpace(c.Body))
}

// isEcho reports whether a remote comment is one the sync posted itself:
// its id is a pushed key (Linear uses the key as the comment id) or its body
// carries a pushed key's marker (GitHub).
func (l *link) isEcho(c domain.Comment) bool {
	if l.PushedKeys[c.ID] {
		return true
	}
	if !strings.Contains(c.Body, "<!-- itervox:ck:") {
		return false
	}
	for k := range l.PushedKeys {
		if tracker.CommentHasKey(c.Body, k) {
			return true
		}
	}
	return false
}

// isMarkerComment reports whether a remote comment is only Itervox
// bookkeeping (a GitHub branch marker and the like), not a comment.
func isMarkerComment(body string) bool {
	b := strings.TrimSpace(body)
	return strings.HasPrefix(b, "<!-- itervox:") && strings.HasSuffix(b, "-->") && strings.Count(b, "\n") == 0
}

// pulledComment is a remote comment as the file keeps it.
func pulledComment(c domain.Comment, now time.Time) local.SyncComment {
	author := strings.TrimSpace(c.AuthorName)
	if author == "" {
		author = strings.TrimSpace(c.AuthorID)
	}
	return local.SyncComment{At: timeOr(c.CreatedAt, now).UTC().Truncate(time.Second), Author: author, Body: strings.TrimSpace(c.Body)}
}

// pushedCommentBody is a file comment as posted to the remote: the file's
// author is named, since the remote shows the token's user as the poster.
func pushedCommentBody(c local.SyncComment) string {
	body := strings.TrimSpace(c.Body)
	if a := strings.TrimSpace(c.Author); a != "" {
		return "**" + a + "** (local issue file):\n\n" + body
	}
	return body
}
