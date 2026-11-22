// Evidence report — useIssueEvidence reads the issue's report through
// authedFetch, parses it with the Zod schema (null lists become []), answers
// null when the daemon does not serve it and surfaces other errors.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { authedFetch } from '../../auth/authedFetch';
import { EvidenceReportSchema } from '../../types/schemas';
import { EVIDENCE_KEY, useIssueEvidence } from '../evidence';

vi.mock('../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

beforeEach(() => {
  mockFetch.mockReset();
});

describe('useIssueEvidence', () => {
  it('fetches the encoded issue path and parses the report with defaults', async () => {
    mockFetch.mockResolvedValue(
      json({
        identifier: 'ENG 1',
        generatedAt: '2026-11-20T10:00:00Z',
        scope: 'viewer',
        runs: [
          { kind: 'worker', status: 'failed', startedAt: '2026-11-20T09:00:00Z', ruleIds: null },
        ],
        approvals: null,
        signedChecks: null,
        pullRequests: [],
        guardrailBlocks: [],
      }),
    );
    const { result } = renderHook(() => useIssueEvidence('ENG 1', false), { wrapper: wrapper() });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/issues/ENG%201/evidence', expect.anything());
    const rep = result.current.data;
    expect(rep?.runs[0]?.ruleIds).toEqual([]);
    expect(rep?.runs[0]?.steers).toEqual([]);
    expect(rep?.runs[0]?.elapsedMs).toBe(0);
    expect(rep?.approvals).toEqual([]);
    expect(rep?.signedChecks).toEqual([]);
    expect(rep?.reviews.verdicts).toEqual([]);
  });

  it('answers null when the daemon does not serve the report', async () => {
    mockFetch.mockResolvedValue(
      json({ error: { code: 'not_supported', message: 'not available' } }, 503),
    );
    const { result } = renderHook(() => useIssueEvidence('ENG-1', false), { wrapper: wrapper() });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(result.current.data).toBeNull();
  });

  it('surfaces other errors', async () => {
    mockFetch.mockResolvedValue(
      json({ error: { code: 'evidence_unreadable', message: 'boom' } }, 500),
    );
    const { result } = renderHook(() => useIssueEvidence('ENG-1', false), { wrapper: wrapper() });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
  });

  it('does not fetch without an identifier and keys by issue', () => {
    renderHook(() => useIssueEvidence('', true), { wrapper: wrapper() });
    expect(mockFetch).not.toHaveBeenCalled();
    expect(EVIDENCE_KEY('ENG-1')).toEqual(['evidence', 'ENG-1']);
  });
});

describe('EvidenceReportSchema', () => {
  it('rejects a report without its identity', () => {
    expect(EvidenceReportSchema.safeParse({ runs: [] }).success).toBe(false);
  });
});
