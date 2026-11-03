package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestMemoryConfigRoundTrip (#202): the memory: block parses its keys;
// absent, memory is on with a 2000-token budget inside an 8000-token
// rules-plus-memory share, and non-positive budgets fall back to those.
func TestMemoryConfigRoundTrip(t *testing.T) {
	lessons := defaultLessons()
	cases := []struct {
		name  string
		extra string
		want  config.MemoryConfig
	}{
		{"absent uses defaults", "", config.MemoryConfig{Enabled: true, TokenBudget: 2000, PromptShareTokens: 8000, Lessons: lessons}},
		{"all set", "memory:\n  enabled: false\n  token_budget: 500\n  prompt_share_tokens: 3000\n",
			config.MemoryConfig{Enabled: false, TokenBudget: 500, PromptShareTokens: 3000, Lessons: lessons}},
		{"non-positive falls back", "memory:\n  token_budget: 0\n  prompt_share_tokens: -1\n",
			config.MemoryConfig{Enabled: true, TokenBudget: 2000, PromptShareTokens: 8000, Lessons: lessons}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal(tc.extra)))
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Memory)
		})
	}
	m := config.MemoryConfig{TokenBudget: 2000, PromptShareTokens: 8000}
	assert.Equal(t, 2000, m.MemoryBudget(100))
	assert.Equal(t, 500, m.MemoryBudget(7500), "rules take their share first")
	assert.Equal(t, 0, m.MemoryBudget(9000))
}

func defaultLessons() config.LessonsConfig {
	return config.LessonsConfig{
		Enabled: true, Scope: "private", MaxPerIssue: 3, RateWindowHours: 24,
		DraftTokenBudget: 6000, DraftTimeoutMs: 60000, StaleAfterRuns: 20,
		EnforceThreshold: 3, RecipeMinFailedRuns: 2, RecipeMinTurns: 40,
	}
}

// TestMemoryLessonsConfigRoundTrip (#203): memory.lessons parses its keys;
// absent, lessons are on, drafted by the reviewer profile (profile ""),
// proposed into the private file and never auto-approved; auto_approve is a
// bool for both scopes or a per-scope map; an unknown scope and
// non-positive limits fall back to the defaults.
func TestMemoryLessonsConfigRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  func(*config.LessonsConfig)
	}{
		{"absent uses defaults", "", func(*config.LessonsConfig) {}},
		{"all set", "memory:\n  lessons:\n    enabled: false\n    profile: template\n    scope: project\n    auto_approve: true\n" +
			"    max_per_issue: 5\n    rate_window_hours: 2\n    draft_token_budget: 900\n    draft_timeout_ms: 5000\n    stale_after_runs: 7\n",
			func(l *config.LessonsConfig) {
				*l = config.LessonsConfig{Enabled: false, Profile: "template", Scope: "project", AutoApprovePrivate: true, AutoApproveProject: true,
					MaxPerIssue: 5, RateWindowHours: 2, DraftTokenBudget: 900, DraftTimeoutMs: 5000, StaleAfterRuns: 7,
					EnforceThreshold: 3, RecipeMinFailedRuns: 2, RecipeMinTurns: 40}
			}},
		// #217: the enforcement and recipe keys.
		{"round 2 keys", "memory:\n  lessons:\n    enforce_threshold: 5\n    enforce_issue_state: Backlog\n" +
			"    recipe_min_failed_runs: 3\n    recipe_min_turns: 25\n",
			func(l *config.LessonsConfig) {
				l.EnforceThreshold, l.EnforceIssueState, l.RecipeMinFailedRuns, l.RecipeMinTurns = 5, "Backlog", 3, 25
			}},
		{"round 2 bad values fall back", "memory:\n  lessons:\n    enforce_threshold: 0\n    recipe_min_turns: -1\n",
			func(*config.LessonsConfig) {}},
		{"per-scope auto_approve", "memory:\n  lessons:\n    auto_approve:\n      project: true\n",
			func(l *config.LessonsConfig) { l.AutoApproveProject = true }},
		{"bad values fall back", "memory:\n  lessons:\n    scope: team\n    max_per_issue: 0\n    draft_token_budget: -5\n",
			func(*config.LessonsConfig) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal(tc.extra)))
			require.NoError(t, err)
			want := defaultLessons()
			tc.want(&want)
			assert.Equal(t, want, cfg.Memory.Lessons)
		})
	}
	l := config.LessonsConfig{AutoApproveProject: true}
	assert.True(t, l.AutoApprove("project"))
	assert.False(t, l.AutoApprove("private"))
}
