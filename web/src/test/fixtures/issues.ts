// Issue fixture factories. Each ends in `TrackerIssueSchema.parse(...)` so a
// schema drift fails on construction, not at the assertion site.

import { TrackerIssueSchema, type TrackerIssue } from '../../types/schemas';
import { applyOverrides, type DeepPartial } from './deepPartial';

export function makeIssue(overrides?: DeepPartial<TrackerIssue>): TrackerIssue {
  const base: TrackerIssue = {
    identifier: 'DEMO-1',
    title: 'Demo issue',
    state: 'Todo',
    description: 'Demo description.',
    url: 'https://example.com/issues/DEMO-1',
    orchestratorState: 'idle',
    labels: [],
    blockedBy: [],
    blockedByDetails: [],
    comments: [],
    statusChanges: [],
  };
  return TrackerIssueSchema.parse(applyOverrides(base, overrides));
}

export function makeIssues(
  count: number,
  overridesForIndex?: (i: number) => DeepPartial<TrackerIssue>,
): TrackerIssue[] {
  return Array.from({ length: count }, (_v, i) =>
    makeIssue({
      identifier: `DEMO-${String(i + 1)}`,
      title: `Demo issue ${String(i + 1)}`,
      ...(overridesForIndex ? overridesForIndex(i) : {}),
    }),
  );
}

export function makeLongTitleIssue(overrides?: DeepPartial<TrackerIssue>): TrackerIssue {
  return makeIssue({
    identifier: 'DEMO-LONG',
    title:
      'A very long demo title that should wrap responsively across breakpoints without overflowing the layout container — this also stress-tests the issue-detail slide rendering on narrow widths.',
    ...overrides,
  });
}

export function makeBlockedIssue(overrides?: DeepPartial<TrackerIssue>): TrackerIssue {
  return makeIssue({
    identifier: 'DEMO-BLOCKED',
    title: 'Blocked issue',
    state: 'Todo',
    orchestratorState: 'idle',
    blockedBy: ['DEMO-1'],
    blockedByDetails: [
      {
        identifier: 'DEMO-1',
        state: 'In Progress',
        url: 'https://example.com/issues/DEMO-1',
      },
    ],
    ineligibleReason: 'blocked by DEMO-1',
    ...overrides,
  });
}

export function makeRunningIssue(overrides?: DeepPartial<TrackerIssue>): TrackerIssue {
  return makeIssue({
    identifier: 'DEMO-RUN',
    title: 'Running issue',
    state: 'In Progress',
    orchestratorState: 'running',
    turnCount: 3,
    tokens: 4200,
    elapsedMs: 30_000,
    lastMessage: 'Reading source files...',
    agentProfile: 'default',
    agentBackend: 'local',
    ...overrides,
  });
}

export function makeRetryingIssue(overrides?: DeepPartial<TrackerIssue>): TrackerIssue {
  return makeIssue({
    identifier: 'DEMO-RETRY',
    title: 'Retrying issue',
    state: 'In Progress',
    orchestratorState: 'retrying',
    error: 'rate_limit: try again in 30s',
    ...overrides,
  });
}

export function makePausedIssue(overrides?: DeepPartial<TrackerIssue>): TrackerIssue {
  return makeIssue({
    identifier: 'DEMO-PAUSED',
    title: 'Paused issue',
    state: 'In Review',
    orchestratorState: 'paused',
    branchName: 'feat/demo-paused',
    ...overrides,
  });
}

export function makeInputRequiredIssue(overrides?: DeepPartial<TrackerIssue>): TrackerIssue {
  return makeIssue({
    identifier: 'DEMO-INPUT-1',
    title: 'Awaiting input',
    state: 'In Progress',
    orchestratorState: 'input_required',
    lastMessage: 'Need confirmation before running migration.',
    ...overrides,
  });
}

export function makePendingResumeIssue(overrides?: DeepPartial<TrackerIssue>): TrackerIssue {
  return makeIssue({
    identifier: 'DEMO-INPUT-2',
    title: 'Pending resume',
    state: 'In Progress',
    orchestratorState: 'pending_input_resume',
    lastMessage: 'User input received; resuming next tick.',
    ...overrides,
  });
}

export function makeTerminalIssue(overrides?: DeepPartial<TrackerIssue>): TrackerIssue {
  return makeIssue({
    identifier: 'DEMO-DONE',
    title: 'Terminal issue',
    state: 'Done',
    orchestratorState: 'idle',
    ...overrides,
  });
}
