import {
  AUTOMATION_TRIGGER_OPTIONS,
  type AutomationTriggerType,
} from '../../../types/automationTriggers';

export type { AutomationTriggerType };

export type InstructionTemplate = {
  id: string;
  label: string;
  description: string;
  instruction: string;
  triggerTypes?: AutomationTriggerType[];
};

export const TRIGGER_OPTIONS = AUTOMATION_TRIGGER_OPTIONS;

export { VARIABLE_GROUPS } from './automationVariableGroups';

export const INSTRUCTION_TEMPLATES: readonly InstructionTemplate[] = [
  {
    id: 'input-responder',
    label: 'Input responder',
    description: 'Low-risk unblocker answers for input-required automations.',
    triggerTypes: ['input_required'],
    instruction: `Answer only narrow, low-risk unblocker questions.

- Prefer the safest bounded assumption that keeps work moving.
- If the request is ambiguous, state the assumption explicitly.
- If the request needs real human approval, do not invent it.
- Use \`itervox action provide-input\` only when the profile is allowed to auto-resume.`,
  },
  {
    id: 'qa-validation',
    label: 'QA validation',
    description: 'Run checks, comment results, and move the issue back when validation fails.',
    triggerTypes: ['cron', 'issue_entered_state', 'tracker_comment_added'],
    instruction: `Run the QA routine for this issue.

- Validate the change against the issue description and tracker comments.
- Comment a concise pass/fail report on the issue.
- If a required check fails, move the issue back to Todo.
- If all required checks pass, explain what was validated.`,
  },
  {
    id: 'pm-backlog-review',
    label: 'PM backlog review',
    description: 'Review issue clarity, missing context, and acceptance criteria.',
    triggerTypes: ['cron', 'issue_moved_to_backlog', 'tracker_comment_added'],
    instruction: `Review the issue for missing product detail.

- Identify vague requirements, unstated assumptions, and missing acceptance criteria.
- Leave one concise comment summarising what is unclear.
- Do not invent scope that is not supported by the issue context.`,
  },
  {
    id: 'comment-triage',
    label: 'Comment triage',
    description: 'Evaluate a newly added tracker comment and decide whether it changes next steps.',
    triggerTypes: ['tracker_comment_added'],
    instruction: `Review the newly added tracker comment in context.

- Summarise whether the comment adds actionable new information.
- If the comment resolves a blocker, say what changed.
- If the comment creates new follow-up work, capture it clearly.`,
  },
  {
    id: 'failure-follow-up',
    label: 'Failure follow-up',
    description: 'Handle permanently failed runs with concise diagnosis and next-step guidance.',
    triggerTypes: ['run_failed'],
    instruction: `Review the failed run context.

- Summarise the likely failure mode using the automation trigger data and issue context.
- Comment the next best step for a human or another agent.
- If the issue should move back to backlog or Todo, do that explicitly.`,
  },
];

export function triggerDescription(triggerType: AutomationTriggerType): string {
  return (
    AUTOMATION_TRIGGER_OPTIONS.find((option) => option.value === triggerType)?.description ??
    'Choose what should wake this automation up.'
  );
}
