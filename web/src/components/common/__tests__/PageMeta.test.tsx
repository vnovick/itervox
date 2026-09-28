// CORE-078 — the attention count is prefixed inside PageMeta (the single
// title writer, react-helmet-async), asserted through a real HelmetProvider.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { Link, MemoryRouter, Route, Routes } from 'react-router';
import userEvent from '@testing-library/user-event';
import PageMeta, { AppWrapper } from '../PageMeta';
import { withAttentionPrefix } from '../../../lib/attentionTitle';
import { authedFetch } from '../../../auth/authedFetch';
import { makeTestQueryClient } from '../../../test/queryClient';
import { resetAllStores } from '../../../test/resetStores';
import { makeSnapshot } from '../../../test/fixtures/snapshots';
import { useItervoxStore } from '../../../store/itervoxStore';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

const QUEUED_AT = '2026-09-01T00:00:00Z';

function snapshotWithAttention(n: number) {
  return makeSnapshot({
    inputRequired: Array.from({ length: n }, (_, i) => ({
      identifier: `ENG-${String(i + 1)}`,
      sessionId: `s${String(i)}`,
      state: 'input_required' as const,
      context: 'q',
      queuedAt: QUEUED_AT,
    })),
  });
}

function renderPages() {
  return render(
    <QueryClientProvider client={makeTestQueryClient()}>
      <AppWrapper>
        <MemoryRouter initialEntries={['/']}>
          <Routes>
            <Route
              index
              element={
                <>
                  <PageMeta title="Itervox | Dashboard" />
                  <Link to="/logs">logs</Link>
                </>
              }
            />
            <Route path="/logs" element={<PageMeta title="Itervox | Logs" />} />
          </Routes>
        </MemoryRouter>
      </AppWrapper>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  resetAllStores();
  vi.mocked(authedFetch).mockImplementation(() =>
    Promise.resolve(
      new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } }),
    ),
  );
  document.title = '';
});

describe('PageMeta', () => {
  it('prefixes attention count', async () => {
    useItervoxStore.setState({ snapshot: snapshotWithAttention(0) });
    renderPages();
    await waitFor(() => {
      expect(document.title).toBe('Itervox | Dashboard');
    });

    act(() => {
      useItervoxStore.setState({ snapshot: snapshotWithAttention(3) });
    });
    await waitFor(() => {
      expect(document.title).toBe('(3) Itervox | Dashboard');
    });

    // Route change keeps the prefix on the new page's title.
    await userEvent.click(document.querySelector('a[href="/logs"]') as HTMLElement);
    await waitFor(() => {
      expect(document.title).toBe('(3) Itervox | Logs');
    });

    act(() => {
      useItervoxStore.setState({ snapshot: snapshotWithAttention(1) });
    });
    await waitFor(() => {
      expect(document.title).toBe('(1) Itervox | Logs');
    });

    act(() => {
      useItervoxStore.setState({ snapshot: snapshotWithAttention(0) });
    });
    await waitFor(() => {
      expect(document.title).toBe('Itervox | Logs');
    });
  });

  it('withAttentionPrefix leaves the title alone at zero and caps huge counts', () => {
    expect(withAttentionPrefix('T', 0)).toBe('T');
    expect(withAttentionPrefix('T', 7)).toBe('(7) T');
    expect(withAttentionPrefix('T', 1000)).toBe('(99+) T');
  });
});
