package jira

import (
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// jiraTimeLayout is the layout Jira sends: the zone offset has no colon.
const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

// normalizeAll converts raw issues from a search or bulk response.
func (c *Client) normalizeAll(raws []map[string]any) []domain.Issue {
	out := make([]domain.Issue, 0, len(raws))
	for _, raw := range raws {
		out = append(out, c.normalizeIssue(raw))
	}
	return out
}

// normalizeIssue maps one raw Jira issue to a domain.Issue. Missing fields
// leave the matching value empty.
func (c *Client) normalizeIssue(raw map[string]any) domain.Issue {
	fields, _ := raw["fields"].(map[string]any)
	id, _ := raw["id"].(string)
	key, _ := raw["key"].(string)
	issue := domain.Issue{
		ID:         id,
		Identifier: key,
		Title:      str(fields["summary"]),
		State:      str(nested(fields, "status", "name")),
		Priority:   priorityOf(str(nested(fields, "priority", "name"))),
		CreatedAt:  parseJiraTime(fields["created"]),
		UpdatedAt:  parseJiraTime(fields["updated"]),
		BlockedBy:  c.blockersOf(fields["issuelinks"]),
	}
	if key != "" {
		u := c.browseURL(key)
		issue.URL = &u
	}
	if d, ok := fields["description"]; ok && d != nil {
		text := adfToText(d)
		issue.Description = &text
	}
	if labels, ok := fields["labels"].([]any); ok {
		for _, l := range labels {
			if s, ok := l.(string); ok {
				issue.Labels = append(issue.Labels, s)
			}
		}
	}
	if acc := str(nested(fields, "assignee", "accountId")); acc != "" {
		issue.Assignees = []string{acc}
	}
	return issue
}

// normalizeComment maps one raw Jira comment to a domain.Comment.
func normalizeComment(raw map[string]any) domain.Comment {
	return domain.Comment{
		ID:         str(raw["id"]),
		Body:       adfToText(raw["body"]),
		CreatedAt:  parseJiraTime(raw["created"]),
		AuthorID:   str(nested(raw, "author", "accountId")),
		AuthorName: str(nested(raw, "author", "displayName")),
	}
}

// blockersOf returns the issues that block this one: links with an inward
// issue whose type is named "Blocks" or whose inward text is "is blocked by".
// Links of any other type, or with only an outward issue, are not blockers.
func (c *Client) blockersOf(links any) []domain.BlockerRef {
	var out []domain.BlockerRef
	list, _ := links.([]any)
	for _, l := range list {
		link, _ := l.(map[string]any)
		inward, ok := link["inwardIssue"].(map[string]any)
		if !ok || !isBlocksLink(link) {
			continue
		}
		id, key := str(inward["id"]), str(inward["key"])
		ref := domain.BlockerRef{ID: &id, Identifier: &key}
		if state := str(nested(inward, "fields", "status", "name")); state != "" {
			ref.State = &state
		}
		if key != "" {
			u := c.browseURL(key)
			ref.URL = &u
		}
		out = append(out, ref)
	}
	return out
}

// isBlocksLink reports whether a link type is named "Blocks" or reads
// "is blocked by" on its inward side. The name catches sites that changed the
// inward text; the text catches sites that renamed the type.
func isBlocksLink(link map[string]any) bool {
	return strings.EqualFold(str(nested(link, "type", "name")), "Blocks") ||
		strings.EqualFold(str(nested(link, "type", "inward")), "is blocked by")
}

func (c *Client) browseURL(key string) string { return c.cfg.Endpoint + "/browse/" + key }

// priorityOf maps a Jira priority name to the codebase scale where a lower
// number is more urgent. An unknown name gives nil.
func priorityOf(name string) *int {
	for i, n := range []string{"highest", "high", "medium", "low", "lowest"} {
		if strings.EqualFold(name, n) {
			p := i + 1
			return &p
		}
	}
	return nil
}

// parseJiraTime parses Jira's "2024-01-17T12:34:00.000+0000" form, then
// RFC3339. It returns nil when neither fits.
func parseJiraTime(v any) *time.Time {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	if t, err := time.Parse(jiraTimeLayout, s); err == nil {
		return &t
	}
	return tracker.ParseTime(s)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// nested walks keys through nested maps; a missing step gives nil.
func nested(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}
