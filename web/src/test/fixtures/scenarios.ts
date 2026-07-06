// Named scenarios composed from the snapshot/issue/log factories. These mirror
// the "Required Existing-Functionality Scenarios" section in qa_framework.md
// (lines 578-768). Both Vitest and Playwright tests must reference these by
// name so a UI change must update one canonical place.

import type { IssueLogEntry, StateSnapshot, TrackerIssue } from '../../types/schemas';
import {
  makeBlockedIssue,
  makeInputRequiredIssue,
  makeIssue,
  makePausedIssue,
  makePendingResumeIssue,
  makeRetryingIssue,
  makeRunningIssue,
  makeTerminalIssue,
} from './issues';
import { makeAllEventTypes, makeLogEntry, makeSubLogEntries } from './logs';
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
} from './snapshots';

export interface Scenario {
  snapshot: StateSnapshot;
  issues: TrackerIssue[];
  logs: Record<string, IssueLogEntry[]>;
}

export const emptyScenario: Scenario = {
  snapshot: makeSnapshot({
    running: [],
    retrying: [],
    paused: [],
    history: [],
    inputRequired: [],
  }),
  issues: [],
  logs: {},
};

export const quickstartScenario: Scenario = {
  snapshot: makeSnapshot({
    trackerKind: 'memory',
    projectName: 'Quickstart Demo',
    availableProfiles: ['default', 'reviewer'],
    profileDefs: {
      default: makeProfileDef(),
      reviewer: makeProfileDef({ command: 'codex', allowedActions: ['comment'] }),
    },
  }),
  issues: [
    makeIssue({ identifier: 'DEMO-1', title: 'Implement greeting', state: 'Todo' }),
    makeIssue({ identifier: 'DEMO-2', title: 'Wire up CLI', state: 'In Progress' }),
    makeTerminalIssue({ identifier: 'DEMO-3', title: 'Initial commit', state: 'Done' }),
  ],
  logs: {
    'DEMO-2': [makeLogEntry('text'), makeLogEntry('action'), makeLogEntry('turn')],
  },
};

export const activeRunScenario: Scenario = (() => {
  const running = [
    makeRunningRow({
      identifier: 'DEMO-RUN-1',
      backend: 'local',
      lastEvent: 'tool_use:Edit',
    }),
    makeRunningRow({
      identifier: 'DEMO-RUN-2',
      backend: 'remote-1',
      workerHost: 'remote-1.example.com',
      kind: 'reviewer',
      lastEvent: 'tool_use:Read',
    }),
  ];
  const issues = [
    makeRunningIssue({ identifier: 'DEMO-RUN-1' }),
    makeRunningIssue({ identifier: 'DEMO-RUN-2', agentBackend: 'remote-1' }),
  ];
  return {
    snapshot: makeSnapshot({ running, history: [makeHistoryRow()] }),
    issues,
    logs: { 'DEMO-RUN-1': [makeLogEntry('text'), makeLogEntry('action')] },
  };
})();

export const inputRequiredScenario: Scenario = {
  snapshot: makeSnapshot({
    inputRequired: [makeInputRequiredRow(), makePendingInputResumeRow()],
    inlineInput: true,
  }),
  issues: [makeInputRequiredIssue(), makePendingResumeIssue()],
  logs: {},
};

export const retryAndPausedScenario: Scenario = {
  snapshot: makeSnapshot({
    retrying: [makeRetryRow()],
    paused: ['DEMO-PAUSED-1'],
    pausedWithPR: { 'DEMO-PAUSED-1': 'https://example.com/pr/42' },
  }),
  issues: [
    makeRetryingIssue({ identifier: 'DEMO-3' }),
    makePausedIssue({ identifier: 'DEMO-PAUSED-1' }),
    makeBlockedIssue({ identifier: 'DEMO-BLOCKED-1' }),
  ],
  logs: {},
};

export const configInvalidScenario: Scenario = {
  snapshot: makeSnapshot({ configInvalid: makeConfigInvalidStatus() }),
  issues: [makeIssue()],
  logs: {},
};

