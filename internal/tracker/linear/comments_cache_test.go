package linear_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker/linear"
)

// TestLinearCommentPagesCachedAcrossReplyChecks (CORE-165): after CORE-124
// every reply check re-read the whole thread, ~1 request per 50 comments. A
// full middle page of an ASCENDING thread cannot change when comments are
// added (they land after it), so it is served from a short-lived cache and
// only the last page is re-read. A descending (or undetermined) thread is
// read in full every time, because there the pages shift.
func TestLinearCommentPagesCachedAcrossReplyChecks(t *testing.T) {
	fetch := func(c *linear.Client) []string {
		issue, err := c.FetchIssueDetail(context.Background(), "issue-1")
		require.NoError(t, err)
		ids := make([]string, 0, len(issue.Comments))
		for _, cm := range issue.Comments {
			ids = append(ids, cm.ID)
		}
		return ids
	}

	t.Run("asc", func(t *testing.T) {
		fake := newCommentThreadServer(260, "asc") // 6 pages
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL})

		first := fetch(client)
		require.Len(t, first, 260)
		require.EqualValues(t, 5, fake.cursorPages.Load())

		second := fetch(client)
		assert.Equal(t, first, second, "a cached read returns the same thread")
		assert.EqualValues(t, 6, fake.cursorPages.Load(), "only the last page is re-read")

		// A new comment lands at the end: still exactly one re-read, and it is seen.
		fake.comments = append(fake.comments, map[string]any{
			"id": "c261", "body": "new reply",
			"createdAt": time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000Z"),
			"user":      map[string]any{"id": "u2", "name": "Bo"},
		})
		third := fetch(client)
		require.Len(t, third, 261)
		assert.Equal(t, "c261", third[len(third)-1])
		assert.EqualValues(t, 7, fake.cursorPages.Load())
	})

	t.Run("desc", func(t *testing.T) {
		fake := newCommentThreadServer(260, "desc")
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL})
		for i := 1; i <= 2; i++ {
			require.Len(t, fetch(client), 260)
			assert.EqualValues(t, 5*i, fake.cursorPages.Load(), fmt.Sprintf("read %d: a descending thread is never cached", i))
		}
	})
}
