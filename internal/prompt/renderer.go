package prompt

import (
	"fmt"
	"maps"
	"regexp"
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

// RenderWith is Render with extra top-level bindings (e.g. the worker's
// `run` object, CORE-101). extra cannot replace `issue` or `attempt`.
func RenderWith(tmpl string, issue domain.Issue, attempt *int, extra map[string]any) (string, error) {
	if strings.TrimSpace(tmpl) == "" {
		return DefaultPrompt, nil
	}

	tpl, err := liquidEngine.ParseTemplate([]byte(tmpl))
	if err != nil {
		return "", fmt.Errorf("template_parse_error: %w", err)
	}

	bindings := map[string]any{}
	for key, value := range extra {
		bindings[key] = value
	}
	bindings["issue"] = issueToMap(issue)
	bindings["attempt"] = attemptValue(attempt)

	out, renderErr := renderBlankingOptional(tpl, bindings)
	if renderErr != nil {
		return "", fmt.Errorf("template_render_error: %w", renderErr)
	}

	return string(out), nil
}

// optionalIssueText are the issue fields where "absent" and "empty" mean the
// same thing to a prompt (#102). Unset, they bind as nil, so a guard such as
// `{% if issue.description %}` skips its block (an empty string is truthy in
// Liquid). Printed without a guard, strict variables would fail the render on
// nil, so renderBlankingOptional renders them as empty text instead.
var optionalIssueText = map[string]bool{"description": true, "branch_name": true, "url": true}

// undefinedOutputRe extracts the variable a strict-variables error names,
// e.g. `undefined variable in {{ issue.description | strip }}`.
var undefinedOutputRe = regexp.MustCompile(`undefined variable in \{\{-?\s*issue\.([a-z_]+)\s*(?:\||-?\}\})`)

// renderBlankingOptional renders tpl with strict variables. When the render
// fails only because an unset optional issue field is printed, that field is
// bound to "" and the template rendered again, still strictly, so a misspelt
// variable (`{{ issue.descripton }}`) is still an error. Guards on the field
// are unaffected unless the template also prints it unguarded.
func renderBlankingOptional(tpl *liquid.Template, bindings map[string]any) ([]byte, error) {
	issue, _ := bindings["issue"].(map[string]any)
	for range len(optionalIssueText) + 1 {
		out, err := tpl.Render(bindings)
		if err == nil {
			return out, nil
		}
		m := undefinedOutputRe.FindStringSubmatch(err.Error())
		if m == nil || issue == nil || !optionalIssueText[m[1]] || issue[m[1]] != nil {
			return nil, err
		}
		issue = maps.Clone(issue)
		issue[m[1]] = ""
		bindings = maps.Clone(bindings)
		bindings["issue"] = issue
	}
	return nil, fmt.Errorf("template_render_error: unreachable")
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

	out, renderErr := renderBlankingOptional(tpl, bindings)
	if renderErr != nil {
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
