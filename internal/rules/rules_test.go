package rules

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// workflowWithLayers lays out a project with all three rules layers and
// returns its WORKFLOW.md path. The user layer is pointed at a temp file.
func workflowWithLayers(t *testing.T, user, project, local string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "acme-web")
	wf := filepath.Join(dir, "WORKFLOW.md")
	writeFile(t, wf, "---\n---\n")
	userPath := filepath.Join(root, "home", ".itervox", "rules.json")
	t.Setenv(UserRulesEnv, userPath)
	if user != "" {
		writeFile(t, userPath, user)
	}
	if project != "" {
		writeFile(t, ProjectPath(wf), project)
	}
	if local != "" {
		writeFile(t, LocalPath(wf), local)
	}
	return wf
}

func TestParseRulesFileAcceptsJSONCAndRejectsBadRules(t *testing.T) {
	rules, err := Parse([]byte(`// comment
{
  "$schema": "x",
  "rules": [
    {"id": "pnpm", "severity": "must", "text": "Use pnpm.", "scope": {"backends": ["claude"]}}, // trailing
  ],
}`))
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, Rule{ID: "pnpm", Severity: SeverityMust, Text: "Use pnpm.", Scope: Scope{Backends: []string{"claude"}}}, rules[0])

	for name, body := range map[string]string{
		"missing severity": `{"rules":[{"id":"a","text":"t"}]}`,
		"bad severity":     `{"rules":[{"id":"a","text":"t","severity":"may"}]}`,
		"empty text":       `{"rules":[{"id":"a","text":" ","severity":"must"}]}`,
		"bad id":           `{"rules":[{"id":"has space","text":"t","severity":"must"}]}`,
		"duplicate id":     `{"rules":[{"id":"a","text":"t","severity":"must"},{"id":"A","text":"u","severity":"should"}]}`,
		"unknown field":    `{"rules":[{"id":"a","text":"t","severity":"must","level":1}]}`,
		"unknown top":      `{"rule":[]}`,
		"scope not list":   `{"rules":[{"id":"a","text":"t","severity":"must","scope":{"profiles":"x"}}]}`,
		"not an object":    `[]`,
	} {
		_, err := Parse([]byte(body))
		assert.Error(t, err, name)
	}
	empty, err := Parse([]byte("  \n"))
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// Rules from the user, project and project-local files are merged in layer
// order, filtered by the run's project, profile and backend, and rendered
// must-first.
func TestRulesFromAllLayersAreMergedAndFilteredByScope(t *testing.T) {
	wf := workflowWithLayers(t,
		`{"rules":[
			{"id":"small-prs","severity":"should","text":"Keep pull requests small."},
			{"id":"other-project","severity":"must","text":"Only for billing.","scope":{"projects":["billing"]}}
		]}`,
		`{"rules":[
			{"id":"pnpm","severity":"must","text":"Use pnpm, never npm."},
			{"id":"codex-only","severity":"must","text":"Codex rule.","scope":{"backends":["codex"]}},
			{"id":"reviewer-only","severity":"should","text":"Reviewer rule.","scope":{"profiles":["reviewer"]}}
		]}`,
		`{"rules":[
			{"id":"local-web","severity":"must","text":"Run the dev server on 5174.","scope":{"projects":["acme-*"]}}
		]}`,
	)
	set := Resolve(wf, Target{Projects: ProjectNames(wf, ""), Profile: "", Backend: "claude"})
	assert.Empty(t, set.Errors)
	assert.Equal(t, []string{"pnpm", "local-web", "small-prs"}, set.IDs(), "must first, then should; out-of-scope rules dropped")
	layers := map[string]string{}
	for _, r := range set.Rules {
		layers[r.ID] = r.Layer
	}
	assert.Equal(t, map[string]string{"small-prs": LayerUser, "pnpm": LayerProject, "local-web": LayerLocal}, layers)

	block := set.Block()
	assert.True(t, strings.HasPrefix(block, "## Rules\n"))
	iMust, iPnpm, iLocal, iShould, iSmall := strings.Index(block, "### Must"), strings.Index(block, "Use pnpm"),
		strings.Index(block, "dev server"), strings.Index(block, "### Should"), strings.Index(block, "small")
	assert.True(t, iMust < iPnpm && iPnpm < iLocal && iLocal < iShould && iShould < iSmall, block)

	reviewer := Resolve(wf, Target{Projects: ProjectNames(wf, ""), Profile: "reviewer", Backend: "codex"})
	assert.Equal(t, []string{"pnpm", "codex-only", "local-web", "small-prs", "reviewer-only"}, reviewer.IDs())

	billing := Resolve(wf, Target{Projects: ProjectNames(wf, "billing"), Backend: "claude"})
	assert.Contains(t, billing.IDs(), "other-project", "a tracker project_slug names the project too")
}

