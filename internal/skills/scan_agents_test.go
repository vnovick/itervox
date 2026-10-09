package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeAgent(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, ".claude", "agents", rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func subagentsBySource(list []Subagent) map[string]Subagent {
	out := map[string]Subagent{}
	for _, a := range list {
		out[a.Source+"/"+a.Name] = a
	}
	return out
}

// TestScanClaudeAgentsProjectUserAndPlugin pins #86: subagents from the
// project's and the user's .claude/agents (nested dirs too) and from plugin
// manifests all appear in the inventory with their source.
func TestScanClaudeAgentsProjectUserAndPlugin(t *testing.T) {
	proj, home := t.TempDir(), t.TempDir()
	reviewerPath := writeAgent(t, proj, "code-reviewer.md", "---\nname: code-reviewer\ndescription: Reviews diffs\ntools: Read, Grep, Bash\nmodel: sonnet\n---\n\nYou review code.\n")
	writeAgent(t, proj, "team/security/auditor.md", "---\nname: security-auditor\ndescription: Audits\ntools:\n  - Read\n  - Grep\n---\nBody\n")
	writeAgent(t, proj, "README.md", "# Our agents\n\nNot an agent: no frontmatter.\n")
	writeAgent(t, proj, "broken.md", "---\nname: [unclosed\n---\n")
	writeAgent(t, proj, "nameless.md", "---\ndescription: no name\n---\n")
	writeAgent(t, proj, "notes.txt", "---\nname: not-markdown\n---\n")
	writeAgent(t, home, "planner.md", "---\nname: planner\ndescription: Plans work\n---\n")
	writePlugin(t, proj, "demo-plugin", fullPluginManifest)

	inv, err := Scan(proj, home, ScanOptions{SkipCodex: true})
	require.NoError(t, err)
	got := subagentsBySource(inv.Subagents)
	assert.Len(t, inv.Subagents, 4, "README, malformed, nameless and non-.md files are skipped: %+v", inv.Subagents)

	reviewer := got["project/code-reviewer"]
	assert.Equal(t, "Reviews diffs", reviewer.Description)
	assert.Equal(t, []string{"Read", "Grep", "Bash"}, reviewer.Tools)
	assert.Equal(t, "sonnet", reviewer.Model)
	assert.Equal(t, "claude", reviewer.Provider)
	assert.Equal(t, reviewerPath, reviewer.FilePath)
	assert.Positive(t, reviewer.ApproxTokens)

	assert.Equal(t, []string{"Read", "Grep"}, got["project/security-auditor"].Tools, "YAML list form and nested dirs")
	assert.Contains(t, got, "user/planner")
	assert.Equal(t, "code reviewer", got["plugin:demo-plugin/reviewer"].Description)

	assert.Contains(t, trackedInventoryFiles(inv, nil), filepath.Clean(reviewerPath),
		"subagent files gate the cache's staleness check")
}

func TestScanClaudeAgentsSkipsUserHomeWhenDisabled(t *testing.T) {
	proj, home := t.TempDir(), t.TempDir()
	writeAgent(t, home, "planner.md", "---\nname: planner\n---\n")
	inv, err := Scan(proj, home, ScanOptions{SkipCodex: true, SkipUserHome: true, SkipPlugins: true})
	require.NoError(t, err)
	assert.Empty(t, inv.Subagents)
}
