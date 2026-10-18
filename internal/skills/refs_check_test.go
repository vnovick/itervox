package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

// TestFindPromptRefsSpans pins the byte range of every occurrence: the whole
// "@agent-<name>" mention, and the name inside the backticks of a list.
// Repeats are kept (the editor underlines each one).
func TestFindPromptRefsSpans(t *testing.T) {
	text := "Ask @agent-code-reviewer, then the `tdd` and `deploy` skills. Again @agent-code-reviewer."
	spans := FindPromptRefs(text)
	require.Len(t, spans, 4)
	got := make([]string, 0, len(spans))
	for _, s := range spans {
		got = append(got, s.Kind+":"+text[s.Start:s.End])
	}
	assert.Equal(t, []string{
		"subagent:@agent-code-reviewer",
		"skill:tdd",
		"skill:deploy",
		"subagent:@agent-code-reviewer",
	}, got)
}

// TestCheckPromptRefsMissingVsValid: a missing skill gets doctor's
// MISSING_SKILL_REF issue; a valid one is ok with no issue.
func TestCheckPromptRefsMissingVsValid(t *testing.T) {
	text := "Use the `verify-before-done` skill and the `no-such-skill` skill."
	got := CheckPromptRefs(refInventory(), "worker", "claude", text, nil)
	require.Len(t, got, 2)
	assert.Equal(t, RefStatusOK, got[0].Status)
	assert.Nil(t, got[0].Issue)
	assert.Equal(t, RefStatusMissing, got[1].Status)
	require.NotNil(t, got[1].Issue)
	assert.Equal(t, "MISSING_SKILL_REF", got[1].Issue.ID)
	assert.Equal(t, "no-such-skill", text[got[1].Start:got[1].End])
}

// TestCheckPromptRefsPerBackend: a Codex profile cannot use Claude
// subagents or Claude-only skills; a Claude profile can, and the reverse
// holds for Codex-only skills.
func TestCheckPromptRefsPerBackend(t *testing.T) {
	text := "Ask @agent-code-reviewer. Use the `verify-before-done` skill and the `codex-only` skill."
	status := func(backend string) []string {
		var out []string
		for _, f := range CheckPromptRefs(refInventory(), "p", backend, text, nil) {
			out = append(out, f.Name+"="+f.Status)
		}
		return out
	}
	assert.Equal(t, []string{"code-reviewer=ok", "verify-before-done=ok", "codex-only=missing"}, status("claude"))
	assert.Equal(t, []string{"code-reviewer=missing", "verify-before-done=missing", "codex-only=ok"}, status("codex"))
}

// TestCheckPromptRefsOutsideRepoOnSSH: a user-scope reference is flagged
// with doctor's USER_SCOPE_REF_ON_SSH info when SSH hosts are configured.
func TestCheckPromptRefsOutsideRepoOnSSH(t *testing.T) {
	got := CheckPromptRefs(refInventory(), "p", "claude", "Use the `personal-notes` skill.", []string{"build-1"})
	require.Len(t, got, 1)
	assert.Equal(t, RefStatusOutsideRepo, got[0].Status)
	require.NotNil(t, got[0].Issue)
	assert.Equal(t, "USER_SCOPE_REF_ON_SSH", got[0].Issue.ID)
	assert.Contains(t, got[0].Issue.Description, "build-1")
}

// TestCheckPromptRefsAgreesWithDoctor: for the same prompt, the issues the
// editor check reports (de-duplicated) are exactly ValidateProfileRefs'.
func TestCheckPromptRefsAgreesWithDoctor(t *testing.T) {
	inv := refInventory()
	text := "Ask @agent-code-reviewer and @agent-ghost. Use the `personal-notes`, `nope` skills. Use the `nope` skill again."
	hosts := []string{"h1"}
	for _, backend := range []string{"claude", "codex"} {
		profile := config.AgentProfile{Command: backend, Instructions: text}
		doctor := ValidateProfileRefs(inv, map[string]config.AgentProfile{"p": profile}, RefBackendDefaults{}, hosts)
		var editor []InventoryIssue
		seen := map[string]bool{}
		for _, f := range CheckPromptRefs(inv, "p", backend, "\n"+text, hosts) {
			if f.Issue == nil || seen[f.Kind+f.Name] {
				continue
			}
			seen[f.Kind+f.Name] = true
			editor = append(editor, *f.Issue)
		}
		assert.Equal(t, doctor, editor, backend)
	}
}

func TestCheckPromptRefsNilInventory(t *testing.T) {
	assert.Nil(t, CheckPromptRefs(nil, "p", "claude", "Ask @agent-x.", nil))
}

// TestScanCodexPrompts lists ~/.codex/prompts/*.md (top level only) as
// Codex prompts with their frontmatter description.
func TestScanCodexPrompts(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "prompts")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "review.md"), []byte("---\ndescription: Review the diff\n---\nReview it.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain.md"), []byte("No frontmatter.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nested", "deep.md"), []byte("ignored"), 0o644))

	inv, err := Scan(t.TempDir(), home, ScanOptions{})
	require.NoError(t, err)
	require.Len(t, inv.Prompts, 2)
	assert.Equal(t, Prompt{Name: "plain", Provider: "codex", Source: "user-codex", FilePath: filepath.Join(dir, "plain.md"), ApproxTokens: 4}, inv.Prompts[0])
	assert.Equal(t, "review", inv.Prompts[1].Name)
	assert.Equal(t, "Review the diff", inv.Prompts[1].Description)
	assert.Contains(t, inv.WatchDirs, dir)

	none, err := Scan(t.TempDir(), home, ScanOptions{SkipCodex: true})
	require.NoError(t, err)
	assert.Empty(t, none.Prompts)
}
