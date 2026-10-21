// #200 — Repo Analysis → Agent readiness: one row per doctor check from
// GET /api/v1/repo/readiness, pass / warn / fail, with the fix for anything
// that is not a pass.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { authedFetch } from '../../../auth/authedFetch';
import { render } from '../../../test/render';
import { ReadinessSection } from '../readiness/ReadinessSection';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

const readiness = {
  workflow: 'WORKFLOW.md',
  generatedAt: '2026-03-01T10:00:00Z',
  checks: [
    {
      id: 'schema_version',
      title: 'WORKFLOW.md schema 2 marker',
      status: 'pass',
      detail: 'itervox_schema_version: 2',
      fix: 'never shown for a pass',
    },
    {
      id: 'gitignore_carveouts',
      title: '.gitignore keeps .itervox/agents and .itervox/handoff committable',
      status: 'warn',
      detail: 'missing !.itervox/agents/**',
      fix: 'run `itervox init --update --workflow WORKFLOW.md`',
    },
    {
      id: 'tracker_reachable',
      title: 'Tracker reachable',
      status: 'fail',
      detail: 'the linear tracker did not answer within 8s',
      fix: 'check the network and the tracker status',
    },
  ],
};

beforeEach(() => {
  vi.mocked(authedFetch).mockReset();
  vi.mocked(authedFetch).mockImplementation(() =>
    Promise.resolve(
      new Response(JSON.stringify(readiness), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    ),
  );
});

function row(id: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-check-id="${id}"]`);
  if (!el) throw new Error(`no row ${id}`);
  return el;
}

describe('ReadinessSection (#200)', () => {
  it('renders pass, warn and fail rows with the fix for non-passing checks', async () => {
    render(<ReadinessSection />);
    const list = await screen.findByRole('list', { name: 'Readiness checks' });
    expect(within(list).getAllByRole('listitem')).toHaveLength(3);

    expect(within(row('schema_version')).getByText('pass')).toBeInTheDocument();
    expect(row('schema_version')).not.toHaveTextContent('Fix:');
    expect(row('schema_version')).not.toHaveTextContent('never shown for a pass');

    expect(within(row('gitignore_carveouts')).getByText('warn')).toBeInTheDocument();
    expect(row('gitignore_carveouts')).toHaveTextContent(
      'Fix: run `itervox init --update --workflow WORKFLOW.md`',
    );

    expect(within(row('tracker_reachable')).getByText('fail')).toBeInTheDocument();
    expect(row('tracker_reachable')).toHaveTextContent('did not answer within 8s');
    expect(row('tracker_reachable')).toHaveTextContent('Fix: check the network');

    expect(screen.getByText(/1 failing · 1 warnings/)).toBeInTheDocument();
    expect(vi.mocked(authedFetch)).toHaveBeenCalledWith(
      '/api/v1/repo/readiness',
      expect.anything(),
    );
  });

  it('re-runs the checks on demand', async () => {
    render(<ReadinessSection />);
    await screen.findByRole('list', { name: 'Readiness checks' });
    const calls = vi.mocked(authedFetch).mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: /re-run checks/i }));
    await waitFor(() => {
      expect(vi.mocked(authedFetch).mock.calls.length).toBeGreaterThan(calls);
    });
  });
});
