import { describe, expect, it } from 'vitest';
import {
  AutomationDefSchema,
  ConfigInvalidStatusSchema,
  HistoryRowSchema,
  ProfileDefSchema,
  RetryRowSchema,
  RunningRowSchema,
  SSHHostInfoSchema,
  StateSnapshotSchema,
} from '../../../types/schemas';
import {
  makeAutomation,
  makeConfigInvalidStatus,
  makeHistoryRow,
  makeInputRequiredRow,
  makePendingInputResumeRow,
  makeProfileDef,
  makeRetryRow,
  makeRunningRow,
  makeSSHHostInfo,
  makeSnapshot,
} from '../snapshots';

describe('snapshot fixture factories', () => {
  it('every factory returns a schema-valid value with no overrides', () => {
    expect(() => RunningRowSchema.parse(makeRunningRow())).not.toThrow();
    expect(() => HistoryRowSchema.parse(makeHistoryRow())).not.toThrow();
    expect(() => RetryRowSchema.parse(makeRetryRow())).not.toThrow();
    expect(() =>
      ProfileDefSchema.parse(
        makeProfileDef({
          soul: '# Default SOUL',
          instructions: '# Default INSTRUCTIONS',
          soulFile: '.itervox/agents/default/SOUL.md',
          instructionsFile: '.itervox/agents/default/INSTRUCTIONS.md',
        }),
      ),
    ).not.toThrow();
    expect(() => AutomationDefSchema.parse(makeAutomation())).not.toThrow();
    expect(() => SSHHostInfoSchema.parse(makeSSHHostInfo())).not.toThrow();
    expect(() => ConfigInvalidStatusSchema.parse(makeConfigInvalidStatus())).not.toThrow();
    expect(() => StateSnapshotSchema.parse(makeSnapshot())).not.toThrow();
  });

  it('input-required rows have the right state values', () => {
    expect(makeInputRequiredRow().state).toBe('input_required');
    expect(makePendingInputResumeRow().state).toBe('pending_input_resume');
  });

  it('profile schema preserves file-backed SOUL and INSTRUCTIONS fields', () => {
    const profile = makeProfileDef({
      soul: '# Default SOUL',
      instructions: '# Default INSTRUCTIONS',
      soulFile: '.itervox/agents/default/SOUL.md',
      instructionsFile: '.itervox/agents/default/INSTRUCTIONS.md',
    });
    expect(profile.soul).toBe('# Default SOUL');
    expect(profile.instructions).toBe('# Default INSTRUCTIONS');
    expect(profile.soulFile).toBe('.itervox/agents/default/SOUL.md');
    expect(profile.instructionsFile).toBe('.itervox/agents/default/INSTRUCTIONS.md');
  });

  it('makeSnapshot derives counts from the running/retrying/paused arrays', () => {
    const snap = makeSnapshot({
      running: [makeRunningRow(), makeRunningRow({ identifier: 'X-2' })],
      retrying: [makeRetryRow()],
      paused: ['X-3', 'X-4', 'X-5'],
    });
    expect(snap.counts.running).toBe(2);
    expect(snap.counts.retrying).toBe(1);
    expect(snap.counts.paused).toBe(3);
  });

  it('explicit counts override beats derivation', () => {
    const snap = makeSnapshot({
      running: [makeRunningRow()],
      counts: { running: 99, retrying: 0, paused: 0 },
    });
    expect(snap.counts.running).toBe(99);
  });

  it('overrides deep-merge into nested objects already present on the base', () => {
    const snap = makeSnapshot({
      profileDefs: {
        default: { command: 'codex' },
      },
    });
    // command was overridden; prompt was not (came from base default)
    expect(snap.profileDefs?.default.command).toBe('codex');
    expect(snap.profileDefs?.default.prompt).toBe('Default prompt body.');
  });

  it('overriding an array replaces wholesale', () => {
    const snap = makeSnapshot({
      activeStates: ['Custom State'],
    });
    expect(snap.activeStates).toEqual(['Custom State']);
  });

  it('factories accept overrides for every documented optional field', () => {
    const row = makeRunningRow({
      identifier: 'CUSTOM-1',
      workerHost: 'custom-host',
      backend: 'remote',
      kind: 'reviewer',
      subagentCount: 2,
    });
    expect(row.identifier).toBe('CUSTOM-1');
    expect(row.workerHost).toBe('custom-host');
    expect(row.kind).toBe('reviewer');
  });

  it('makeRunningRow omits automation fields by default and surfaces them via overrides', () => {
    const manual = makeRunningRow();
    expect(manual.automationId).toBeUndefined();
    expect(manual.triggerType).toBeUndefined();
    expect(manual.commentCount).toBeUndefined();

    const automated = makeRunningRow({
      identifier: 'AUTO-1',
      kind: 'automation',
      automationId: 'pr-on-input',
      triggerType: 'input_required',
      commentCount: 2,
    });
    expect(automated.automationId).toBe('pr-on-input');
    expect(automated.triggerType).toBe('input_required');
    expect(automated.kind).toBe('automation');
    expect(automated.commentCount).toBe(2);
  });

  it('makeHistoryRow omits automation fields by default and surfaces them via overrides', () => {
    const manual = makeHistoryRow();
    expect(manual.automationId).toBeUndefined();
    expect(manual.triggerType).toBeUndefined();
    expect(manual.commentCount).toBeUndefined();

    const automated = makeHistoryRow({
      identifier: 'AUTO-9',
      kind: 'automation',
      automationId: 'cron-nightly',
      triggerType: 'cron',
      commentCount: 1,
      status: 'succeeded',
    });
    expect(automated.automationId).toBe('cron-nightly');
    expect(automated.triggerType).toBe('cron');
    expect(automated.commentCount).toBe(1);
  });

  it('makeSnapshot accepts automation queue backpressure alert data', () => {
    const snap = makeSnapshot({
      automationQueueBackpressure: {
        length: 100,
        maxLength: 100,
        saturated: true,
        pausedProducers: true,
        rejectedSinceBoot: 3,
        lastRejectedAt: '2026-05-20T13:00:00Z',
        lastRejectedReason: 'queue_full:nightly:cron:DEMO-1',
      },
    });

    expect(snap.automationQueueBackpressure).toEqual({
      length: 100,
      maxLength: 100,
      saturated: true,
      pausedProducers: true,
      rejectedSinceBoot: 3,
      lastRejectedAt: '2026-05-20T13:00:00Z',
      lastRejectedReason: 'queue_full:nightly:cron:DEMO-1',
    });
  });

  it('preserves automation queue, dependency audit, and dependency graph rows', () => {
    const raw: Record<string, unknown> = {
      ...makeSnapshot(),
      running: [
        makeRunningRow({
          identifier: 'DEMO-2',
          kind: 'automation',
          automationId: 'qa-validation',
          triggerType: 'cron',
        }),
      ],
      automationQueue: [
        {
          id: 'automation:nightly:cron:DEMO-1',
          automationId: 'nightly',
          triggerType: 'cron',
          identifier: 'DEMO-1',
          title: 'Queued cleanup',
          issueState: 'Backlog',
          profile: 'pm',
          status: 'queued',
          reason: 'no_slots',
          queuedAt: '2026-05-20T13:00:00Z',
          firedAt: '2026-05-20T13:00:00Z',
          attemptCount: 1,
          cron: '*/5 * * * *',
          timezone: 'UTC',
        },
        {
          id: 'automation:review-pr:pr_opened:DEMO-2:https://github.com/acme/repo/pull/7',
          automationId: 'review-pr',
          triggerType: 'pr_opened',
          identifier: 'DEMO-2',
          issueState: 'PR Open',
          profile: 'reviewer',
          backend: 'codex',
          status: 'blocked',
          reason: 'blocked_by',
          reasonDetail: 'DEMO-0',
          queuedAt: '2026-05-20T13:01:00Z',
          firedAt: '2026-05-20T13:01:00Z',
          attemptCount: 2,
          prUrl: 'https://github.com/acme/repo/pull/7',
          switchedToBackend: 'codex',
        },
      ],
      automationQueueBackpressure: {
        length: 2,
        maxLength: 2,
        saturated: true,
        pausedProducers: true,
        rejectedSinceBoot: 1,
        lastRejectedAt: '2026-05-20T13:02:00Z',
        lastRejectedReason: 'queue_full:nightly:cron:DEMO-3',
      },
      dependencyAudit: [
        {
          identifier: 'DEMO-2',
          issueState: 'Backlog',
          status: 'blocked',
          sources: ['tracker_relation'],
          blockedBy: [{ identifier: 'DEMO-0', state: 'In Progress' }],
          unresolvedBlockers: [{ identifier: 'DEMO-0', state: 'In Progress' }],
          wasBlocked: true,
          firstBlockedAt: '2026-05-20T12:00:00Z',
          lastAuditedAt: '2026-05-20T13:00:00Z',
        },
        {
          identifier: 'DEMO-3',
          issueState: 'Backlog',
          status: 'unblocked',
          sources: ['tracker_relation'],
          blockedBy: [{ identifier: 'DEMO-1', state: 'Done' }],
          resolvedBlockers: [{ identifier: 'DEMO-1', state: 'Done' }],
          wasBlocked: true,
          unblockedAt: '2026-05-20T13:05:00Z',
          lastAuditedAt: '2026-05-20T13:05:00Z',
          lastTransitionVersion: 2,
          lastTransitionReason: 'blockers_resolved',
        },
      ],
      dependencyGraphNodes: [
        {
          id: 'DEMO-1',
          identifier: 'DEMO-1',
          title: 'Terminal blocker',
          state: 'Done',
          status: 'unblocked',
          running: false,
          queued: true,
          terminal: true,
        },
        {
          id: 'DEMO-3',
          identifier: 'DEMO-3',
          title: 'Unblocked dependent',
          state: 'Backlog',
          status: 'unblocked',
          running: true,
          queued: false,
          terminal: false,
        },
      ],
      dependencyGraphEdges: [
        {
          id: 'DEMO-1->DEMO-3',
          sourceIdentifier: 'DEMO-1',
          targetIdentifier: 'DEMO-3',
          sourceState: 'Done',
          targetState: 'Backlog',
          resolved: true,
          sourceKnown: true,
        },
      ],
    };

    const parsed = StateSnapshotSchema.parse(raw);
    expect(parsed.automationQueue).toHaveLength(2);
    expect(parsed.automationQueueBackpressure?.saturated).toBe(true);
    expect(parsed.dependencyAudit).toHaveLength(2);
    expect(parsed.dependencyGraphNodes).toHaveLength(2);
    expect(parsed.dependencyGraphEdges?.[0]?.resolved).toBe(true);
  });
});
