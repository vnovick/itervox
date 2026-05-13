package orchestrator

import "time"

func automationTriggerBindings(automation *AutomationDispatch) map[string]any {
	if automation == nil {
		return nil
	}
	trigger := automation.Trigger
	return map[string]any{
		"trigger": map[string]any{
			"type":                    trigger.Type,
			"fired_at":                trigger.FiredAt.Format(time.RFC3339),
			"automation_id":           trigger.AutomationID,
			"cron":                    trigger.Cron,
			"timezone":                trigger.Timezone,
			"trigger_state":           trigger.TriggerState,
			"input_context":           trigger.InputContext,
			"blocked_profile":         trigger.BlockedProfile,
			"blocked_backend":         trigger.BlockedBackend,
			"previous_state":          trigger.PreviousState,
			"current_state":           trigger.CurrentState,
			"error_message":           trigger.ErrorMessage,
			"will_retry":              trigger.WillRetry,
			"retry_attempt":           trigger.RetryAttempt,
			"retry_backoff_ms":        trigger.RetryBackoffMs,
			"comment_id":              trigger.CommentID,
			"comment_body":            trigger.CommentBody,
			"comment_author_id":       trigger.CommentAuthorID,
			"comment_author_name":     trigger.CommentAuthorName,
			"comment_created_at":      trigger.CommentCreatedAt,
			"pr_url":                  trigger.PRURL,
			"pr_branch":               trigger.PRBranch,
			"pr_base_branch":          trigger.PRBaseBranch,
			"failed_profile":          trigger.FailedProfile,
			"failed_backend":          trigger.FailedBackend,
			"prompt_tokens_total":     trigger.PromptTokensTotal,
			"completion_tokens_total": trigger.CompletionTokensTotal,
			"switched_to_profile":     trigger.SwitchedToProfile,
			"switched_to_backend":     trigger.SwitchedToBackend,
			"comment": map[string]any{
				"id":          trigger.CommentID,
				"body":        trigger.CommentBody,
				"author_id":   trigger.CommentAuthorID,
				"author_name": trigger.CommentAuthorName,
				"created_at":  trigger.CommentCreatedAt,
			},
		},
	}
}