export const timelineLogsScenario: Scenario = {
  snapshot: makeSnapshot({
    history: [
      makeHistoryRow({ identifier: 'DEMO-OK', status: 'succeeded' }),
      makeHistoryRow({ identifier: 'DEMO-FAIL', status: 'failed' }),
      makeHistoryRow({ identifier: 'DEMO-CANCEL', status: 'cancelled' }),
      makeHistoryRow({ identifier: 'DEMO-STALL', status: 'stalled' }),
      makeHistoryRow({ identifier: 'DEMO-INPUT', status: 'input_required' }),
    ],
  }),
  issues: [
    makeTerminalIssue({ identifier: 'DEMO-OK', state: 'Done' }),
    makeIssue({ identifier: 'DEMO-FAIL', state: 'Done', error: 'tool_failure' }),
  ],
  logs: {
    'DEMO-OK': makeAllEventTypes(),
    'DEMO-FAIL': [makeLogEntry('error'), ...makeSubLogEntries(3, 'sess-demo-fail')],
  },
};

export const settingsMatrixScenario: Scenario = {
  snapshot: makeSnapshot({
    availableProfiles: ['default', 'reviewer', 'writer'],
    profileDefs: {
      default: makeProfileDef(),
      reviewer: makeProfileDef({ command: 'codex', allowedActions: ['comment'] }),
      writer: makeProfileDef({ command: 'claude', backend: 'remote-1' }),
    },
    reviewerProfile: 'reviewer',
    autoReview: true,
    automations: [
      makeAutomation(),
      makeAutomation({
        id: 'auto-2',
        trigger: { type: 'input_required' },
        profile: 'writer',
      }),
    ],
    activeStates: ['In Progress'],
    terminalStates: ['Done'],
    completionState: 'Done',
    backlogStates: ['Backlog'],
    sshHosts: [makeSSHHostInfo(), makeSSHHostInfo({ host: 'remote-2.example.com' })],
    dispatchStrategy: 'least-busy',
    autoClearWorkspace: true,
  }),
  issues: [makeIssue()],
  logs: {},
};

export const mobileShellScenario: Scenario = {
  snapshot: quickstartScenario.snapshot,
  issues: [
    ...quickstartScenario.issues,
    makeIssue({
      identifier: 'DEMO-LONG',
      title:
        'A very long demo title that should wrap responsively across breakpoints without overflowing the layout container.',
    }),
  ],
  logs: quickstartScenario.logs,
};

// automationsPassScenario — fixture for the Lane 2 e2e spec covering the
// F-1..T-10 automations UI pass. Carries:
//   - 2 configured automations (cron-nightly + pr-on-input)
//   - history rows tagged with automationId so Activity tab + sparkline +
//     hero stat all hydrate
//   - a running reviewer with commentCount > 0 so the IssueCard review
//     badge renders
//   - a single AUTOMATION FIRED log line on DEMO-AUTO so the Logs filter
//     chip can be exercised
const _automationsPassToday = new Date();
_automationsPassToday.setHours(9, 0, 0, 0);
const _automationsPassYesterday = new Date(_automationsPassToday.getTime() - 24 * 60 * 60 * 1000);

