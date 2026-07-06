// Liquid template variable groups surfaced in the Variables picker on the
// automation editor. Extracted from automationEditorConstants.ts to keep
// that file under its size-budget cap. Trigger-context fields here mirror
// the Go AutomationTriggerContext struct in
// internal/orchestrator/automation.go.

export const VARIABLE_GROUPS = [
  {
    title: 'Issue variables',
    values: [
      '{{ issue.identifier }}',
      '{{ issue.title }}',
      '{{ issue.description }}',
      '{{ issue.state }}',
      '{{ issue.labels }}',
      '{{ issue.comments }}',
    ],
  },
  {
    title: 'Trigger variables',
    values: [
      '{{ trigger.type }}',
      '{{ trigger.fired_at }}',
      '{{ trigger.automation_id }}',
      '{{ trigger.cron }}',
      '{{ trigger.timezone }}',
      '{{ trigger.trigger_state }}',
      '{{ trigger.previous_state }}',
      '{{ trigger.current_state }}',
      '{{ trigger.input_context }}',
      '{{ trigger.blocked_profile }}',
      '{{ trigger.blocked_backend }}',
      '{{ trigger.comment.body }}',
      '{{ trigger.comment.author_name }}',
      '{{ trigger.error_message }}',
      '{{ trigger.retry_attempt }}',
      '{{ trigger.pr_url }}',
      '{{ trigger.pr_branch }}',
      '{{ trigger.pr_base_branch }}',
      '{{ trigger.failed_profile }}',
      '{{ trigger.failed_backend }}',
      '{{ trigger.prompt_tokens_total }}',
      '{{ trigger.completion_tokens_total }}',
      '{{ trigger.switched_to_profile }}',
      '{{ trigger.switched_to_backend }}',
    ],
  },
] as const;
