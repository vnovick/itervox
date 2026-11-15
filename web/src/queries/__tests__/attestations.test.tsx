// Signed checks (0.3.3) — useIssueAttestations reads the issue's records
// through authedFetch, parses them with the Zod schema, answers null when
// the daemon does not serve signed checks and surfaces other errors.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { authedFetch } from '../../auth/authedFetch';
import { ATTESTATIONS_KEY, useIssueAttestations } from '../attestations';

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

describe('useIssueAttestations', () => {
  it('fetches the encoded issue path and parses records with defaults', async () => {
    mockFetch.mockResolvedValue(
      json({
        identifier: 'ENG 1',
        keyId: 'abc',
        runs: [
          {
            runId: 'r1',
            records: [{ kind: 'check', name: 't', exitCode: 0, verified: true }],
          },
          { runId: 'r2', records: null },
        ],
      }),
    );
    const { result } = renderHook(() => useIssueAttestations('ENG 1', false), {
      wrapper: wrapper(),
    });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/issues/ENG%201/attestations',
      expect.anything(),
    );
    expect(result.current.data?.keyId).toBe('abc');
    expect(result.current.data?.runs[0]?.records[0]?.durationMs).toBe(0);
    expect(result.current.data?.runs[1]?.records).toEqual([]);
  });

  it('answers null on 503 not_supported', async () => {
    mockFetch.mockResolvedValue(json({ error: { code: 'not_supported', message: 'no' } }, 503));
    const { result } = renderHook(() => useIssueAttestations('ENG-1', false), {
      wrapper: wrapper(),
    });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(result.current.data).toBeNull();
  });

  it('surfaces a read error', async () => {
    mockFetch.mockResolvedValue(
      json({ error: { code: 'attestations_unreadable', message: 'could not be read' } }, 500),
    );
    const { result } = renderHook(() => useIssueAttestations('ENG-1', false), {
      wrapper: wrapper(),
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(String(result.current.error)).toContain('could not be read');
  });

  it('does not fetch without an identifier', () => {
    renderHook(() => useIssueAttestations('', true), { wrapper: wrapper() });
    expect(mockFetch).not.toHaveBeenCalled();
    expect(ATTESTATIONS_KEY('ENG-1')).toEqual(['attestations', 'ENG-1']);
  });
});
