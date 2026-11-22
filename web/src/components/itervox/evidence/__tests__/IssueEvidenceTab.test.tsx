// Evidence report tab — renders the daemon's report (the Go-emitted
// fixture, so the shape is the real one): what ran, what was checked, who
// approved, the PRs; empty and unsupported states.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import full from '../../../../types/fixtures/EvidenceReport.json';
import empty from '../../../../types/fixtures/EvidenceReportEmpty.json';
import IssueEvidenceTab from '../IssueEvidenceTab';
import { routeFetch } from '../../changes/__tests__/fetchMock';
import { authedFetch } from '../../../../auth/authedFetch';
import { approvalSubject, approver, decisionPill, evidenceTime } from '../evidenceFormat';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

function serve(body: unknown, status = 200) {
  routeFetch(vi.mocked(authedFetch), { '/api/v1/issues/ENG-1/evidence': { status, body } });
}

describe('IssueEvidenceTab', () => {
  beforeEach(() => {
    vi.mocked(authedFetch).mockReset();
  });

  it('renders runs, checks, approvals and PRs from the report', async () => {
    serve(full);
    render(<IssueEvidenceTab identifier="ENG-1" live={false} />, { wrapper });
    expect(await screen.findByTestId('issue-evidence')).toBeInTheDocument();

    const runs = screen.getAllByTestId('evidence-run');
    expect(runs).toHaveLength(2);
    const first = within(runs[0]);
    expect(first.getByText('coder')).toBeInTheDocument();
    expect(first.getByText('succeeded')).toBeInTheDocument();
    expect(first.getByText('run-1')).toBeInTheDocument();
    expect(first.getByText('claude · claude-sonnet-4-5')).toBeInTheDocument();
    expect(first.getByText('aaaaaaaaaaaa')).toBeInTheDocument();
    expect(first.getByText('bugfix')).toBeInTheDocument();
    expect(first.getByText('missing ci', { exact: false })).toBeInTheDocument();
    expect(first.getByText('1 steering message')).toBeInTheDocument();
    expect(first.getByText(/check the tests/)).toBeInTheDocument();
    expect(within(runs[1]).getByText('retry 1')).toBeInTheDocument();
    expect(screen.getByText(/Retry 2 due/)).toBeInTheDocument();

    expect(within(screen.getByTestId('evidence-signed')).getByText('verified')).toBeInTheDocument();
    expect(screen.getByTestId('evidence-verdict')).toHaveTextContent(
      'reviewer approve codex · 1 reasons · 2 line comments',
    );
    expect(screen.getByTestId('evidence-quorum')).toHaveTextContent('passed');
    expect(screen.getByTestId('evidence-guardrail-block')).toHaveTextContent('force_push');
    expect(screen.getByTestId('evidence-push-lane')).toHaveTextContent('3 files (1 kept)');

    const approvals = screen.getAllByTestId('evidence-approval');
    expect(approvals).toHaveLength(2);
    expect(approvals[0]).toHaveTextContent('Held push');
    expect(approvals[0]).toHaveTextContent('approved');
    expect(approvals[0]).toHaveTextContent('ops · dashboard:tok_1');
    expect(approvals[1]).toHaveTextContent('pending');
    expect(approvals[1]).toHaveTextContent('4 files');

    expect(screen.getByTestId('evidence-pr')).toHaveTextContent('merged');
    expect(screen.getByText(/Shown as a admin sees it/)).toBeInTheDocument();
  });

  it('shows empty states for an issue with nothing recorded', async () => {
    serve(empty);
    render(<IssueEvidenceTab identifier="ENG-1" live />, { wrapper });
    expect(await screen.findByText('No runs recorded for this issue.')).toBeInTheDocument();
    expect(screen.getByText('No checks recorded for this issue.')).toBeInTheDocument();
    expect(screen.getByText('No approvals or answers recorded.')).toBeInTheDocument();
    expect(screen.getByText('No pull request recorded.')).toBeInTheDocument();
  });

  it('says so when the daemon does not serve the report, and on an error', async () => {
    serve({ error: { code: 'not_supported', message: 'x' } }, 503);
    const { unmount } = render(<IssueEvidenceTab identifier="ENG-1" live={false} />, { wrapper });
    expect(
      await screen.findByText('This daemon does not serve evidence reports.'),
    ).toBeInTheDocument();
    unmount();
    serve({ error: { code: 'evidence_unreadable', message: 'x' } }, 500);
    render(<IssueEvidenceTab identifier="ENG-1" live={false} />, { wrapper });
    expect(await screen.findByText('The evidence report could not be read.')).toBeInTheDocument();
  });
});

describe('evidenceFormat', () => {
  it('formats times, subjects, decisions and approvers', () => {
    expect(evidenceTime('2026-11-20T09:05:00Z')).toBe('2026-11-20 09:05');
    expect(evidenceTime('nope')).toBe('—');
    expect(evidenceTime()).toBe('—');
    const base = { at: '', decision: 'approved' };
    expect(approvalSubject({ ...base, kind: 'guardrail', push: true })).toBe('Held push');
    expect(approvalSubject({ ...base, kind: 'guardrail' })).toBe('Held command');
    expect(approvalSubject({ ...base, kind: 'input' })).toBe('Agent question');
    expect(decisionPill('answered')).toBe('pill-ok');
    expect(decisionPill('dismissed')).toBe('pill-danger');
    expect(decisionPill('pending')).toBe('pill-warn');
    expect(approver({ ...base, kind: 'input' })).toBe('');
    expect(approver({ ...base, kind: 'input', actor: 'tracker:Dana' })).toBe('tracker:Dana');
  });
});
