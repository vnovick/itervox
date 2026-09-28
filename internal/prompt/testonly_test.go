package prompt

import "github.com/vnovick/itervox/internal/domain"

// Render renders a Liquid template with issue and attempt variables.
// Returns template_parse_error on bad syntax, template_render_error on unknown vars/filters.
func Render(tmpl string, issue domain.Issue, attempt *int) (string, error) {
	return RenderWith(tmpl, issue, attempt, nil)
}
