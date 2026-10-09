package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoctorWarnsOnMissingProfileRefs pins #86 in `itervox doctor`: a profile
// prompt naming a missing subagent or skill prints a WARNING, a valid
// reference prints nothing, and the exit code is unaffected.
func TestDoctorWarnsOnMissingProfileRefs(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	prev := doctorHomeDir
	doctorHomeDir = func() string { return home }
	t.Cleanup(func() { doctorHomeDir = prev })

	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	write(".claude/agents/code-reviewer.md", "---\nname: code-reviewer\ndescription: Reviews\n---\n")
	write(".claude/skills/verify/SKILL.md", "---\nname: verify-before-done\ndescription: Verify\n---\nBody\n")
	write(".itervox/agents/impl/SOUL.md", "You are the implementer.\n")
	write(".itervox/agents/impl/INSTRUCTIONS.md", "Use the `verify-before-done` skill, then ask @agent-code-reviewer and @agent-ghost-reviewer.\n")
	write("WORKFLOW.md", `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  profiles:
    impl:
      command: claude
      soul_file: .itervox/agents/impl/SOUL.md
      instructions_file: .itervox/agents/impl/INSTRUCTIONS.md
---

Prompt.
`)

	report, _ := collectDoctorReport(filepath.Join(dir, "WORKFLOW.md"))
	require.True(t, report.SchemaPassed, report.WorkflowError)
	require.Len(t, report.ProfileRefIssues, 1, "%+v", report.ProfileRefIssues)
	assert.Equal(t, "MISSING_SUBAGENT_REF", report.ProfileRefIssues[0].ID)

	out := renderDoctorReport(report)
	assert.Contains(t, out, `WARNING: Profile "impl" references unknown subagent "ghost-reviewer"`)
	assert.NotContains(t, out, `"code-reviewer"`, "a valid reference prints nothing")
	assert.NotContains(t, out, `"verify-before-done"`)
	assert.Equal(t, 0, doctorExitCode(DoctorReport{SchemaPassed: true, ProfileRefIssues: report.ProfileRefIssues}),
		"reference warnings never fail doctor")
}
