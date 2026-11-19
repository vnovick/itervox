package jira_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

func TestBlocksLinkWithCustomInwardTextIsStillABlocker(t *testing.T) {
	raw := issueFixture("10001", "PROJ-1", "To Do")
	raw["fields"].(map[string]any)["issuelinks"] = []any{
		map[string]any{
			"id":          "1",
			"type":        map[string]any{"name": "Blocks", "inward": "blocked by", "outward": "blocks"},
			"inwardIssue": map[string]any{"id": "10002", "key": "PROJ-2", "fields": map[string]any{"status": map[string]any{"name": "In Progress"}}},
		},
	}
	c, _ := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		writeJSON(w, map[string]any{"issues": []any{raw}})
	})

	got, err := c.FetchIssueStatesByIDs(t.Context(), []string{"10001"})

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Len(t, got[0].BlockedBy, 1, "a link of type Blocks (the type CreateIssue writes) must make a blocker")
}

func TestFetchIssueByIdentifierRefusesIssueNowInOtherProject(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		if strings.HasSuffix(r.Path, "/comment") {
			writeJSON(w, map[string]any{"total": 0, "comments": []any{}})
			return
		}
		writeJSON(w, issueFixture("20009", "OTHER-9", "To Do")) // Jira followed a moved key
	})

	got, err := c.FetchIssueByIdentifier(t.Context(), "PROJ-5")

	assert.Nil(t, got)
	assert.ErrorIs(t, err, tracker.ErrNotFound, "an issue now outside the configured project must be out of scope")
}

func TestCommentPagesIgnoringStartAtDoNotDuplicate(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		if strings.HasSuffix(r.Path, "/comment") {
			writeJSON(w, map[string]any{"startAt": 0, "maxResults": 2, "total": 4, "comments": []any{
				commentFixture("1", "2024-01-01T00:00:00.000+0000", "a"),
				commentFixture("2", "2024-01-02T00:00:00.000+0000", "b"),
			}})
			return
		}
		writeJSON(w, issueFixture("10001", "PROJ-1", "To Do"))
	})
	c.SetCommentPageSize(2)

	got, err := c.FetchIssueDetail(t.Context(), "10001")

	require.NoError(t, err)
	ids := map[string]int{}
	for _, cm := range got.Comments {
		ids[cm.ID]++
	}
	assert.Equal(t, map[string]int{"1": 1, "2": 1}, ids, "each comment once, whatever the server does with startAt")
}

func TestCommentPaginationIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var commentReqs atomic.Int64
	c, _ := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		if strings.HasSuffix(r.Path, "/comment") {
			n := commentReqs.Add(1)
			if n >= 50 {
				cancel() // safety net so a regression fails fast instead of looping
			}
			writeJSON(w, map[string]any{"total": 1 << 40, "comments": []any{
				commentFixture(strconv.FormatInt(n, 10), "2024-01-01T00:00:00.000+0000", "a"),
			}})
			return
		}
		writeJSON(w, issueFixture("10001", "PROJ-1", "To Do"))
	})
	jira.SetMaxPages(c, 3)

	_, err := c.FetchIssueDetail(ctx, "10001")

	require.Error(t, err)
	assert.NotErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "pagination did not end")
	assert.Equal(t, int64(3), commentReqs.Load())
}

func TestADFKeepsHyperlinkTargets(t *testing.T) {
	link := func(text, href string) map[string]any {
		return map[string]any{"type": "text", "text": text, "marks": []any{
			map[string]any{"type": "link", "attrs": map[string]any{"href": href}},
		}}
	}
	doc := map[string]any{"type": "doc", "version": 1, "content": []any{
		map[string]any{"type": "paragraph", "content": []any{
			map[string]any{"type": "text", "text": "See "},
			link("the failing run", "https://ci.example.com/run/42"),
			map[string]any{"type": "text", "text": ", "},
			link("https://same.example.com", "https://same.example.com"),
			map[string]any{"type": "text", "text": " and "},
			link("no address", ""),
		}},
	}}

	got := jira.ADFToText(doc)

	assert.Equal(t, "See the failing run (https://ci.example.com/run/42), https://same.example.com and no address", got)
}