// A later layer replaces a rule with the same id, except a user must rule:
// it survives and the conflict is reported.
func TestUserMustRuleSurvivesConflictingProjectRule(t *testing.T) {
	wf := workflowWithLayers(t,
		`{"rules":[
			{"id":"pnpm","severity":"must","text":"Use pnpm."},
			{"id":"tone","severity":"should","text":"Be terse."}
		]}`,
		`{"rules":[
			{"id":"pnpm","severity":"should","text":"npm is fine here."},
			{"id":"tone","severity":"should","text":"Be friendly."}
		]}`,
		`{"rules":[{"id":"PNPM","severity":"must","text":"Use yarn."}]}`,
	)
	set := Resolve(wf, Target{})
	byID := map[string]Effective{}
	for _, r := range set.Rules {
		byID[strings.ToLower(r.ID)] = r
	}
	assert.Equal(t, "Use pnpm.", byID["pnpm"].Text, "the user must rule stands")
	assert.Equal(t, LayerUser, byID["pnpm"].Layer)
	assert.Equal(t, "Be friendly.", byID["tone"].Text, "a should rule is replaced by a later layer")
	assert.Equal(t, LayerUser, byID["tone"].Overrides)
	require.Len(t, set.Conflicts, 2)
	assert.Equal(t, Conflict{ID: "pnpm", Layer: LayerProject, KeptLayer: LayerUser, Message: set.Conflicts[0].Message}, set.Conflicts[0])
	assert.Contains(t, set.Conflicts[0].Message, "cannot override")
	assert.Equal(t, LayerLocal, set.Conflicts[1].Layer)
}

func TestBrokenLayerIsSkippedAndReported(t *testing.T) {
	wf := workflowWithLayers(t, "", `{"rules":[{"id":"a"}]}`, `{"rules":[{"id":"b","severity":"must","text":"B."}]}`)
	set := Resolve(wf, Target{})
	assert.Equal(t, []string{"b"}, set.IDs())
	require.Len(t, set.Errors, 1)
	assert.Equal(t, LayerProject, set.Errors[0].Layer)
	assert.Contains(t, set.Errors[0].Message, "rules[0].text: required")
}

func TestRuleSetRecordHashAndIDs(t *testing.T) {
	a := Set{Rules: []Effective{{Rule: Rule{ID: "s", Severity: SeverityShould, Text: "S."}}, {Rule: Rule{ID: "m", Severity: SeverityMust, Text: "M."}}}}
	rec := a.Record()
	assert.Equal(t, []string{"m", "s"}, rec.IDs)
	assert.True(t, strings.HasPrefix(rec.Hash, "sha256:"))
	b := a
	b.Rules = []Effective{a.Rules[0], {Rule: Rule{ID: "m", Severity: SeverityMust, Text: "M changed."}}}
	assert.NotEqual(t, rec.Hash, b.Record().Hash, "the hash follows the rule text")
	assert.Equal(t, Record{}, Set{}.Record())
}

func TestTemplatePlacesRules(t *testing.T) {
	for _, tmpl := range []string{"{{ rules }}", "x {{- rules.must -}} y", "{{rules | strip}}", "{{ rules['should'] }}"} {
		assert.True(t, TemplatePlacesRules("no", tmpl), tmpl)
	}
	for _, tmpl := range []string{"", "rules", "{{ rules_extra }}", "{{ issue.rules }}"} {
		assert.False(t, TemplatePlacesRules(tmpl), tmpl)
	}
}

// The form save rewrites only what changed, so comments survive.
func TestSaveRulesKeepsComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".itervox", "rules.json")
	writeFile(t, path, `// Team rules — keep this header.
{
  "rules": [
    // Package manager.
    {"id": "pnpm", "severity": "must", "text": "Use pnpm."},
    {"id": "old", "severity": "should", "text": "Remove me."},
  ],
}
`)
	l := ReadLayer(LayerProject, path)
	require.NoError(t, l.Err)
	err := SaveRules(path, []Rule{
		{ID: "pnpm", Severity: SeverityMust, Text: "Use pnpm 9.", Description: "Lockfile is pnpm."},
		{ID: "small", Severity: SeverityShould, Text: "Small PRs.", Scope: Scope{Profiles: []string{"implementer"}}},
	}, l.Version)
	require.NoError(t, err)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(got), "// Team rules — keep this header.")
	assert.Contains(t, string(got), "// Package manager.")
	rules, err := Parse(got)
	require.NoError(t, err)
	assert.Equal(t, []string{"pnpm", "small"}, []string{rules[0].ID, rules[1].ID})
	assert.Equal(t, "Use pnpm 9.", rules[0].Text)
	assert.Equal(t, "Lockfile is pnpm.", rules[0].Description)
	assert.Equal(t, []string{"implementer"}, rules[1].Scope.Profiles)

	// A stale version is refused and the file is untouched.
	err = SaveRules(path, nil, l.Version)
	assert.ErrorIs(t, err, ErrStale)
	after, _ := os.ReadFile(path)
	assert.Equal(t, string(got), string(after))
}

func TestSaveRulesCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".itervox", "rules.local.json")
	require.NoError(t, SaveRules(path, []Rule{{ID: "a", Severity: SeverityMust, Text: "A."}}, ""))
	l := ReadLayer(LayerLocal, path)
	require.NoError(t, l.Err)
	assert.Equal(t, "a", l.Rules[0].ID)
	assert.Contains(t, string(l.Raw), SchemaURL)
}

func TestSaveRawValidatesAndKeepsText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	raw := "// mine\n{\"rules\": [{\"id\": \"a\", \"severity\": \"should\", \"text\": \"A.\"}]}\n"
	require.NoError(t, SaveRaw(path, []byte(raw), ""))
	got, _ := os.ReadFile(path)
	assert.Equal(t, raw, string(got))

	err := SaveRaw(path, []byte(`{"rules":[{"id":"a"}]}`), Version(got))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalid), err)
	after, _ := os.ReadFile(path)
	assert.Equal(t, raw, string(after), "an invalid save leaves the file alone")
}
