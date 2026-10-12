package github

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// FetchIssuesUpdatedSince implements tracker.IncrementalFetcher (#114): one
// paginated `issues?state=all&since=` listing of the repository, newest
// update first: an issue edited while the pages are read moves to the front,
// so it is listed twice or (when the first page was already read) on the
// next pull, never skipped. An issue whose labels no longer name a tracked
// state (active, terminal, backlog or the completion state) is reported
// gone, and so is a closed issue unless "closed" is a terminal state: the
// full pull lists only open issues by label, so placing a closed one would
// show it until the next full pull. Pull requests are skipped. A deleted or
// transferred issue does not appear: the store's full pull catches those.
func (c *Client) FetchIssuesUpdatedSince(ctx context.Context, since time.Time) ([]domain.Issue, []string, error) {
	q := url.Values{}
	q.Set("state", "all")
	q.Set("since", since.UTC().Format(time.RFC3339))
	q.Set("sort", "updated")
	q.Set("direction", "desc")
	q.Set("per_page", strconv.Itoa(pageSize))
	nextURL := fmt.Sprintf("%s/repos/%s/%s/issues?%s", c.cfg.Endpoint, c.owner, c.repo, q.Encode())

	extra := append([]string{}, c.cfg.BacklogStates...)
	if cs := strings.TrimSpace(c.cfg.CompletionState); cs != "" {
		extra = append(extra, cs)
	}
	tracksClosed := slices.ContainsFunc(c.terminalStates(), func(st string) bool {
		return strings.EqualFold(st, "closed")
	})
	var changed []domain.Issue
	var gone []string
	for nextURL != "" {
		body, linkHeader, err := c.get(ctx, nextURL)
		if err != nil {
			return nil, nil, err
		}
		rawItems, ok := body.([]any)
		if !ok {
			return nil, nil, fmt.Errorf("github_unknown_payload: expected array response")
		}
		for _, item := range rawItems {
			raw, ok := item.(map[string]any)
			if !ok || isPullRequest(raw) {
				continue
			}
			ghState, _ := raw["state"].(string)
			derived := ""
			if tracksClosed || !strings.EqualFold(ghState, "closed") {
				derived = deriveState(raw, c.activeStates(), c.terminalStates())
				if derived == "" {
					derived = firstLabelIn(extractLabels(raw), extra)
				}
			}
			if derived == "" {
				if n, ok := tracker.ToIntVal(raw["number"]); ok {
					gone = append(gone, strconv.Itoa(n))
				}
				continue
			}
			if issue := normalizeIssue(raw, derived); issue != nil {
				changed = append(changed, *issue)
			}
		}
		next, err := ParseNextLink(linkHeader)
		if err != nil {
			break // no rel="next": the last page
		}
		nextURL = next
	}
	return c.populateBlockerStates(ctx, changed), gone, nil
}

// firstLabelIn returns the first of states that labels carries (any case),
// or "".
func firstLabelIn(labels, states []string) string {
	for _, label := range labels {
		for _, st := range states {
			if strings.EqualFold(label, st) {
				return st
			}
		}
	}
	return ""
}

var _ tracker.IncrementalFetcher = (*Client)(nil)
