package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

// schema1WorkflowWithComments is a schema-1 workflow modelled on this
// repository's own WORKFLOW.md: inline comments, full-line comment blocks
// (the GitHub label setup instructions), a commented-out key, quoted
// strings, flow sequences and a block-scalar hook — plus one profile with
// an inline prompt so the migration has something to move.
const schema1WorkflowWithComments = `---
# Itervox workflow for this repository.
tracker:
  kind: github
  api_key: $GITHUB_TOKEN # export GITHUB_TOKEN=ghp_...
  project_slug: vnovick/itervox
  # GitHub uses labels to map states. Labels must exist in your repo.
  # Create them with: gh label create "todo" --color "0075ca" --repo vnovick/itervox
  #                   gh label create "in-progress" --color "e4e669" --repo vnovick/itervox
  active_states: ["todo", "in-progress"]
  terminal_states: ["done", "cancelled"]
  working_state: "in-progress" # Label applied when an agent starts.
  completion_state: "in-review" # Label applied when the agent finishes.
  # backlog_states: ["backlog"]  # Shown in the Kanban; not auto-dispatched.
  backlog_states: ["backlog"]

polling:
  interval_ms: 60000

agent:
  command: codex
  backend: codex
  max_turns: 60 # keep runs bounded
  max_concurrent_agents: 3
  profiles:
    implementer:
      command: codex
      backend: codex
      prompt: |
        You are the implementer.
        Keep changes focused.

workspace:
  root: ~/.itervox/workspaces/itervox

hooks:
  before_run: |
    git fetch origin
    git checkout -B main origin/main

server:
  port: 8090 # dashboard
---

Body {{ issue.identifier }}.
`

var commentLineRE = regexp.MustCompile(`#.*$`)

// commentTexts returns every comment in the front matter (full-line and
// trailing), trimmed, so the test can assert each one survives verbatim.
func commentTexts(t *testing.T, front string) []string {
	t.Helper()
	var out []string
	inBlockScalar := false
	for _, line := range strings.Split(front, "\n") {
		trimmed := strings.TrimSpace(line)
		if inBlockScalar {
			if strings.HasPrefix(line, "    ") || trimmed == "" {
				continue
			}
			inBlockScalar = false
		}
		if strings.HasSuffix(trimmed, ": |") {
			inBlockScalar = true
			continue
		}
		if m := commentLineRE.FindString(line); m != "" {
			out = append(out, strings.TrimSpace(m))
		}
	}
	require.NotEmpty(t, out, "fixture must contain comments")
	return out
}

// TestMigrateWorkflowToSchema2PreservesCommentsAndKeyOrder pins #71: the
// schema-2 migration edits the YAML node tree, so every comment line and the
// original key order survive; only migrated keys change and the schema
// marker lands at the top of the front matter.
func TestMigrateWorkflowToSchema2PreservesCommentsAndKeyOrder(t *testing.T) {
	// The fixture's api_key is `$GITHUB_TOKEN`, resolved by config.Load; CI
	// runners do not export it.
	t.Setenv("GITHUB_TOKEN", "ghp_test_token")
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(workflowPath, []byte(schema1WorkflowWithComments), 0o644))

	originalFront, _, ok := splitWorkflowFrontMatter(schema1WorkflowWithComments)
	require.True(t, ok)
	originalComments := commentTexts(t, originalFront)
	originalOrder, err := frontMatterKeyOrder(originalFront)
	require.NoError(t, err)
	require.Equal(t, []string{"tracker", "polling", "agent", "workspace", "hooks", "server"}, originalOrder,
		"fixture keys are deliberately NOT alphabetical so re-sorting would be caught")

	result, err := migrateWorkflowToSchema2(workflowPath, false, time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.True(t, result.Changed)

	// WORKFLOW.md.bak behaviour unchanged: byte-exact copy of the input.
	backup, err := os.ReadFile(workflowPath + ".bak")
	require.NoError(t, err)
	assert.Equal(t, schema1WorkflowWithComments, string(backup))

	rewritten, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	front, body, ok := splitWorkflowFrontMatter(string(rewritten))
	require.True(t, ok)
	assert.Equal(t, "\nBody {{ issue.identifier }}.\n", body, "prompt body is untouched")

	// Every comment survives verbatim (full-line blocks, trailing comments,
	// the commented-out key).
	for _, comment := range originalComments {
		assert.Contains(t, front, comment, "comment dropped by migration: %q", comment)
	}
	assert.Equal(t, len(originalComments), len(commentTexts(t, front)), "comment count must not change")

	// Key order is preserved; the schema marker is the first line.
	order, err := frontMatterKeyOrder(front)
	require.NoError(t, err)
	assert.Equal(t, append([]string{"itervox_schema_version"}, originalOrder...), order)
	assert.True(t, strings.HasPrefix(front, "# Itervox workflow for this repository.\nitervox_schema_version: 2\n") ||
		strings.HasPrefix(front, "itervox_schema_version: 2\n"),
		"schema marker must be at the top of the front matter, got:\n%s", front)

	// Nested order and styles survive too; only the migrated keys change.
	_, root, err := parseFrontMatterNode(front)
	require.NoError(t, err)
	assert.Equal(t, []string{"kind", "api_key", "project_slug", "active_states", "terminal_states",
		"working_state", "completion_state", "backlog_states"},
		yamlNodeMappingEntries(yamlNodeMappingFor(root, "tracker")))
	assert.Equal(t, []string{"command", "backend", "max_turns", "max_concurrent_agents", "profiles", "deps_analyzer_profile"},
		yamlNodeMappingEntries(yamlNodeMappingFor(root, "agent")),
		"existing agent keys keep their order; the scaffolded field is appended")
	assert.Equal(t, []string{"command", "backend", "soul_file", "instructions_file"},
		yamlNodeMappingEntries(yamlNodeMappingFor(root, "agent", "profiles", "implementer")),
		"prompt is replaced by the file references at the end of the profile")
	assert.Contains(t, front, `active_states: ["todo", "in-progress"]`, "flow sequence style kept")
	assert.Contains(t, front, `working_state: "in-progress" # Label applied when an agent starts.`)
	assert.Contains(t, front, "api_key: $GITHUB_TOKEN # export GITHUB_TOKEN=ghp_...")
	assert.Contains(t, front, "  before_run: |\n    git fetch origin\n    git checkout -B main origin/main\n", "block scalar kept")
	assert.NotContains(t, front, "prompt:")
	assert.NotContains(t, front, "You are the implementer.")
	for _, section := range []string{"polling", "agent", "workspace", "hooks", "server"} {
		assert.Contains(t, front, "\n\n"+section+":\n", "blank line before %s section must be restored", section)
	}

	// The rewritten file still loads and validates as schema 2.
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg))
	assert.Equal(t, 2, cfg.SchemaVersion)
	assert.Equal(t, []string{"todo", "in-progress"}, cfg.Tracker.ActiveStates)
	assert.Contains(t, cfg.Agent.Profiles["implementer"].Instructions, "Keep changes focused.")

	// A second run is a no-op that leaves the file byte-identical.
	second, err := migrateWorkflowToSchema2(workflowPath, false, time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.False(t, second.Changed)
	again, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	assert.Equal(t, string(rewritten), string(again))
}

