import { describe, it, expect } from 'vitest';
import type { IssueLogEntry } from '../../../../types/schemas';
import {
  clamp01,
  fromRunning,
  fromHistory,
  extractSubagents,
  filterByRun,
  dotStyle,
} from '../types';
import type { NormalisedSession } from '../types';

describe('clamp01', () => {
  it('clamps values below 0', () => {
    expect(clamp01(-0.5)).toBe(0);
  });
  it('clamps values above 1', () => {
    expect(clamp01(1.5)).toBe(1);
  });
  it('passes through values in range', () => {
    expect(clamp01(0.5)).toBe(0.5);
  });
  it('handles boundary values', () => {
    expect(clamp01(0)).toBe(0);
    expect(clamp01(1)).toBe(1);
  });
});

describe('fromRunning', () => {
  it('maps RunningRow to NormalisedSession with live status', () => {
    const row = {
      identifier: 'ENG-1',
      state: 'In Progress',
      startedAt: '2026-01-01T00:00:00Z',
      elapsedMs: 5000,
      turnCount: 3,
      tokens: 1200,
      inputTokens: 800,
      outputTokens: 400,
      lastEvent: 'working',
      lastEventAt: null,
      sessionId: 'sess-1',
      workerHost: 'local',
      backend: 'claude',
    };
    const result = fromRunning(row);
    expect(result.status).toBe('live');
    expect(result.identifier).toBe('ENG-1');
    expect(result.sessionId).toBe('sess-1');
  });

  it('propagates the F-1 automation context fields when present (T-5)', () => {
    const row = {
      identifier: 'ENG-AUTO',
      state: 'In Progress',
      startedAt: '2026-01-01T00:00:00Z',
      elapsedMs: 1000,
      turnCount: 1,
      tokens: 100,
      inputTokens: 80,
      outputTokens: 20,
      lastEvent: '',
      lastEventAt: null,
      sessionId: 'sess-auto',
      workerHost: 'local',
      backend: 'claude',
      automationId: 'pr-on-input',
      triggerType: 'input_required',
    };
    const result = fromRunning(row);
    expect(result.automationId).toBe('pr-on-input');
    expect(result.triggerType).toBe('input_required');
  });

  it('leaves automationId / triggerType undefined for manual runs', () => {
    const row = {
      identifier: 'ENG-MANUAL',
      state: 'In Progress',
      startedAt: '2026-01-01T00:00:00Z',
      elapsedMs: 1000,
      turnCount: 1,
      tokens: 100,
      inputTokens: 80,
      outputTokens: 20,
      lastEvent: '',
      lastEventAt: null,
      sessionId: 'sess-manual',
      workerHost: 'local',
      backend: 'claude',
    };
    const result = fromRunning(row);
    expect(result.automationId).toBeUndefined();
    expect(result.triggerType).toBeUndefined();
  });
});

describe('fromHistory', () => {
  it('maps HistoryRow to NormalisedSession preserving status', () => {
    const row = {
      identifier: 'ENG-2',
      title: 'Fix bug',
      startedAt: '2026-01-01T00:00:00Z',
      finishedAt: '2026-01-01T00:05:00Z',
      elapsedMs: 300000,
      turnCount: 10,
      tokens: 5000,
      status: 'succeeded' as const,
      sessionId: 'sess-2',
      workerHost: 'local',
      backend: 'claude',
    };
    const result = fromHistory(row);
    expect(result.status).toBe('succeeded');
    expect(result.title).toBe('Fix bug');
    expect(result.finishedAt).toBe('2026-01-01T00:05:00Z');
  });
});

describe('extractSubagents', () => {
  function makeEntry(event: string, message: string, sessionId?: string): IssueLogEntry {
    return {
      event,
      message,
      level: 'INFO',
      tool: '',
      time: '',
      sessionId,
    } as unknown as IssueLogEntry;
  }

  it('returns empty for empty logs', () => {
    expect(extractSubagents([])).toEqual([]);
  });

  it('extracts subagent segments', () => {
    const logs = [
      makeEntry('text', 'main work'),
      makeEntry('subagent', 'agent-1'),
      makeEntry('text', 'sub work 1'),
      makeEntry('text', 'sub work 2'),
      makeEntry('subagent', 'agent-2'),
      makeEntry('text', 'sub work 3'),
    ];
    const result = extractSubagents(logs);
    expect(result).toHaveLength(2);
    expect(result[0].name).toBe('agent-1');
    expect(result[0].logSlice).toHaveLength(3); // subagent marker + 2 entries before next
    expect(result[1].name).toBe('agent-2');
    expect(result[1].logSlice).toHaveLength(2); // subagent marker + 1 entry until end
  });

  it('filters by sessionId when provided', () => {
    const logs = [
      makeEntry('subagent', 'agent-1', 'sess-1'),
      makeEntry('text', 'work', 'sess-1'),
      makeEntry('text', 'other', 'sess-2'),
    ];
    const result = extractSubagents(logs, 'sess-1');
    expect(result).toHaveLength(1);
    expect(result[0].logSlice).toHaveLength(2);
  });
});

describe('filterByRun', () => {
  function makeEntry(
    event: string,
    message: string,
    opts?: { sessionId?: string; time?: string },
  ): IssueLogEntry {
    return {
      event,
      message,
      level: 'INFO',
      tool: '',
      time: opts?.time ?? '',
      sessionId: opts?.sessionId,
    } as unknown as IssueLogEntry;
  }

  const baseRun: NormalisedSession = {
    identifier: 'ENG-1',
    startedAt: '2026-01-01T00:00:00Z',
    finishedAt: '2026-01-01T00:05:00Z',
    elapsedMs: 300000,
    turnCount: 5,
    tokens: 1000,
    status: 'succeeded',
    sessionId: 'sess-1',
  };

  it('returns all logs when run is null', () => {
    const logs = [makeEntry('text', 'hello')];
    expect(filterByRun(logs, null)).toEqual(logs);
  });

  it('filters by sessionId when run has sessionId', () => {
    const logs = [
      makeEntry('text', 'match', { sessionId: 'sess-1' }),
      makeEntry('text', 'no-match', { sessionId: 'sess-2' }),
    ];
    const result = filterByRun(logs, baseRun);
    expect(result).toHaveLength(1);
    expect(result[0].message).toBe('match');
  });

  it('includes orchestrator messages by timestamp when no sessionId on entry', () => {
    const logs = [makeEntry('text', 'orch msg', { time: '2026-01-01T00:02:00Z' })];
    const result = filterByRun(logs, baseRun);
    expect(result).toHaveLength(1);
  });

  it('excludes entries outside run time window', () => {
    const logs = [
      makeEntry('text', 'too early', { time: '2025-12-31T00:00:00Z' }),
      makeEntry('text', 'too late', { time: '2026-01-02T00:00:00Z' }),
    ];
    const result = filterByRun(logs, baseRun);
    expect(result).toHaveLength(0);
  });
});

describe('dotStyle', () => {
  it('returns success style for live', () => {
    const style = dotStyle('live');
    expect(style.background).toBe('var(--success)');
  });
  it('returns accent for succeeded', () => {
    expect(dotStyle('succeeded').background).toBe('var(--accent)');
  });
  it('returns danger for failed', () => {
    expect(dotStyle('failed').background).toBe('var(--danger)');
  });
  it('returns muted for cancelled', () => {
    expect(dotStyle('cancelled').background).toBe('var(--muted)');
  });
});