export const automationsPassScenario: Scenario = {
  snapshot: makeSnapshot({
    availableProfiles: ['default', 'reviewer'],
    profileDefs: {
      default: makeProfileDef(),
      reviewer: makeProfileDef({ command: 'codex', allowedActions: ['comment'] }),
    },
    reviewerProfile: 'reviewer',
    autoReview: true,
    automations: [
      makeAutomation({ id: 'cron-nightly', profile: 'reviewer' }),
      makeAutomation({
        id: 'pr-on-input',
        profile: 'reviewer',
        trigger: { type: 'input_required' },
      }),
    ],
    activeStates: ['In Progress'],
    backlogStates: ['Backlog'],
    terminalStates: ['Done'],
    completionState: 'Done',
    running: [
      makeRunningRow({
        identifier: 'DEMO-AUTO',
        kind: 'reviewer',
        backend: 'codex',
        automationId: 'pr-on-input',
        triggerType: 'input_required',
        commentCount: 2,
      }),
    ],
    history: [
      makeHistoryRow({
        identifier: 'DEMO-1',
        automationId: 'cron-nightly',
        triggerType: 'cron',
        startedAt: _automationsPassToday.toISOString(),
        finishedAt: _automationsPassToday.toISOString(),
      }),
      makeHistoryRow({
        identifier: 'DEMO-2',
        automationId: 'cron-nightly',
        triggerType: 'cron',
        startedAt: _automationsPassToday.toISOString(),
        finishedAt: _automationsPassToday.toISOString(),
      }),
      // Yesterday — should NOT count toward "automations triggered today".
      makeHistoryRow({
        identifier: 'DEMO-OLD',
        automationId: 'pr-on-input',
        triggerType: 'input_required',
        startedAt: _automationsPassYesterday.toISOString(),
        finishedAt: _automationsPassYesterday.toISOString(),
      }),
      // Manual run — should not appear under any automation chip.
      makeHistoryRow({
        identifier: 'DEMO-MANUAL',
        startedAt: _automationsPassToday.toISOString(),
        finishedAt: _automationsPassToday.toISOString(),
      }),
    ],
  }),
  issues: [
    makeIssue({ identifier: 'DEMO-1', title: 'Cron-fired issue', state: 'In Progress' }),
    makeIssue({ identifier: 'DEMO-2', title: 'Another cron run', state: 'In Progress' }),
    makeIssue({ identifier: 'DEMO-AUTO', title: 'Reviewer is commenting', state: 'In Progress' }),
    makeIssue({ identifier: 'DEMO-MANUAL', title: 'Manually dispatched', state: 'Done' }),
  ],
  logs: {
    'DEMO-AUTO': [
      makeLogEntry('text', {
        message:
          'AUTOMATION FIRED · pr-on-input\n  trigger: input_required\n  context: "Should I rebase before merge?"\n  profile: reviewer · backend: codex',
      }),
      makeLogEntry('action'),
      makeLogEntry('text'),
    ],
  },
};

// notificationsScenario — composite covering all five Notifications tab
// groups simultaneously so the e2e spec can assert each section renders.
// One review item is "session-completed" (matching currentAppSessionId on
// a worker history row) and one is "tracker-only" so the green vs muted
// pill assertion has both states.
const _notificationsSessionId = 'session-notifications';
export const notificationsScenario: Scenario = {
  snapshot: makeSnapshot({
    completionState: 'In Review',
    currentAppSessionId: _notificationsSessionId,
    inputRequired: [makeInputRequiredRow({ identifier: 'DEMO-INPUT' })],
    retrying: [makeRetryRow({ identifier: 'DEMO-RETRY' })],
    paused: ['DEMO-PAUSED'],
    pausedWithPR: { 'DEMO-PAUSED': 'https://example.com/pr/77' },
    configInvalid: makeConfigInvalidStatus(),
    history: [
      // Session-completed worker — review-source pill should be green.
      makeHistoryRow({
        identifier: 'DEMO-REVIEW-SESSION',
        kind: 'worker',
        status: 'succeeded',
        appSessionId: _notificationsSessionId,
      }),
    ],
  }),
  issues: [
    makeIssue({ identifier: 'DEMO-INPUT', title: 'Needs human input', state: 'In Progress' }),
    makeIssue({
      identifier: 'DEMO-REVIEW-SESSION',
      title: 'Just finished by worker this session',
      state: 'In Review',
    }),
    makeIssue({
      identifier: 'DEMO-REVIEW-TRACKER',
      title: 'Was already in review when daemon started',
      state: 'In Review',
    }),
    makeRetryingIssue({ identifier: 'DEMO-RETRY' }),
    makePausedIssue({ identifier: 'DEMO-PAUSED' }),
  ],
  logs: {},
};
