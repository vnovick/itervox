package prompt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
)

// TestEmptyDescriptionRendersAsNothing (#102): an issue with no description
// renders `{{ issue.description }}` as nothing instead of failing the
// dispatch, while a misspelt variable still fails under strict variables.
func TestEmptyDescriptionRendersAsNothing(t *testing.T) {
	issue := domain.Issue{Identifier: "#1", Title: "x"}

	out, err := Render("Title: {{ issue.title }}\n[{{ issue.description }}]", issue, nil)
	require.NoError(t, err)
	assert.Equal(t, "Title: x\n[]", out)

	for _, tpl := range []string{
		"[{{issue.description}}]",
		"[{{- issue.description -}}]",
		"[{{ issue.description | strip }}]",
		"[{{ issue.url }}] [{{ issue.branch_name }}] [{{ issue.description }}]",
	} {
		out, err := Render(tpl, issue, nil)
		require.NoError(t, err, tpl)
		assert.NotContains(t, out, "nil", tpl)
	}

	_, err = Render("{{ issue.descripton }}", issue, nil)
	require.Error(t, err, "a typo is still an undefined variable")
	assert.Contains(t, err.Error(), "undefined variable")

	_, err = Render("{{ issue.description }} {{ issue.nope }}", issue, nil)
	require.Error(t, err, "blanking a known field never hides a typo elsewhere")

	_, err = Render("{{ issue.priority }}", issue, nil)
	require.Error(t, err, "priority stays unset: there, absent carries meaning")

	assert.Equal(t, "[]", RenderPromptOverlay("[{{ issue.description }}]", issue, nil, nil),
		"profile prompts get the same treatment")
}

// TestEmptyDescriptionGuardStillSkips (#102): `{% if issue.description %}`
// still skips its block for an issue with no description, and still renders
// it when there is one.
func TestEmptyDescriptionGuardStillSkips(t *testing.T) {
	tpl := "{% if issue.description %}## Description\n{{ issue.description }}{% else %}none{% endif %}"
	out, err := Render(tpl, domain.Issue{Identifier: "#1"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "none", out)

	d := "Fix it."
	out, err = Render(tpl, domain.Issue{Identifier: "#1", Description: &d}, nil)
	require.NoError(t, err)
	assert.Equal(t, "## Description\nFix it.", out)

	out, err = Render("{% unless issue.url %}no url{% endunless %}", domain.Issue{Identifier: "#1"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "no url", out)
}

// TestEmptyDescriptionGuardWithUnguardedPrint (#102): a template that both
// guards the field and prints it unguarded still skips the guarded block,
// and the other ways of printing the field render as nothing too.
func TestEmptyDescriptionGuardWithUnguardedPrint(t *testing.T) {
	issue := domain.Issue{Identifier: "#1"}
	out, err := Render("{% if issue.description %}## Description{% endif %}[{{ issue.description }}]", issue, nil)
	require.NoError(t, err)
	assert.Equal(t, "[]", out)

	out, err = Render("{% if issue.url %}Link: {{ issue.url }}{% else %}No link{% endif %} [{{ issue.url }}]", issue, nil)
	require.NoError(t, err)
	assert.Equal(t, "No link []", out)

	for _, tpl := range []string{
		`[{{ issue["description"] }}]`,
		`[{{ issue['branch_name'] }}]`,
		`{% assign d = issue.description %}[{{ d }}]`,
		`{%- assign d = issue.url -%}[{{ d }}]`,
	} {
		out, err := Render(tpl, issue, nil)
		require.NoError(t, err, tpl)
		assert.Equal(t, "[]", out, tpl)
	}

	d := "Text"
	out, err = Render(`{% if issue.description %}Has {% endif %}{{ issue.description | downcase }}`, domain.Issue{Identifier: "#1", Description: &d}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Has text", out, "a set description is untouched")
}
