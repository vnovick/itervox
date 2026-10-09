package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/templates"
)

// TestOfferIssueTemplate (#83): the GitHub template is written only after
// confirmation (or --yes), never over an existing file; Linear gets the same
// sections printed after confirmation.
func TestOfferIssueTemplate(t *testing.T) {
	t.Run("github declined or EOF", func(t *testing.T) {
		for _, answer := range []string{"", "n\n", "maybe\n"} {
			dir := t.TempDir()
			var out strings.Builder
			offerIssueTemplate(dir, "github", false, bufioReader(answer), &out)
			assert.NoFileExists(t, filepath.Join(dir, agentTaskTemplateRel), "answer %q", answer)
			assert.Contains(t, out.String(), "Add an agent-ready issue template")
		}
	})
	t.Run("github accepted", func(t *testing.T) {
		dir := t.TempDir()
		var out strings.Builder
		offerIssueTemplate(dir, "github", false, bufioReader("y\n"), &out)
		raw, err := os.ReadFile(filepath.Join(dir, agentTaskTemplateRel))
		require.NoError(t, err)
		assert.Equal(t, string(templates.AgentTaskGitHubTemplate()), string(raw))
	})
	t.Run("github --yes", func(t *testing.T) {
		dir := t.TempDir()
		var out strings.Builder
		offerIssueTemplate(dir, "github", true, bufioReader(""), &out)
		assert.FileExists(t, filepath.Join(dir, agentTaskTemplateRel))
	})
	t.Run("github existing file is never overwritten or asked about", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, agentTaskTemplateRel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("MINE\n"), 0o644))
		var out strings.Builder
		offerIssueTemplate(dir, "github", true, bufioReader("y\n"), &out)
		raw, _ := os.ReadFile(path)
		assert.Equal(t, "MINE\n", string(raw))
		assert.Contains(t, out.String(), "already exists; leaving it as is")
		assert.NotContains(t, out.String(), "[y/N]")
	})
	t.Run("linear prints after confirmation", func(t *testing.T) {
		dir := t.TempDir()
		var out strings.Builder
		offerIssueTemplate(dir, "linear", false, bufioReader("y\n"), &out)
		assert.Contains(t, out.String(), linearTemplateBody())
		assert.Contains(t, out.String(), "## Acceptance criteria")
		assert.NotContains(t, out.String(), "Blocked by #", "Linear blockers come from its relation, not text")
		assert.NoDirExists(t, filepath.Join(dir, ".github"))

		var declined strings.Builder
		offerIssueTemplate(dir, "linear", false, bufioReader(""), &declined)
		assert.NotContains(t, declined.String(), "## Acceptance criteria")
	})
}

func TestWriteFileExclusiveRefusesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b.md")
	require.NoError(t, writeFileExclusive(path, []byte("one")))
	err := writeFileExclusive(path, []byte("two"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not overwriting")
	raw, _ := os.ReadFile(path)
	assert.Equal(t, "one", string(raw))
}

// TestInitIssueTemplateStepNeverBlocksWithoutTerminal (review of #83): init
// used to be non-interactive, so without a terminal it must not read stdin
// — a pipe that never closes would hang it — and with --issue-template it
// writes without asking.
func TestInitIssueTemplateStepNeverBlocksWithoutTerminal(t *testing.T) {
	pr, pw := io.Pipe() // never written or closed
	t.Cleanup(func() { _ = pw.Close() })

	dir := t.TempDir()
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		initIssueTemplateStep(dir, "github", false, false, bufio.NewReader(pr), &out)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("init's issue-template step blocked on stdin without a terminal")
	}
	assert.Contains(t, out.String(), "not offered (no terminal)")
	assert.NoFileExists(t, filepath.Join(dir, agentTaskTemplateRel))

	var flagged strings.Builder
	initIssueTemplateStep(dir, "github", true, false, bufio.NewReader(pr), &flagged)
	assert.FileExists(t, filepath.Join(dir, agentTaskTemplateRel), "--issue-template writes without asking")

	other := t.TempDir()
	var asked strings.Builder
	initIssueTemplateStep(other, "github", false, true, bufioReader("y\n"), &asked)
	assert.FileExists(t, filepath.Join(other, agentTaskTemplateRel), "a terminal is asked")

	var memory strings.Builder
	initIssueTemplateStep(t.TempDir(), "memory", true, true, bufioReader("y\n"), &memory)
	assert.Empty(t, memory.String())
}
