import { describe, it, expect, afterEach } from 'vitest';
import { StateSnapshotSchema, setSchemaFallbackListener } from '../schemas';

// CORE-047 — a Go value the web does not know in any closed snapshot enum
// must degrade that one field, never reject the whole snapshot (which would
// freeze the production dashboard). Each enum falls back to a documented
// value and notifies the fallback listener (wired to CORE-048's reporter).

const BASE = {
  generatedAt: '2026-09-26T00:00:00Z',
  counts: { running: 0, retrying: 0, paused: 0 },
  running: [],
  retrying: [],
  paused: [],
  maxConcurrentAgents: 3,
  maxRetries: 5,
  maxSwitchesPerIssuePerWindow: 2,
  switchWindowHours: 6,
  rateLimits: null,
};

afterEach(() => {
  setSchemaFallbackListener(null);
});

describe('StateSnapshotSchema closed-enum tolerance', () => {
  it('accepts an unknown depsAnalysisMode and falls back to auto', () => {
    const parsed = StateSnapshotSchema.safeParse({ ...BASE, depsAnalysisMode: 'hybrid' });
    expect(parsed.success).toBe(true);
    expect(parsed.data?.depsAnalysisMode).toBe('auto');
  });

  it('keeps an absent depsAnalysisMode absent', () => {
    const parsed = StateSnapshotSchema.parse(BASE);
    expect(parsed.depsAnalysisMode).toBeUndefined();
  });

  it('degrades every closed enum on the snapshot instead of rejecting it', () => {
    const seen: string[] = [];
    setSchemaFallbackListener((label) => {
      seen.push(label);
    });
    const snap = {
      ...BASE,
      history: [
        {
          identifier: 'A-1',
          startedAt: 'x',
          finishedAt: 'x',
          elapsedMs: 1,
          turnCount: 1,
          tokens: 1,
          inputTokens: 1,
          outputTokens: 1,
          status: 'exploded',
        },
      ],
      supportedAgentActions: ['comment', 'teleport'],
      profileDefs: { p: { command: 'claude', allowedActions: ['teleport', 'merge_pr'] } },
      automations: [
        {
          id: 'a',
          enabled: true,
          profile: 'p',
          trigger: { type: 'on_full_moon' },
          filter: { matchMode: 'most' },
          policy: { switchToBackend: 'gemini' },
        },
      ],
      inputRequired: [
        { identifier: 'A-1', sessionId: 's', state: 'thinking', context: 'c', queuedAt: 'x' },
      ],
      automationQueue: [
        {
          id: 'q',
          automationId: 'a',
          triggerType: 'cron',
          identifier: 'A-1',
          profile: 'p',
          status: 'parked',
          reason: 'r',
          queuedAt: 'x',
          firedAt: 'x',
          attemptCount: 0,
        },
      ],
      dependencyAudit: [
        { identifier: 'A-1', issueState: 'Todo', status: 'maybe', wasBlocked: false },
      ],
      dependencyGraphNodes: [
        {
          id: 'A-1',
          identifier: 'A-1',
          status: 'maybe',
          running: false,
          queued: false,
          terminal: false,
        },
      ],
      depsAnalyzeJob: { jobId: 'j', status: 'paused', queuedAt: 'x' },
      depsAnalysisMode: 'hybrid',
    };
    const parsed = StateSnapshotSchema.safeParse(snap);
    expect(parsed.success, JSON.stringify(parsed.error?.issues)).toBe(true);
    if (!parsed.success) return;
    const data = parsed.data;
    expect(data.history?.[0].status).toBe('cancelled');
    expect(data.supportedAgentActions).toEqual(['comment']);
    expect(data.profileDefs?.p.allowedActions).toEqual(['merge_pr']);
    expect(data.automations?.[0].trigger.type).toBe('cron');
    expect(data.automations?.[0].filter?.matchMode).toBe('all');
    expect(data.automations?.[0].policy?.switchToBackend).toBe('');
    expect(data.inputRequired?.[0].state).toBe('input_required');
    expect(data.automationQueue?.[0].status).toBe('queued');
    expect(data.dependencyAudit?.[0].status).toBe('unknown');
    expect(data.dependencyGraphNodes?.[0].status).toBe('unknown');
    expect(data.depsAnalyzeJob?.status).toBe('failed');
    expect(data.depsAnalysisMode).toBe('auto');
    expect(seen.length).toBeGreaterThanOrEqual(12);
  });
});
