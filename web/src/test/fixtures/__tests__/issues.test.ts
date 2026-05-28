import { describe, expect, it } from 'vitest';
import { TrackerIssueSchema } from '../../../types/schemas';
import {
  makeBlockedIssue,
  makeInputRequiredIssue,
  makeIssue,
  makeIssues,
  makeLongTitleIssue,
  makePausedIssue,
  makePendingResumeIssue,
  makeRetryingIssue,
  makeRunningIssue,
  makeTerminalIssue,
} from '../issues';

describe('issue fixture factories', () => {
  it('every factory returns a schema-valid TrackerIssue', () => {
    const factories = [
      makeIssue(),
      makeLongTitleIssue(),
      makeBlockedIssue(),
      makeRunningIssue(),
      makeRetryingIssue(),
      makePausedIssue(),
      makeInputRequiredIssue(),
      makePendingResumeIssue(),
      makeTerminalIssue(),
    ];
    for (const issue of factories) {
      expect(() => TrackerIssueSchema.parse(issue)).not.toThrow();
    }
  });

  it('every orchestratorState the schema accepts is reachable from a factory', () => {
    expect(makeIssue().orchestratorState).toBe('idle');
    expect(makeRunningIssue().orchestratorState).toBe('running');
    expect(makeRetryingIssue().orchestratorState).toBe('retrying');
    expect(makePausedIssue().orchestratorState).toBe('paused');
    expect(makeInputRequiredIssue().orchestratorState).toBe('input_required');
    expect(makePendingResumeIssue().orchestratorState).toBe('pending_input_resume');
  });

  it('makeIssues produces sequential identifiers', () => {
    const issues = makeIssues(3);
    expect(issues.map((i) => i.identifier)).toEqual(['DEMO-1', 'DEMO-2', 'DEMO-3']);
  });

  it('makeIssues accepts a per-index override callback', () => {
    const issues = makeIssues(2, (i) => ({ state: i % 2 === 0 ? 'Todo' : 'Done' }));
    expect(issues[0].state).toBe('Todo');
    expect(issues[1].state).toBe('Done');
  });

  it('blocked issue carries blockedBy and ineligibleReason', () => {
    const issue = makeBlockedIssue();
    expect(issue.blockedBy).toEqual(['DEMO-1']);
    expect(issue.blockedByDetails).toEqual([
      {
        identifier: 'DEMO-1',
        state: 'In Progress',
        url: 'https://example.com/issues/DEMO-1',
      },
    ]);
    expect(issue.ineligibleReason).toMatch(/blocked by/);
  });

  it('parses issue status-change history from issue detail responses', () => {
    const issue = TrackerIssueSchema.parse({
      ...makeIssue(),
      statusChanges: [
        {
          fromState: 'Todo',
          toState: 'In Progress',
          source: 'worker_lifecycle',
          profileName: 'default',
          backend: 'codex',
          workerHost: 'ssh-build-1',
          at: '2026-05-20T09:31:00Z',
        },
        {
          fromState: 'In Progress',
          toState: 'In Review',
          source: 'automation',
          automationId: 'dispatch-reviewer-on-pr',
          triggerType: 'pr_opened',
          at: '2026-05-20T10:04:00Z',
        },
      ],
    });

    expect(issue.statusChanges).toHaveLength(2);
    expect(issue.statusChanges?.[1].automationId).toBe('dispatch-reviewer-on-pr');
  });

  it('overrides deep-merge as expected', () => {
    const issue = makeIssue({ identifier: 'CUSTOM-1', labels: ['urgent'] });
    expect(issue.identifier).toBe('CUSTOM-1');
    expect(issue.labels).toEqual(['urgent']);
  });
});
