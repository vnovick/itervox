import type { AutomationTriggerType } from '../../../types/automationTriggers';

export interface SuggestedAutomation {
  id: string;
  label: string;
  description: string;
  profile: string;
  triggerType: AutomationTriggerType;
  instructions: string;
  triggerState?: string;
  cron?: string;
  timezone?: string;
  matchMode?: 'all' | 'any';
  states?: string[];
  labelsAny?: string[];
  identifierRegex?: string;
  limit?: number;
  inputContextRegex?: string;
  autoResume?: boolean;
}

export const SUGGESTED_AUTOMATIONS: readonly SuggestedAutomation[] = [
  {
    id: 'input-responder',
    label: 'Input Responder',
    description:
      'Dispatches a helper profile when a run blocks for input, using narrow trigger context and optional auto-resume.',
    profile: 'input-responder',
    triggerType: 'input_required',
    instructions: `Answer only narrow, low-risk unblocker questions.

- Prefer the safest bounded assumption that keeps work moving.
- If the blocked request is ambiguous, state the assumption explicitly.
- If the request needs real human approval, do not invent it.`,
    inputContextRegex: 'continue|branch|which file|test command',
    matchMode: 'all',
    autoResume: true,
  },
  {
    id: 'qa-validation',
    label: 'QA Validation',
    description:
      'Runs a QA profile on issues ready for verification, comments results, and pushes failures back to Todo.',
    profile: 'qa',
    triggerType: 'cron',
    cron: '0 */2 * * *',
    matchMode: 'all',
    instructions: `Run the QA routine for this issue.

- Validate the change against the issue description and comments.
- Comment a concise pass/fail report on the issue.
- If any required check fails, move the issue to Todo.`,
    states: ['Ready for QA'],
    limit: 10,
  },
  {
    id: 'pm-backlog-review',
    label: 'PM Backlog Review',
    description:
      'Reviews backlog issues for missing clarity, acceptance criteria, and scope gaps before engineering picks them up.',
    profile: 'pm',
    triggerType: 'cron',
    cron: '0 9 * * 1-5',
    matchMode: 'all',
    instructions: `Review the issue for missing product detail.

- Identify vague requirements, unstated assumptions, and missing acceptance criteria.
- Leave one concise comment summarising what is unclear.
- Do not rewrite the task or invent scope that is not supported by context.`,
    states: ['Backlog'],
    limit: 20,
  },
  {
    id: 'dependency-readiness',
    label: 'Dependency Readiness',
    description:
      'Runs a prompt-governed unblock check for blocked backlog issues and comments readiness advice.',
    profile: 'unblock-manager',
    triggerType: 'cron',
    cron: '*/30 * * * *',
    timezone: 'UTC',
    matchMode: 'all',
    states: ['Backlog'],
    labelsAny: ['blocked'],
    limit: 10,
    instructions: `Review blocker state for this issue.

- If any blocker is missing, unclear, or non-terminal, leave one concise comment and stop.
- If every blocker appears terminal and the issue is otherwise ready, comment that it appears ready for Todo.
- Do not claim deterministic dependency resolution; this is advisory automation only.`,
  },
  {
    id: 'plan-required-gate',
    label: 'Plan Required Gate',
    description:
      'Checks issues entering Todo for an explicit plan and comments when implementation should wait.',
    profile: 'readiness-manager',
    triggerType: 'issue_entered_state',
    triggerState: 'Todo',
    matchMode: 'all',
    labelsAny: ['needs-plan'],
    instructions: `Inspect the issue for a concrete implementation plan.

- If a plan exists, comment that the plan is present and name any remaining risks.
- If a plan is missing or vague, comment with the exact missing planning questions.
- Do not move state; this is a comment-only gate.`,
  },
  {
    id: 'planner-pair-claude',
    label: 'Planner Pair Claude',
    description:
      'Posts the Claude side of a two-planner design comparison when planning work is requested.',
    profile: 'planner-claude',
    triggerType: 'tracker_comment_added',
    matchMode: 'all',
    labelsAny: ['planning'],
    instructions: `If the latest comment asks for a Claude plan or planner-pair review, write the Claude plan.

Prefix the comment with ITERVOX PLAN CLAUDE. If the comment is unrelated, stop without posting.`,
  },
  {
    id: 'planner-pair-codex',
    label: 'Planner Pair Codex',
    description:
      'Posts the Codex side of a two-planner design comparison when planning work is requested.',
    profile: 'planner-codex',
    triggerType: 'tracker_comment_added',
    matchMode: 'all',
    labelsAny: ['planning'],
    instructions: `If the latest comment asks for a Codex plan or planner-pair review, write the Codex plan.

Prefix the comment with ITERVOX PLAN CODEX. If the comment is unrelated, stop without posting.`,
  },
  {
    id: 'debate-moderator',
    label: 'Debate Moderator',
    description:
      'Compares planner comments and posts a decision summary once both sides are present.',
    profile: 'debate-moderator',
    triggerType: 'tracker_comment_added',
    matchMode: 'all',
    labelsAny: ['planning'],
    instructions: `Look for ITERVOX PLAN CLAUDE and ITERVOX PLAN CODEX comments.

- If either plan is missing, comment that the debate is incomplete and stop.
- If both are present, compare risks, scope control, and verification strength.
- Post the selected plan or a merged plan. Do not move state.`,
  },
  {
    id: 'evaluator-optimizer',
    label: 'Evaluator Optimizer',
    description:
      'Runs a browser or QA evaluator on Ready for QA issues and comments improvement suggestions.',
    profile: 'qa-browser',
    triggerType: 'issue_entered_state',
    triggerState: 'Ready for QA',
    matchMode: 'all',
    labelsAny: ['ui'],
    instructions: `Evaluate the issue from the user's perspective.

- Run the relevant UI or acceptance-flow checks available in context.
- Comment pass/fail evidence and the smallest improvement that would raise confidence.
- Do not move state automatically.`,
  },
  {
    id: 'release-captain',
    label: 'Release Captain',
    description:
      'Audits release-candidate issues on a schedule and comments missing release evidence.',
    profile: 'release-captain',
    triggerType: 'cron',
    cron: '0 10 * * 1-5',
    timezone: 'UTC',
    matchMode: 'all',
    states: ['Ready for Release'],
    labelsAny: ['release'],
    limit: 10,
    instructions: `Review release-readiness evidence for this issue.

- Check tests, docs, changelog notes, unresolved blockers, and release checklist state.
- Comment go/no-go status and exact missing evidence.
- Do not tag, publish, or close issues.`,
  },
  {
    id: 'skills-hygiene',
    label: 'Skills Hygiene',
    description:
      'Reviews skills-inventory findings attached to a hygiene issue and comments cleanup recommendations.',
    profile: 'capability-curator',
    triggerType: 'cron',
    cron: '0 11 * * 1',
    timezone: 'UTC',
    matchMode: 'all',
    states: ['Backlog'],
    labelsAny: ['skills-hygiene'],
    limit: 5,
    instructions: `Review the latest skills inventory evidence mentioned on this issue.

- Identify duplicates, oversized profiles, unused capabilities, and risky hooks.
- Recommend manual cleanup steps with file paths when available.
- Do not edit user-owned skill, plugin, hook, or MCP files.`,
  },
];
