// Signed checks (0.3.3) — the issue panel lists every run's signed check
// records: name, ok/failed with the exit code, duration, short tree hash and
// whether the signature and the run's seal verify. Read-only.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import { authedFetch } from '../../../auth/authedFetch';
import { render } from '../../../test/render';
import { IssueSignedChecks } from '../IssueSignedChecks';
import { checkDuration, checkOutcome, shortTree } from '../../../utils/signedChecks';
import { routeFetch } from '../changes/__tests__/fetchMock';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

const URL = '/api/v1/issues/ENG-1/attestations';
const TREE = 'aaaaaaa1111111111111111111111111111111111';

const attestations = {
  identifier: 'ENG-1',
  keyId: '0123456789abcdef',
  runs: [
    {
      runId: 'run-1',
      profile: 'implementer',
      finalTree: TREE,
      records: [
        {
          kind: 'check',
          name: 'test',
          command: 'make test',
          exitCode: 0,
          startedAt: '2026-11-14T10:00:00Z',
          durationMs: 65_000,
          treeHash: TREE,
          keyId: '0123456789abcdef',
          verified: true,
        },
        {
          kind: 'check',
          name: 'lint',
          exitCode: 2,
          startedAt: '2026-11-14T10:01:05Z',
          durationMs: 850,
          treeHash: 'bbbbbbb2222',
          verified: false,
          reason: 'the check ran on another tree than the run sealed',
        },
        { kind: 'seal', exitCode: 0, durationMs: 0, treeHash: TREE, verified: true },
      ],
    },
    {
      runId: 'run-2',
      profile: 'reviewer',
      records: [
        {
          kind: 'check',
          name: 'e2e',
          exitCode: -1,
          timedOut: true,
          durationMs: 600_000,
          verified: false,
          reason: 'bad signature',
        },
      ],
    },
  ],
};

beforeEach(() => {
  mockFetch.mockReset();
});

describe('IssueSignedChecks', () => {
  it('lists each run with its checks, outcome, duration, tree and verification', async () => {
    routeFetch(mockFetch, { [URL]: { body: attestations } });
    render(<IssueSignedChecks identifier="ENG-1" live={false} />);
    const section = await screen.findByTestId('signed-checks');
    expect(section).toHaveTextContent('key 0123456789abcdef');
    const runs = within(section).getAllByTestId('signed-checks-run');
    expect(runs).toHaveLength(2);

    expect(runs[0]).toHaveTextContent('implementer · 2026-11-14 10:00');
    expect(runs[0]).toHaveTextContent('sealed aaaaaaa');
    const checks = within(runs[0]).getAllByTestId('signed-check');
    expect(checks).toHaveLength(2); // the seal is the run's status, not a row
    expect(checks[0]).toHaveTextContent('test');
    expect(checks[0]).toHaveTextContent('ok · exit 0');
    expect(checks[0]).toHaveTextContent('1m 05s');
    expect(checks[0]).toHaveTextContent('tree aaaaaaa');
    expect(within(checks[0]).getByText('verified')).toBeInTheDocument();
    expect(checks[1]).toHaveTextContent('lint');
    expect(checks[1]).toHaveTextContent('failed · exit 2');
    expect(checks[1]).toHaveTextContent('850ms');
    expect(checks[1]).toHaveTextContent('tree bbbbbbb');
    expect(checks[1]).toHaveTextContent('not verified');
    expect(checks[1]).toHaveTextContent('the check ran on another tree than the run sealed');

    expect(runs[1]).toHaveTextContent('reviewer');
    expect(runs[1]).toHaveTextContent('not sealed');
    expect(runs[1]).toHaveTextContent('timed out');
    expect(runs[1]).toHaveTextContent('tree —');
    expect(runs[1]).toHaveTextContent('bad signature');
  });

  it('shows an empty state when no run has signed checks', async () => {
    routeFetch(mockFetch, { [URL]: { body: { identifier: 'ENG-1', runs: null } } });
    render(<IssueSignedChecks identifier="ENG-1" live />);
    const section = await screen.findByTestId('signed-checks');
    expect(section).toHaveTextContent('No signed checks have run for this issue.');
    expect(within(section).queryByTestId('signed-checks-run')).not.toBeInTheDocument();
  });

  it('renders nothing when the daemon does not serve signed checks', async () => {
    routeFetch(mockFetch, {
      [URL]: {
        status: 503,
        body: { error: { code: 'not_supported', message: 'signed checks are not available' } },
      },
    });
    render(<IssueSignedChecks identifier="ENG-1" live={false} />);
    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(URL, expect.anything());
    });
    expect(screen.queryByTestId('signed-checks')).not.toBeInTheDocument();
  });
});

describe('IssueSignedChecks helpers', () => {
  it('shortens trees, formats durations and classifies outcomes', () => {
    expect(shortTree('0123456789')).toBe('0123456');
    expect(shortTree(undefined)).toBe('—');
    expect(checkDuration(0)).toBe('0ms');
    expect(checkDuration(1_500)).toBe('1s');
    const base = { kind: 'check', exitCode: 0, durationMs: 0, verified: true };
    expect(checkOutcome(base)).toEqual({ ok: true, label: 'ok · exit 0' });
    expect(checkOutcome({ ...base, exitCode: 1 })).toEqual({ ok: false, label: 'failed · exit 1' });
    expect(checkOutcome({ ...base, exitCode: -1, error: 'could not start' })).toEqual({
      ok: false,
      label: 'failed',
    });
  });
});
