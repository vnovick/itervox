import { describe, it, expect } from 'vitest';
import {
  fmtMs,
  orchDotClass,
  priorityDotClass,
  stateBadgeColor,
  formatOrchestratorState,
} from '../format';

describe('fmtMs', () => {
  it('renders seconds when under 60s', () => {
    expect(fmtMs(5000)).toBe('5s');
    expect(fmtMs(59000)).toBe('59s');
  });

  it('renders minutes and seconds when 60s or more', () => {
    expect(fmtMs(60000)).toBe('1m 00s');
    expect(fmtMs(90000)).toBe('1m 30s');
    expect(fmtMs(3723000)).toBe('62m 03s');
  });

  it('handles 0ms', () => {
    expect(fmtMs(0)).toBe('0s');
  });
});

describe('orchDotClass', () => {
  it('returns green pulse for running', () => {
    expect(orchDotClass('running')).toBe('bg-green-500 animate-pulse');
  });

  it('returns yellow pulse for retrying', () => {
    expect(orchDotClass('retrying')).toBe('bg-yellow-400 animate-pulse');
  });

  it('returns red (no pulse) for paused', () => {
    expect(orchDotClass('paused')).toBe('bg-red-400');
  });

  it('returns orange pulse for pending input resume', () => {
    expect(orchDotClass('pending_input_resume')).toBe('bg-orange-400 animate-pulse');
  });

  it('returns gray for idle and unknown states', () => {
    expect(orchDotClass('idle')).toBe('bg-gray-300 dark:bg-gray-600');
    expect(orchDotClass('anything')).toBe('bg-gray-300 dark:bg-gray-600');
  });
});

describe('priorityDotClass', () => {
  it('returns null for falsy priority', () => {
    expect(priorityDotClass(null)).toBeNull();
    expect(priorityDotClass(undefined)).toBeNull();
    expect(priorityDotClass(0)).toBeNull();
  });

  it('returns red for P1', () => {
    expect(priorityDotClass(1)).toBe('bg-red-500');
  });

  it('returns orange for P2', () => {
    expect(priorityDotClass(2)).toBe('bg-orange-400');
  });

  it('returns yellow for P3', () => {
    expect(priorityDotClass(3)).toBe('bg-yellow-400');
  });

  it('returns gray for P4+', () => {
    expect(priorityDotClass(4)).toBe('bg-gray-400');
    expect(priorityDotClass(99)).toBe('bg-gray-400');
  });
});

describe('formatOrchestratorState', () => {
  it('formats pending input resume for display', () => {
    expect(formatOrchestratorState('pending_input_resume')).toBe('reply received');
  });

  it('formats underscore states generically', () => {
    expect(formatOrchestratorState('input_required')).toBe('input required');
  });
});

describe('stateBadgeColor', () => {
  it('returns warning for in-progress states', () => {
    expect(stateBadgeColor('In Progress')).toBe('warning');
    expect(stateBadgeColor('in_progress')).toBe('warning');
  });

  it('returns success for review and done states', () => {
    expect(stateBadgeColor('In Review')).toBe('success');
    expect(stateBadgeColor('Done')).toBe('success');
  });

  it('returns primary for todo states', () => {
    expect(stateBadgeColor('Todo')).toBe('primary');
    expect(stateBadgeColor('TODO')).toBe('primary');
  });

  it('returns light for unrecognised states', () => {
    expect(stateBadgeColor('Backlog')).toBe('light');
    expect(stateBadgeColor('')).toBe('light');
  });
});
