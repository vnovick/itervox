// Signed checks guide: the daemon's own `make test` fails after the agent's
// run, the run is held for input, and the next run's checks pass. The issue
// panel's Signed checks section lists both runs.

import type { MockApiHandle } from '../../fixtures/mockApi';
import type { Scenario } from '../../fixtures/scenarios';
import { makeInputRequiredIssue, makeIssue } from '../../../src/test/fixtures/issues';
import { makeInputRequiredRow, makeSnapshot } from '../../../src/test/fixtures/snapshots';
import { fixed, quietIssuePanels, runContext } from './common';

export const ISSUE = 'DEMO-1';
const KEY = '45ad02a488567497';
const TREE_1 = '9c1e5a7f3b2d4e6f8a0b1c2d3e4f5a6b7c8d9e0f';
const TREE_2 = '687d457a30e7b1c4d9e2f0a3b6c8d1e4f7a0b3c6';

export const HELD = [
  'Signed checks failed (verify): Itervox ran them itself in the worktree after the agent finished.',
  '- `test` exited with code 1 (`make test`)',
  '',
  '```',
  '--- FAIL: TestCartTotal (0.00s)',
  '    cart_test.go:18: Total() = 1190, want 1200',
  'FAIL\tgithub.com/example/cart/internal/cart\t0.004s',
  '```',
  '',
  'Ask the agent to fix the failure, then reply to resume; the checks run again when the run ends.',
].join('\n');

const issueBase = {
  identifier: ISSUE,
  title: 'Fix the failing cart total test',
  agentProfile: 'implementer',
  agentBackend: 'claude',
  branchName: 'itervox/demo-1',
  labels: ['bug'],
};

/** The checks failed: the run is held input-required. */
export const heldScenario: Scenario = {
  snapshot: makeSnapshot({
    capabilities: ['run_context'],
    inputRequired: [
      makeInputRequiredRow({
        identifier: ISSUE,
        sessionId: `run-${ISSUE}-1`,
        context: HELD,
        backend: 'claude',
        profile: 'implementer',
      }),
    ],
  }),
  issues: [makeInputRequiredIssue({ ...issueBase, error: HELD, lastMessage: HELD })],
  logs: {},
};

/** The second run passed and the issue is in review. */
export const passedScenario: Scenario = {
  snapshot: makeSnapshot({ capabilities: ['run_context'] }),
  issues: [makeIssue({ ...issueBase, state: 'In Review' })],
  logs: {},
};

function check(
  name: string,
  exitCode: number,
  durationMs: number,
  tree: string,
  startedAt: string,
  over: Record<string, unknown> = {},
) {
  return {
    kind: 'check',
    name,
    command: `make ${name}`,
    exitCode,
    startedAt,
    durationMs,
    treeHash: tree,
    keyId: KEY,
    verified: true,
    ...over,
  };
}
const seal = (tree: string, over: Record<string, unknown> = {}) => ({
  kind: 'seal',
  exitCode: 0,
  durationMs: 0,
  treeHash: tree,
  keyId: KEY,
  verified: true,
  ...over,
});

const RUN_1 = {
  runId: 'run-DEMO-1-1',
  profile: 'implementer',
  finalTree: TREE_1,
  records: [
    check('test', 1, 4_210, TREE_1, '2026-11-14T10:00:00Z', { outputTail: 'FAIL' }),
    seal(TREE_1),
  ],
};
const RUN_2 = {
  runId: 'run-DEMO-1-2',
  profile: 'implementer',
  finalTree: TREE_2,
  records: [
    check('test', 0, 3_980, TREE_2, '2026-11-14T10:24:00Z'),
    check('lint', 0, 850, TREE_2, '2026-11-14T10:24:04Z'),
    seal(TREE_2),
  ],
};

function mocks(api: MockApiHandle, runs: unknown[], outcome: string) {
  quietIssuePanels(api);
  fixed(api, /\/api\/v1\/issues\/[^/]+\/attestations$/, { identifier: ISSUE, keyId: KEY, runs });
  fixed(
    api,
    /\/api\/v1\/issues\/[^/]+\/context$/,
    runContext({
      identifier: ISSUE,
      run: {
        live: false,
        profile: 'implementer',
        outcome,
      },
      changedFiles: [
        { path: 'internal/cart/cart.go', status: 'M', additions: 4, deletions: 2 },
        { path: 'internal/cart/cart_test.go', status: 'M', additions: 3, deletions: 0 },
      ],
    }),
  );
}

export const heldMocks = (api: MockApiHandle) => mocks(api, [RUN_1], 'input_required');
export const passedMocks = (api: MockApiHandle) => mocks(api, [RUN_1, RUN_2], 'succeeded');

/** A record edited after signing: its signature no longer verifies. */
export const tamperedMocks = (api: MockApiHandle) =>
  mocks(
    api,
    [
      {
        ...RUN_2,
        records: [
          check('test', 0, 3_980, TREE_2, '2026-11-14T10:24:00Z', {
            verified: false,
            reason: 'attest: signature does not verify',
          }),
          check('lint', 0, 850, TREE_2, '2026-11-14T10:24:04Z'),
          seal(TREE_2),
        ],
      },
    ],
    'succeeded',
  );