// TestMigrateWorkflowToSchema2KeepsExistingIndentWidth pins that a workflow
// indented with four spaces is re-encoded with four spaces, so the diff is
// limited to the migrated keys.
func TestMigrateWorkflowToSchema2KeepsExistingIndentWidth(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	legacy := "---\ntracker:\n    kind: linear # four-space indent\n    api_key: key\n    project_slug: proj\nagent:\n    profiles:\n        implementer:\n            command: claude\n            prompt: Old prompt.\n---\n\nBody.\n"
	require.NoError(t, os.WriteFile(workflowPath, []byte(legacy), 0o644))

	_, err := migrateWorkflowToSchema2(workflowPath, false, time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	rewritten, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	front := string(rewritten)
	assert.True(t, strings.HasPrefix(front, "---\nitervox_schema_version: 2\ntracker:\n    kind: linear # four-space indent\n"), "got:\n%s", front)
	assert.Contains(t, front, "        implementer:\n            command: claude\n            soul_file: .itervox/agents/implementer/SOUL.md\n")
}

func TestDetectFrontMatterIndent(t *testing.T) {
	assert.Equal(t, 2, detectFrontMatterIndent("tracker:\n  kind: x\n"))
	assert.Equal(t, 4, detectFrontMatterIndent("# comment\ntracker:\n    kind: x\n"))
	assert.Equal(t, 2, detectFrontMatterIndent("flat: 1\n"), "nothing indented → default")
	assert.Equal(t, 2, detectFrontMatterIndent(""), "empty → default")
}

func TestRestoreTopLevelBlankLines(t *testing.T) {
	original := "a: 1\n\n# about b\nb: 2\nc: 3\n\nd:\n  e: 4\n"
	encoded := "x: 0\na: 1\n# about b\nb: 2\nc: 3\nd:\n  e: 4\n"
	assert.Equal(t, "x: 0\na: 1\n\n# about b\nb: 2\nc: 3\n\nd:\n  e: 4\n", restoreTopLevelBlankLines(original, encoded),
		"blank line lands above a key's comment block; keys without one are left alone")
	assert.Equal(t, encoded, restoreTopLevelBlankLines("a: 1\nb: 2\n", encoded), "no blank lines in the original → unchanged")
	assert.Equal(t, "a: 1\n\nb: 2\n", restoreTopLevelBlankLines(original, "a: 1\n\nb: 2\n"), "never doubles an existing blank line")
}

func TestParseFrontMatterNodeEdgeCases(t *testing.T) {
	for _, front := range []string{"", "\n", "# only a comment\n", "~\n"} {
		doc, root, err := parseFrontMatterNode(front)
		require.NoError(t, err, "front=%q", front)
		require.NotNil(t, doc)
		require.Equal(t, 1, len(doc.Content))
		assert.Equal(t, root, doc.Content[0])
		yamlNodeInsertFirst(root, "itervox_schema_version", yamlIntNode(2))
		encoded, err := encodeFrontMatterNode(doc, 2)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), "itervox_schema_version: 2\n", "front=%q", front)
	}
	_, _, err := parseFrontMatterNode("- a\n- b\n")
	require.Error(t, err, "a sequence is not a workflow front matter")
}
