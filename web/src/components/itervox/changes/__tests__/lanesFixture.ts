import type { IssueLanesResponse } from '../../../../types/schemas';
import { IssueLanesResponseSchema } from '../../../../types/schemas';

/** A Lanes view: a workflow with a finding, an auth file, code and a skipped test. */
export function lanesResponse(overrides: Partial<IssueLanesResponse> = {}): IssueLanesResponse {
  const finding = {
    kind: 'finding',
    path: '.github/workflows/ci.yml',
    line: 10,
    rule: 'ci_continue_on_error',
    message: 'failure ignored in a CI workflow',
    text: 'continue-on-error: true',
  };
  const skip = {
    kind: 'finding',
    path: 'calc_test.go',
    line: 6,
    rule: 'go_skip',
    message: 'test skip added (t.Skip)',
    text: 't.Skip("flaky")',
  };
  return IssueLanesResponseSchema.parse({
    identifier: 'ENG-1',
    base: 'origin/main',
    mergeBase: 'abc',
    enforced: true,
    lane: 'human',
    textAllowed: true,
    files: [
      {
        path: '.github/workflows/ci.yml',
        status: 'modified',
        additions: 1,
        deletions: 0,
        lane: 'human',
        rule: '.github/**',
        findings: [finding],
      },
      {
        path: 'auth/login.go',
        status: 'added',
        additions: 3,
        deletions: 0,
        lane: 'second_opinion',
        rule: 'auth/**',
        findings: [],
      },
      {
        path: 'main.go',
        status: 'modified',
        additions: 2,
        deletions: 1,
        lane: 'auto',
        findings: [],
      },
      {
        path: 'calc_test.go',
        status: 'modified',
        additions: 1,
        deletions: 0,
        lane: 'auto',
        findings: [skip],
      },
    ],
    reasons: [
      finding,
      skip,
      { kind: 'lane_rule', path: '.github/workflows/ci.yml', lane: 'human', rule: '.github/**' },
    ],
    reasonsTotal: 3,
    ...overrides,
  });
}

export const codeFirstSplit = {
  order: 'code_first',
  reason: 'the code passed its checks alone',
  codeFiles: ['auth/login.go', 'main.go', 'calc_test.go'],
  codeFilesTotal: 3,
  pipelineFiles: ['.github/workflows/ci.yml'],
  pipelineFilesTotal: 1,
  codeLane: 'second_opinion',
  lowerPr: 7,
  lowerPrUrl: 'https://example.test/pr/7',
  upperPrUrl: 'https://example.test/pr/8',
};
