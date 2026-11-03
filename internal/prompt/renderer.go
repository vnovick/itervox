package prompt

import (
	"fmt"
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

	tpl, err := liquidEngine.ParseTemplate([]byte(blankOptionalText(tmpl)))
	if err != nil {
		return "", fmt.Errorf("template_parse_error: %w", err)
	}

	bindings := map[string]any{}
	for key, value := range extra {
		bindings[key] = value
	}
	bindings["issue"] = issueToMap(issue)
	bindings["attempt"] = attemptValue(attempt)

	out, err := tpl.Render(bindings)
	if err != nil {
		return "", fmt.Errorf("template_render_error: %w", err)
	}

	return string(out), nil
}

// optionalOutputRe matches the inside of an output tag that prints an
// optional issue field (#102): `{{ issue.description }}`,
// `{{- issue.url | strip -}}`, `{{ issue["branch_name"] }}`. Group 1 ends
// where the field does. These are the fields where "absent" and "empty" mean
// the same thing to a prompt.
var optionalOutputRe = regexp.MustCompile(`^(-?\s*` + optionalField + `)(?:\s*\||\s*-?\s*$)`)

// optionalField is an optional issue field reference.
const optionalField = `issue(?:\.(?:description|url|branch_name)|\[\s*(?:"(?:description|url|branch_name)"|'(?:description|url|branch_name)')\s*\])`

var (
	// optionalAssignRe matches an assign of an optional field, naming the
	// alias in group 1.
	optionalAssignRe = regexp.MustCompile(`^\{%-?\s*assign\s+(\w+)\s*=\s*` + optionalField + `\s*(?:\||-?%\}$)`)
	// variableOutputRe matches an output tag's inside that starts with a
	// plain variable, named in group 2; group 1 ends where the name does.
	variableOutputRe = regexp.MustCompile(`^(-?\s*(\w+))(?:\s*\||\s*-?\s*$)`)
)

// blankOptionalText makes each output tag that prints an optional issue
// field fall back to "" when it is unset, by adding `| default: ""` right
// after the field; likewise an output of an alias assigned from one
// (`{% assign d = issue.description %}…{{ d }}`). Only output tags change:
// the binding and the alias stay nil, so a guard such as
// `{% if issue.description %}` or `{% if d %}` keeps its meaning (an empty
// string is truthy in Liquid). A misspelt field or alias is not matched and
// still fails strict variables.
//
// The template is scanned token by token the way the engine reads it, not
// searched with a pattern, so text that is not an output stays as written:
// the inside of tags (quoted strings included) and {% raw %} and
// {% comment %} blocks, each of which ends only at its own closing tag. A
// prompt may show the agent Liquid examples.
func blankOptionalText(tmpl string) string {
	var b strings.Builder
	aliases := map[string]bool{}
	i := 0
	for i < len(tmpl) {
		open := strings.IndexByte(tmpl[i:], '{')
		if open < 0 {
			break
		}
		open += i
		if open+1 >= len(tmpl) || (tmpl[open+1] != '{' && tmpl[open+1] != '%') {
			b.WriteString(tmpl[i : open+1])
			i = open + 1
			continue
		}
		b.WriteString(tmpl[i:open])
		closer := "}}"
		if tmpl[open+1] == '%' {
			closer = "%}"
		}
		end := tokenEnd(tmpl, open+2, closer)
		if end < 0 {
			i = open // unterminated: left for the parser to report
			break
		}
		tok := tmpl[open:end]
		if closer == "}}" {
			inner := tok[2 : len(tok)-2]
			m := optionalOutputRe.FindStringSubmatchIndex(inner)
			if m == nil {
				if v := variableOutputRe.FindStringSubmatchIndex(inner); v != nil && aliases[inner[v[4]:v[5]]] {
					m = v
				}
			}
			if m != nil {
				tok = "{{" + inner[:m[3]] + ` | default: ""` + inner[m[3]:] + "}}"
			}
			b.WriteString(tok)
			i = end
			continue
		}
		if a := optionalAssignRe.FindStringSubmatch(tok); a != nil {
			aliases[a[1]] = true
		}
		if name := verbatimTagRe.FindStringSubmatch(tok); name != nil {
			// Copy through the block's own closing tag.
			closeRe := endRawRe
			if name[1] == "comment" {
				closeRe = endCommentRe
			}
			loc := closeRe.FindStringIndex(tmpl[end:])
			if loc == nil {
				i = open
				break
			}
			end += loc[1]
		}
		b.WriteString(tmpl[open:end])
		i = end
	}
	b.WriteString(tmpl[i:])
	return b.String()
}

// tokenEnd returns the index just past the first closer from start, or -1.
// Like the Liquid engine's own lexer, it does not look inside quotes: a tag
// ends at its first closer.
func tokenEnd(s string, start int, closer string) int {
	k := strings.Index(s[start:], closer)
	if k < 0 {
		return -1
	}
	return start + k + len(closer)
}

var (
	// verbatimTagRe matches the opening tag of a raw or comment block.
	verbatimTagRe = regexp.MustCompile(`^\{%-?\s*(raw|comment)\s*-?%\}$`)
	endRawRe      = regexp.MustCompile(`\{%-?\s*endraw\s*-?%\}`)
	endCommentRe  = regexp.MustCompile(`\{%-?\s*endcomment\s*-?%\}`)
)

// RenderPromptOverlay renders a plain-text or Liquid prompt fragment using the
// standard issue/attempt bindings plus optional extra bindings, returning the
// original text on parse/render errors for backward compatibility.
func RenderPromptOverlay(promptText string, issue domain.Issue, attempt *int, extra map[string]any) string {
	if strings.TrimSpace(promptText) == "" {
		return ""
	}

	tpl, err := liquidEngine.ParseTemplate([]byte(blankOptionalText(promptText)))
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
