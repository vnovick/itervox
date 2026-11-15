package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/automationconfig"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
)

const automationGroupsWorkflow = `---
itervox_schema_version: 2
tracker:
  kind: memory
automations:
  - id: groups-only
    profile: fixer
    trigger: {type: cron, cron: "0 * * * *"}
    filter:
      - labels_any: [agent, ai]
        labels_none: [blocked]
      - labels_all: [urgent, backend]
  - id: groups-and-states
    profile: fixer
    trigger: {type: cron, cron: "0 * * * *"}
    filter:
      states: [Todo]
      groups: [{labels_any: [agent]}, {priority: [1]}]
  - id: legacy
    profile: fixer
    trigger: {type: cron, cron: "0 * * * *"}
    filter:
      labels_any: [triage]
---

Prompt.
`

// TestAutomationFilterAcceptsGroups (#198): an automation's filter accepts
// the tracker-filter group syntax — a list of groups alone, or groups:
// beside the other keys (one more check, combined by match_mode) — matched
// by the shared matcher; labels_any keeps working; the dashboard round-trip
// (definitions → WORKFLOW.md) keeps the groups; a malformed group is a
// validation error.
func TestAutomationFilterAcceptsGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(automationGroupsWorkflow), 0o600))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Automations, 3)
	assert.Equal(t, issuefilter.Filter{
		{LabelsAny: []string{"agent", "ai"}, LabelsNone: []string{"blocked"}},
		{LabelsAll: []string{"urgent", "backend"}},
	}, cfg.Automations[0].Filter.Groups)

	match := func(i int, is domain.Issue) bool {
		return automationEntryMatches(is, compiledAutomation{cfg: cfg.Automations[i]}, "")
	}
	assert.True(t, match(0, domain.Issue{Labels: []string{"ai"}}))
	assert.False(t, match(0, domain.Issue{Labels: []string{"ai", "blocked"}}), "none-of")
	assert.True(t, match(0, domain.Issue{Labels: []string{"urgent", "backend", "blocked"}}), "OR: the second group")
	assert.False(t, match(0, domain.Issue{Labels: []string{"urgent"}}), "all-of")

	one := 1
	assert.True(t, match(1, domain.Issue{State: "Todo", Priority: &one}))
	assert.False(t, match(1, domain.Issue{State: "Done", Priority: &one}), "states AND groups (match_mode all)")
	assert.False(t, match(1, domain.Issue{State: "Todo"}), "no group matches")

	assert.True(t, match(2, domain.Issue{Labels: []string{"triage"}}), "labels_any keeps working")
	assert.False(t, match(2, domain.Issue{Labels: []string{"docs"}}))

	// Dashboard round-trip: definitions → the settings file (#239; was
	// WORKFLOW.md) → config.
	defs := automationconfig.DefinitionsFromConfigs(cfg.Automations)
	assert.Equal(t, cfg.Automations[1].Filter.Groups, defs[1].Filter.Groups)
	require.NoError(t, persistSettings(path, []setting{{Key: "automations", Value: automationsSetting(defs)}}))
	again, err := config.Load(path)
	require.NoError(t, err)
	for i := range cfg.Automations {
		assert.Equal(t, cfg.Automations[i].Filter.Groups, again.Automations[i].Filter.Groups, cfg.Automations[i].ID)
	}

	for bad, want := range map[string]string{
		"filter: [{labels: [x]}]":  `unknown condition "labels"`,
		"filter: [{}]":             "a group needs at least one condition",
		"filter: [{assignee: me}]": `assignee "me" is not resolved in automation filters`,
	} {
		body := "---\nitervox_schema_version: 2\ntracker:\n  kind: memory\nautomations:\n  - id: a\n    profile: fixer\n    trigger: {type: cron, cron: \"0 * * * *\"}\n    " + bad + "\n---\n\nPrompt.\n"
		p := filepath.Join(t.TempDir(), "WORKFLOW.md")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		c, err := config.Load(p)
		if err == nil {
			err = config.ValidateAutomationsWithDefaults(c.Automations, map[string]config.AgentProfile{"fixer": {Command: "claude"}}, "claude")
		}
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), want, bad)
	}
}
