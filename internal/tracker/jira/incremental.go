package jira

import (
	"context"
	"fmt"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// searchLag widens the incremental pull (#114) beyond the tracker store's
// own look-back (store.CursorOverlap, one minute), because Jira's search is
// not read-after-write consistent: an update reaches POST /search/jql after
// a delay that Atlassian gives as "a few seconds to minutes", longest for
// bulk edits (https://developer.atlassian.com/cloud/jira/platform/search-and-reconcile/).
// Measured on a Jira Cloud site on 2026-11-20: a single edit was searchable
// 0.7 to 4.2 s after the write returned (10 samples), the issues of a
// five-issue bulk edit 2 to 13 s after the bulk task completed. With this
// widening the pull looks back five minutes in all; an update that takes
// longer to reach search is missed until the next full pull
// (tracker.store.full_sync_interval_ms). The cost is size, not requests:
// every pull lists again the issues updated in the last five minutes, and
// the store drops the unchanged ones without a write.
const searchLag = 4 * time.Minute

// FetchIssuesUpdatedSince implements tracker.IncrementalFetcher (#114): the
// project's issues updated at or after since less searchLag, in any status,
// newest update first, in one paginated search. The time is sent as
// unquoted epoch milliseconds, which JQL compares to the millisecond in any
// time zone; a quoted date would be read in the token's profile time zone,
// to the minute.
//
// The query names only the project. The assignee and the labels are applied
// to each listed issue in memory, by MatchesFilter, the same scope the list
// reads compile into JQL: an issue they no longer cover (reassigned, label
// removed) is returned in gone, so the store drops it at this pull. The
// others are changed, whatever their status: the store places each by
// status, and one in a status no list tracks leaves every list. A deleted
// issue, or one moved to another project, is not listed at all; the store's
// full pull removes it. A failed page fails the call with nothing returned.
// With an assignee of "me", the token's account is read first (resolveMe)
// and the call fails if it cannot be.
func (c *Client) FetchIssuesUpdatedSince(ctx context.Context, since time.Time) (changed []domain.Issue, gone []string, err error) {
	if isMe(c.cfg.Assignee) {
		if err := c.resolveMe(ctx); err != nil {
			return nil, nil, err
		}
	}
	jql := fmt.Sprintf("project = %s AND updated >= %d ORDER BY updated DESC",
		jqlQuote(c.cfg.ProjectSlug), since.Add(-searchLag).UnixMilli())
	raws, err := c.search(ctx, jql)
	if err != nil {
		return nil, nil, fmt.Errorf("jira_search_updated: %w", err)
	}
	for _, iss := range c.normalizeAll(raws) {
		if in, known := c.MatchesFilter(iss); known && !in {
			gone = append(gone, iss.ID)
			continue
		}
		changed = append(changed, iss)
	}
	return changed, gone, nil
}

var _ tracker.IncrementalFetcher = (*Client)(nil)
