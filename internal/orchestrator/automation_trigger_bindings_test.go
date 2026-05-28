package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/prompt"
)

func TestAutomationTriggerBindingsExposePROpenedAndRateLimitedFields(t *testing.T) {
	firedAt := time.Date(2026, 5, 7, 12, 30, 0, 0, time.UTC)
	bindings := automationTriggerBindings(&AutomationDispatch{
		AutomationID: "rate-limit-fallback",
		Trigger: AutomationTriggerContext{
			Type:                  config.AutomationTriggerRateLimited,
			FiredAt:               firedAt,
			AutomationID:          "rate-limit-fallback",
			CurrentState:          "In Progress",
			PRURL:                 "https://github.com/acme/repo/pull/42",
			PRBranch:              "feature/fallback",
			PRBaseBranch:          "main",
			FailedProfile:         "claude-coder",
			FailedBackend:         "claude",
			PromptTokensTotal:     180000,
			CompletionTokensTotal: 22000,
			SwitchedToProfile:     "codex-coder",
			SwitchedToBackend:     "codex",
		},
	})

	trigger, ok := bindings["trigger"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://github.com/acme/repo/pull/42", trigger["pr_url"])
	assert.Equal(t, "feature/fallback", trigger["pr_branch"])
	assert.Equal(t, "main", trigger["pr_base_branch"])
	assert.Equal(t, "claude-coder", trigger["failed_profile"])
	assert.Equal(t, "claude", trigger["failed_backend"])
	assert.Equal(t, 180000, trigger["prompt_tokens_total"])
	assert.Equal(t, 22000, trigger["completion_tokens_total"])
	assert.Equal(t, "codex-coder", trigger["switched_to_profile"])
	assert.Equal(t, "codex", trigger["switched_to_backend"])
}

func TestAutomationTriggerBindingsRenderPROpenedAndRateLimitedLiquid(t *testing.T) {
	rendered := prompt.RenderPromptOverlay(
		"PR={{ trigger.pr_url }} profile={{ trigger.failed_profile }} switched={{ trigger.switched_to_profile }} tokens={{ trigger.prompt_tokens_total }}",
		domain.Issue{Identifier: "ENG-1", Title: "Fallback"},
		nil,
		automationTriggerBindings(&AutomationDispatch{
			AutomationID: "rate-limit-fallback",
			Trigger: AutomationTriggerContext{
				Type:                  config.AutomationTriggerRateLimited,
				FiredAt:               time.Date(2026, 5, 7, 12, 30, 0, 0, time.UTC),
				AutomationID:          "rate-limit-fallback",
				PRURL:                 "https://github.com/acme/repo/pull/42",
				FailedProfile:         "claude-coder",
				PromptTokensTotal:     180000,
				SwitchedToProfile:     "codex-coder",
				CompletionTokensTotal: 22000,
			},
		}),
	)

	assert.Contains(t, rendered, "PR=https://github.com/acme/repo/pull/42")
	assert.Contains(t, rendered, "profile=claude-coder")
	assert.Contains(t, rendered, "switched=codex-coder")
	assert.Contains(t, rendered, "tokens=180000")
}

func TestAutomationTriggerBindingsRenderBlockersResolvedLiquid(t *testing.T) {
	blockerIdentifier := "ENG-1"
	unblockedAt := time.Date(2026, 5, 7, 12, 30, 0, 0, time.UTC)
	bindings := automationTriggerBindings(&AutomationDispatch{
		AutomationID: "unblock-backlog-to-todo",
		MoveToState:  "Todo",
		Trigger: AutomationTriggerContext{
			Type:                   config.AutomationTriggerBlockersResolved,
			AutomationID:           "unblock-backlog-to-todo",
			ResolvedBlockers:       []domain.BlockerRef{{Identifier: &blockerIdentifier}},
			PreviouslyBlockedBy:    []domain.BlockerRef{{Identifier: &blockerIdentifier}},
			DependencyAuditVersion: 4,
			DependencyUnblockedAt:  unblockedAt,
			MoveToState:            "Todo",
		},
	})
	trigger, ok := bindings["trigger"].(map[string]any)
	require.True(t, ok)
	require.Len(t, trigger["resolved_blockers"], 1)
	require.Len(t, trigger["previously_blocked_by"], 1)
	assert.Equal(t, int64(4), trigger["dependency_audit_version"])
	assert.Equal(t, "2026-05-07T12:30:00Z", trigger["dependency_unblocked_at"])
	assert.Equal(t, "Todo", trigger["move_to_state"])

	rendered := prompt.RenderPromptOverlay(
		"version={{ trigger.dependency_audit_version }} unblocked={{ trigger.dependency_unblocked_at }} move={{ trigger.move_to_state }}",
		domain.Issue{Identifier: "ENG-2", Title: "Ready"},
		nil,
		bindings,
	)

	assert.Contains(t, rendered, "version=4")
	assert.Contains(t, rendered, "unblocked=2026-05-07T12:30:00Z")
	assert.Contains(t, rendered, "move=Todo")
}
