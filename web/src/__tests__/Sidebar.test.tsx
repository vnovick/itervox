// CORE-078 — the sidebar's Dashboard link carries the attention count, from
// the same useAttentionCount source as the inbox and the page title.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, screen, within } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import { AppShell } from '../App';
import { ThemeProvider } from '../context/ThemeContext';
import { AttentionInbox } from '../pages/Dashboard/components/AttentionInbox';
import { authedFetch } from '../auth/authedFetch';
import { render } from '../test/render';
import { makeRetryRow, makeSnapshot } from '../test/fixtures/snapshots';
import { useItervoxStore } from '../store/itervoxStore';

vi.mock('../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

beforeEach(() => {
  vi.mocked(authedFetch).mockImplementation(() =>
    Promise.resolve(
      new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } }),
    ),
  );
});

function renderShell() {
  return render(
    <ThemeProvider>
      <Routes>
        <Route element={<AppShell />}>
          <Route index element={<AttentionInbox onSelectIssue={vi.fn()} />} />
        </Route>
      </Routes>
    </ThemeProvider>,
    {
      snapshot: makeSnapshot({
        paused: ['ENG-3'],
        retrying: [makeRetryRow({ identifier: 'ENG-4' })],
        inputRequired: [
          {
            identifier: 'ENG-1',
            sessionId: 'a',
            state: 'input_required',
            context: 'q',
            queuedAt: '2026-09-01T00:00:00Z',
          },
          // Read-only resuming row: shown in the inbox, not counted.
          {
            identifier: 'ENG-2',
            sessionId: 'b',
            state: 'pending_input_resume',
            context: 'r',
            queuedAt: '2026-09-01T00:00:00Z',
          },
        ],
      }),
    },
  );
}

describe('Sidebar', () => {
  it('nav badge shows the same count as the inbox', () => {
    renderShell();
    const inboxCount = screen.getByTestId('attention-inbox-count').textContent;
    expect(inboxCount).toBe('3');
    // Both the desktop sidebar and the (inert) mobile drawer render the nav.
    const links = screen.getAllByRole('link', {
      name: 'Dashboard, 3 need attention',
      hidden: true,
    });
    expect(links.length).toBeGreaterThan(0);
    for (const link of links) {
      const badge = within(link).getByTestId('nav-badge');
      expect(badge).toHaveTextContent('3');
      // The badge is visual only; the count reaches AT through the link's
      // accessible name, never through a live region.
      expect(badge).toHaveAttribute('aria-hidden', 'true');
      expect(link.closest('[aria-live]')).toBeNull();
    }
  });

  it('hides the badge at zero and keeps the plain label', () => {
    renderShell();
    act(() => {
      useItervoxStore.setState({ snapshot: makeSnapshot() });
    });
    expect(screen.queryAllByTestId('nav-badge')).toHaveLength(0);
    expect(screen.getAllByRole('link', { name: 'Dashboard', hidden: true }).length).toBeGreaterThan(
      0,
    );
  });
});
