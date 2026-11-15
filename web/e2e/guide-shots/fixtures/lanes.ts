// Lanes guide: an agent asked to fix a red build pushes a change to the CI
// workflow; the push is held as a Pipeline change, the operator rejects it
// with a reason, and the agent fixes the code instead.

import type { MockApiHandle } from '../../fixtures/mockApi';
import type { Scenario } from '../../fixtures/scenarios';
import { makeIssue, makeRunningIssue } from '../../../src/test/fixtures/issues';
import {
  makeInputRequiredRow,
  makeRunningRow,
  makeSnapshot,
} from '../../../src/test/fixtures/snapshots';
import { fixed, quietIssuePanels, runContext, T } from './common';

export const ISSUE = 'DEMO-1';
export const LANE_PATHS = ['.github/workflows/ci.yml', 'Makefile'];
const HELD =
  'Pipeline change: the agent wants to push changes to .github/workflows/ci.yml, Makefile (git push origin refs/heads/itervox/demo-1)';

const issues = [
  makeRunningIssue({
    identifier: ISSUE,
    title: 'Fix the failing cart total test',
    agentProfile: 'fixer',
    agentBackend: 'claude',
    branchName: 'itervox/demo-1',
    labels: ['bug'],
  }),
  makeRunningIssue({
    identifier: 'DEMO-2',
    title: 'Add pagination to the orders list',
    agentProfile: 'implementer',
    agentBackend: 'codex',
  }),
  makeIssue({ identifier: 'DEMO-3', title: 'Document the export API', state: 'Todo' }),
];

const running = [
  makeRunningRow({ identifier: ISSUE, backend: 'claude', lastEvent: 'tool_use:Bash' }),
  makeRunningRow({ identifier: 'DEMO-2', backend: 'codex', lastEvent: 'tool_use:Edit' }),
];

/** The push is held: a running issue with a guardrail approval waiting. */
export const heldScenario: Scenario = {
  snapshot: makeSnapshot({
    capabilities: ['run_context'],
    maxConcurrentAgents: 3,
    running,
    inputRequired: [
      makeInputRequiredRow({
        identifier: ISSUE,
        sessionId: `run-${ISSUE}`,
        context: HELD,
        backend: 'claude',
        profile: 'fixer',
        queuedAt: T,
        guardrailApproval: true,
        guardrailLanePaths: LANE_PATHS,
        guardrailLanePathsTotal: LANE_PATHS.length,
      }),
    ],
  }),
  issues,
  logs: {},
};

/** After the rejection: nothing waits, the agent is fixing the code. */
export const fixedScenario: Scenario = {
  snapshot: makeSnapshot({ capabilities: ['run_context'], maxConcurrentAgents: 3, running }),
  issues,
  logs: {},
};

export function heldMocks(api: MockApiHandle): void {
  quietIssuePanels(api);
  fixed(
    api,
    /\/api\/v1\/issues\/[^/]+\/context$/,
    runContext({
      identifier: ISSUE,
      run: {
        currentStep: 'Pushing the branch; the push changes the CI workflow and the Makefile.',
        lastToolCall: { name: 'Bash', target: 'git push origin itervox/demo-1', at: T },
        pendingInput: HELD,
        pendingApproval: true,
      },
      changedFiles: [
        { path: '.github/workflows/ci.yml', status: 'M', additions: 1, deletions: 0 },
        { path: 'Makefile', status: 'M', additions: 2, deletions: 1 },
        { path: 'internal/cart/cart_test.go', status: 'M', additions: 3, deletions: 0 },
      ],
    }),
  );
}

export function fixedMocks(api: MockApiHandle): void {
  quietIssuePanels(api);
  fixed(
    api,
    /\/api\/v1\/issues\/[^/]+\/context$/,
    runContext({
      identifier: ISSUE,
      run: {
        currentStep:
          'The pipeline push was rejected: fixing the total in cart.go and leaving the CI config as it is.',
        lastToolCall: { name: 'Edit', target: 'internal/cart/cart.go', at: T },
        toolCalls: 21,
        turnCount: 4,
        steers: [
          {
            text: 'Rejected: fix the code, not the pipeline. The CI config stays as it is.',
            at: T,
            delivery: 'delivered',
          },
        ],
      },
      changedFiles: [
        { path: 'internal/cart/cart.go', status: 'M', additions: 4, deletions: 2 },
        { path: 'internal/cart/cart_test.go', status: 'M', additions: 3, deletions: 0 },
      ],
    }),
  );
}
