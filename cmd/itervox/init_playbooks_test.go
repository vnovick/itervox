package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/playbook"
)

// TestInitScaffoldsPlaybooksWithoutOverwriting (#218): `itervox init`
// writes .itervox/playbooks/{bug-fix,feature,refactor,perf,investigation,
// docs}.md; `itervox init --update` adds only the missing ones and never
// overwrites an operator's edit; the root .gitignore carve-outs keep the
// directory committable under a broad .itervox/ ignore.
func TestInitScaffoldsPlaybooksWithoutOverwriting(t *testing.T) {
	project := t.TempDir()
	itervoxDir := filepath.Join(project, ".itervox")
	var out bytes.Buffer
	scaffoldPlaybooks(itervoxDir, "itervox init", &out)
	assert.Contains(t, out.String(), "wrote 6 playbook(s)")
	dir := filepath.Join(itervoxDir, "playbooks")
	for _, name := range []string{"bug-fix", "feature", "refactor", "perf", "investigation", "docs"} {
		data, err := os.ReadFile(filepath.Join(dir, name+".md"))
		require.NoError(t, err, name)
		p, err := playbook.Parse(name, data)
		require.NoError(t, err, name)
		assert.NotEmpty(t, p.Description, name)
		assert.NotEmpty(t, p.Keywords, name)
		assert.NotEmpty(t, p.Block(), name)
	}

	// An operator edits one and deletes another; --update restores only
	// the deleted one.
	edited := filepath.Join(dir, "perf.md")
	require.NoError(t, os.WriteFile(edited, []byte("my own perf playbook\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(dir, "docs.md")))
	out.Reset()
	scaffoldPlaybooks(itervoxDir, "itervox init --update", &out)
	assert.Contains(t, out.String(), "wrote 1 playbook(s)")
	data, err := os.ReadFile(edited)
	require.NoError(t, err)
	assert.Equal(t, "my own perf playbook\n", string(data), "never overwritten")
	_, err = os.Stat(filepath.Join(dir, "docs.md"))
	require.NoError(t, err)
	out.Reset()
	scaffoldPlaybooks(itervoxDir, "itervox init --update", &out)
	assert.Empty(t, out.String(), "nothing missing, nothing written")

	// Committable: the carve-outs land next to agents/handoff.
	require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"), []byte(".itervox/\n"), 0o644))
	require.NoError(t, patchRootGitignoreForAgents(project))
	gi, err := os.ReadFile(filepath.Join(project, ".gitignore"))
	require.NoError(t, err)
	lines := strings.Split(string(gi), "\n")
	assert.Contains(t, lines, "!.itervox/playbooks/")
	assert.Contains(t, lines, "!.itervox/playbooks/**")
}
