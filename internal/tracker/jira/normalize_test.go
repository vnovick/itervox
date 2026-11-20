package jira_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

// fetchOne serves one raw issue through bulkfetch and returns it normalized.
func fetchOne(t *testing.T, raw map[string]any) domain.Issue {
	t.Helper()
	c, _ := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		writeJSON(w, map[string]any{"issues": []any{raw}})
	})
	got, err := c.FetchIssueStatesByIDs(t.Context(), []string{raw["id"].(string)})
	require.NoError(t, err)
	require.Len(t, got, 1)
	return got[0]
}

func TestNormalizeMapsBasicFields(t *testing.T) {
	raw := issueFixture("10001", "PROJ-1", "In Progress")
	f := raw["fields"].(map[string]any)
	f["labels"] = []any{"bug", "ui"}
	f["assignee"] = map[string]any{"accountId": "acc-1", "displayName": "Ann"}
	f["description"] = map[string]any{"type": "doc", "version": 1, "content": []any{
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "Hello"}}},
	}}

	got := fetchOne(t, raw)

	assert.Equal(t, "10001", got.ID)
	assert.Equal(t, "PROJ-1", got.Identifier)
	assert.Equal(t, "Summary PROJ-1", got.Title)
	assert.Equal(t, "In Progress", got.State)
	assert.Equal(t, []string{"bug", "ui"}, got.Labels)
	assert.Equal(t, []string{"acc-1"}, got.Assignees)
	require.NotNil(t, got.URL)
	assert.Regexp(t, `/browse/PROJ-1$`, *got.URL)
	require.NotNil(t, got.Description)
	assert.Equal(t, "Hello", *got.Description)
}

func TestNormalizeMissingFieldsAreTolerated(t *testing.T) {
	got := fetchOne(t, map[string]any{"id": "10009", "key": "PROJ-9"})

	assert.Equal(t, "PROJ-9", got.Identifier)
	assert.Nil(t, got.Description)
	assert.Nil(t, got.Priority)
	assert.Nil(t, got.CreatedAt)
	assert.Empty(t, got.BlockedBy)
	assert.Empty(t, got.Assignees)
}

func TestNormalizeMapsBlockedByLinksToBlockers(t *testing.T) {
	raw := issueFixture("10001", "PROJ-1", "To Do")
	raw["fields"].(map[string]any)["issuelinks"] = []any{
		map[string]any{ // PROJ-2 blocks PROJ-1: this issue is blocked by it.
			"id":   "1",
			"type": map[string]any{"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
			"inwardIssue": map[string]any{
				"id": "10002", "key": "PROJ-2",
				"fields": map[string]any{"status": map[string]any{"name": "In Progress"}},
			},
		},
		map[string]any{ // this issue blocks PROJ-3: not a blocker.
			"id":           "2",
			"type":         map[string]any{"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
			"outwardIssue": map[string]any{"id": "10003", "key": "PROJ-3"},
		},
		map[string]any{ // plain relation: not a blocker.
			"id":          "3",
			"type":        map[string]any{"name": "Relates", "inward": "relates to", "outward": "relates to"},
			"inwardIssue": map[string]any{"id": "10004", "key": "PROJ-4"},
		},
	}

	got := fetchOne(t, raw)

	require.Len(t, got.BlockedBy, 1)
	b := got.BlockedBy[0]
	require.NotNil(t, b.ID)
	assert.Equal(t, "10002", *b.ID)
	require.NotNil(t, b.Identifier)
	assert.Equal(t, "PROJ-2", *b.Identifier)
	require.NotNil(t, b.State)
	assert.Equal(t, "In Progress", *b.State)
	require.NotNil(t, b.URL)
	assert.Regexp(t, `/browse/PROJ-2$`, *b.URL)
	assert.Empty(t, b.Origin)
}

func TestNormalizeMapsPriorityAndTimes(t *testing.T) {
	want := map[string]*int{"Highest": ptr(1), "High": ptr(2), "medium": ptr(3), "LOW": ptr(4), "Lowest": ptr(5), "Urgent!": nil}
	for name, p := range want {
		raw := issueFixture("10001", "PROJ-1", "To Do")
		f := raw["fields"].(map[string]any)
		f["priority"] = map[string]any{"name": name}
		f["created"] = "2024-01-17T12:34:00.000+0000"
		f["updated"] = "2024-01-18T09:00:00.000-0530"

		got := fetchOne(t, raw)

		if p == nil {
			assert.Nil(t, got.Priority, name)
		} else if assert.NotNil(t, got.Priority, name) {
			assert.Equal(t, *p, *got.Priority, name)
		}
		require.NotNil(t, got.CreatedAt)
		assert.True(t, got.CreatedAt.Equal(time.Date(2024, 1, 17, 12, 34, 0, 0, time.UTC)))
		require.NotNil(t, got.UpdatedAt)
		assert.True(t, got.UpdatedAt.Equal(time.Date(2024, 1, 18, 14, 30, 0, 0, time.UTC)), "offset -0530 applied")
	}
}

func TestNormalizeParsesRFC3339TimeAndRejectsGarbage(t *testing.T) {
	raw := issueFixture("10001", "PROJ-1", "To Do")
	f := raw["fields"].(map[string]any)
	f["created"] = "2024-01-17T12:34:00Z"
	f["updated"] = "yesterday"

	got := fetchOne(t, raw)

	require.NotNil(t, got.CreatedAt)
	assert.Nil(t, got.UpdatedAt)
}

func TestADFToTextFlattensParagraphsListsAndCode(t *testing.T) {
	text := func(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
	para := func(c ...any) map[string]any { return map[string]any{"type": "paragraph", "content": c} }
	item := func(s string) map[string]any {
		return map[string]any{"type": "listItem", "content": []any{para(text(s))}}
	}
	doc := map[string]any{"type": "doc", "version": 1, "content": []any{
		map[string]any{"type": "heading", "attrs": map[string]any{"level": 2}, "content": []any{text("Title")}},
		para(text("one"), map[string]any{"type": "hardBreak"}, text("two "), map[string]any{"type": "mention", "attrs": map[string]any{"id": "x", "text": "@Ann"}}),
		map[string]any{"type": "bulletList", "content": []any{item("a"), item("b")}},
		map[string]any{"type": "orderedList", "content": []any{item("c")}},
		map[string]any{"type": "codeBlock", "content": []any{text("x := 1")}},
		para(map[string]any{"type": "inlineCard", "attrs": map[string]any{"url": "https://example.com/p"}}, map[string]any{"type": "emoji", "attrs": map[string]any{"shortName": ":x:", "text": "X"}}),
		map[string]any{"type": "panel", "content": []any{para(text("nested"))}},
	}}

	// Blocks are separated by one blank line, as in Markdown.
	want := "Title\n\none\ntwo @Ann\n\n- a\n- b\n\n- c\n\n```\nx := 1\n```\n\nhttps://example.com/pX\n\nnested"
	assert.Equal(t, want, jira.ADFToText(doc))
	assert.Equal(t, "plain", jira.ADFToText("plain"))
	assert.Empty(t, jira.ADFToText(nil))
	assert.Empty(t, jira.ADFToText(42))
}

func ptr[T any](v T) *T { return &v }
