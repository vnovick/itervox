package prompt

import (
	"fmt"
	"strings"
	"time"

	"github.com/osteele/liquid"
	"github.com/vnovick/itervox/internal/domain"
)

// DefaultPrompt is used when the workflow prompt body is empty.
const DefaultPrompt = "You are working on an issue."

// liquidEngine is a package-level singleton to avoid constructing a new engine
// on every Render call. The osteele/liquid Engine is goroutine-safe: its
// internal state (registered tags, filters) is set up at construction time and
// never mutated afterwards. ParseTemplate and Execute are safe to call from
// multiple goroutines concurrently.
var liquidEngine = func() *liquid.Engine {
	e := liquid.NewEngine()
	e.StrictVariables()
	return e
}()

// Render renders a Liquid template with issue and attempt variables.
// Returns template_parse_error on bad syntax, template_render_error on unknown vars/filters.
func Render(tmpl string, issue domain.Issue, attempt *int) (string, error) {
	if strings.TrimSpace(tmpl) == "" {
		return DefaultPrompt, nil
	}

	tpl, err := liquidEngine.ParseTemplate([]byte(tmpl))
	if err != nil {
		return "", fmt.Errorf("template_parse_error: %w", err)
	}

	bindings := map[string]any{
		"issue":   issueToMap(issue),
		"attempt": attemptValue(attempt),
	}

	out, err := tpl.Render(bindings)
	if err != nil {
		return "", fmt.Errorf("template_render_error: %w", err)
	}

	return string(out), nil
}

// RenderProfilePrompt renders a profile prompt through the Liquid engine with
// the same issue bindings as Render. If the prompt contains no Liquid syntax
// it passes through unchanged. Returns the input as-is on parse/render errors
// so a plain-text prompt still works.
func RenderProfilePrompt(promptText string, issue domain.Issue, attempt *int) string {
	return RenderPromptOverlay(promptText, issue, attempt, nil)
}

// RenderPromptOverlay renders a plain-text or Liquid prompt fragment using the
// standard issue/attempt bindings plus optional extra bindings, returning the
// original text on parse/render errors for backward compatibility.
func RenderPromptOverlay(promptText string, issue domain.Issue, attempt *int, extra map[string]any) string {
	if strings.TrimSpace(promptText) == "" {
		return ""
	}

	tpl, err := liquidEngine.ParseTemplate([]byte(promptText))
	if err != nil {
		// Not valid Liquid — return as plain text (backward-compatible).
		return promptText
	}

	bindings := map[string]any{
		"issue":   issueToMap(issue),
		"attempt": attemptValue(attempt),
	}
	for key, value := range extra {
		bindings[key] = value
	}

	out, err := tpl.Render(bindings)
	if err != nil {
		return promptText
	}

	return string(out)
}

func attemptValue(attempt *int) any {
	if attempt == nil {
		return nil
	}
	return *attempt
}

// issueToMap converts an Issue to a string-keyed map for Liquid template consumption.
func issueToMap(issue domain.Issue) map[string]any {
	return map[string]any{
		"id":          issue.ID,
		"identifier":  issue.Identifier,
		"title":       issue.Title,
		"description": derefString(issue.Description),
		"priority":    derefInt(issue.Priority),
		"state":       issue.State,
		"branch_name": derefString(issue.BranchName),
		"url":         derefString(issue.URL),
		"labels":      labelsValue(issue.Labels),
		"blocked_by":  blockersValue(issue.BlockedBy),
		"comments":    commentsValue(issue.Comments),
		"created_at":  timeValue(issue.CreatedAt),
		"updated_at":  timeValue(issue.UpdatedAt),
	}
}

func derefString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func derefInt(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}

func timeValue(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

func labelsValue(labels []string) []any {
	out := make([]any, len(labels))
	for i, l := range labels {
		out[i] = l
	}
	return out
}

func blockersValue(blockers []domain.BlockerRef) []any {
	out := make([]any, len(blockers))
	for i, b := range blockers {
		out[i] = map[string]any{
			"id":         derefString(b.ID),
			"identifier": derefString(b.Identifier),
			"state":      derefString(b.State),
		}
	}
	return out
}

func commentsValue(comments []domain.Comment) []any {
	out := make([]any, len(comments))
	for i, c := range comments {
		out[i] = map[string]any{
			"body":        c.Body,
			"author_name": c.AuthorName,
			"created_at":  timeValue(c.CreatedAt),
		}
	}
	return out
}
