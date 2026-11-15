// Guardrails guide: an approval command (`vercel --prod`) waiting on the
// dashboard, and a held pipeline push on the phone through the hub.

import type { MockApiHandle } from '../../fixtures/mockApi';
import type { Scenario } from '../../fixtures/scenarios';
import { installTourData } from '../../fixtures/tourData';
import { makeIssue, makeRunningIssue } from '../../../src/test/fixtures/issues';
import {
  makeInputRequiredRow,
  makeRunningRow,
  makeSnapshot,
} from '../../../src/test/fixtures/snapshots';
import { fixed, quietIssuePanels, runContext, T } from './common';
import { heldMocks as laneHeldMocks } from './lanes';

export const DEPLOY_ISSUE = 'DEMO-4';
const ASK = 'Agent wants to run: `vercel --prod`';

export const deployScenario: Scenario = {
  snapshot: makeSnapshot({
    capabilities: ['run_context'],
    maxConcurrentAgents: 3,
    running: [
      makeRunningRow({ identifier: DEPLOY_ISSUE, backend: 'claude', lastEvent: 'tool_use:Bash' }),
      makeRunningRow({ identifier: 'DEMO-2', backend: 'codex', lastEvent: 'tool_use:Edit' }),
    ],
    inputRequired: [
      makeInputRequiredRow({
        identifier: DEPLOY_ISSUE,
        sessionId: `run-${DEPLOY_ISSUE}`,
        context: ASK,
        backend: 'claude',
        profile: 'implementer',
        queuedAt: T,
        guardrailApproval: true,
      }),
    ],
  }),
  issues: [
    makeRunningIssue({
      identifier: DEPLOY_ISSUE,
      title: 'Publish the docs preview',
      agentProfile: 'implementer',
      agentBackend: 'claude',
    }),
    makeRunningIssue({
      identifier: 'DEMO-2',
      title: 'Add pagination to the orders list',
      agentProfile: 'implementer',
      agentBackend: 'codex',
    }),
    makeIssue({ identifier: 'DEMO-3', title: 'Document the export API', state: 'Todo' }),
  ],
  logs: {},
};

export function deployMocks(api: MockApiHandle): void {
  quietIssuePanels(api);
  fixed(
    api,
    /\/api\/v1\/issues\/[^/]+\/context$/,
    runContext({
      identifier: DEPLOY_ISSUE,
      run: {
        profile: 'implementer',
        currentStep: 'The build passed; deploying the preview to production.',
        lastToolCall: { name: 'Bash', target: 'vercel --prod', at: T },
        pendingInput: ASK,
        pendingApproval: true,
      },
      changedFiles: [{ path: 'docs/preview.md', status: 'M', additions: 12, deletions: 3 }],
    }),
  );
}

/** The hub with one project waiting on a held pipeline push. */
export function hubMocks(api: MockApiHandle): void {
  // Overrides match in install order, so these go before the tour's.
  fixed(api, /\/hub\/api\/v1\/projects$/, {
    hub: {
      version: 'v0.3.3',
      pid: 42,
      url: 'http://127.0.0.1:8089/hub/',
      startedAt: T,
      headSeq: 7,
    },
    projects: [
      {
        name: 'web-app',
        workflow: '/code/web-app/WORKFLOW.md',
        projectDir: '/code/web-app',
        pid: 1,
        url: 'http://127.0.0.1:8090/',
        version: 'v0.3.3',
        startedAt: T,
        health: 'ok',
        proxied: true,
        capabilities: ['dispatch_pause', 'run_context'],
        summary: { counts: { running: 2, inputRequired: 1 } },
      },
    ],
  });
  laneHeldMocks(api);
  installTourData(api);
}
