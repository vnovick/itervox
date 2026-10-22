import { render as rtlRender, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import type { ReactElement, ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

import { authedFetch } from '../../../../auth/authedFetch';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { useToastStore } from '../../../../store/toastStore';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import { DispatchPauseToggle } from '../DispatchPauseToggle';
import { dispatchPausedLabel } from '../dispatchPauseModel';
import { LiveOpsStrip } from '../LiveOpsStrip';

// #213 — the LiveOps strip's pause/resume-all-dispatch toggle.

const mockAuthedFetch = vi.mocked(authedFetch);

function render(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
  return rtlRender(ui, { wrapper });
}

let refresh: ReturnType<typeof vi.fn>;
beforeEach(() => {
  mockAuthedFetch.mockReset();
  useToastStore.setState({ toasts: [] });
  refresh = vi.fn().mockResolvedValue(undefined);
  useItervoxStore.setState({ refreshSnapshot: refresh });
});

function setSnapshot(overrides: Parameters<typeof makeSnapshot>[0]) {
  useItervoxStore.setState({ snapshot: makeSnapshot(overrides) });
}

describe('DispatchPauseToggle', () => {
  it('is hidden on a daemon without the dispatch_pause capability', () => {
    setSnapshot({ capabilities: ['failure_ack'] });
    render(<DispatchPauseToggle />);
    expect(screen.queryByTestId('dispatch-pause-toggle')).toBeNull();
  });

  it('renders inside the LiveOps strip', () => {
    setSnapshot({ capabilities: ['dispatch_pause'], dispatchPaused: null });
    render(<LiveOpsStrip />);
    const strip = screen.getByTestId('live-ops-strip');
    expect(within(strip).getByTestId('dispatch-pause-toggle')).toHaveTextContent('Pause dispatch');
  });

  it('pauses only after the confirm, then refreshes the snapshot', async () => {
    const user = userEvent.setup();
    setSnapshot({ capabilities: ['dispatch_pause'], dispatchPaused: null });
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ dispatchPaused: { pausedAt: '2026-10-22T10:00:00Z' } }), {
        status: 200,
      }),
    );
    render(<DispatchPauseToggle />);

    await user.click(screen.getByTestId('dispatch-pause-toggle'));
    expect(screen.getByText('Pause all dispatch?')).toBeInTheDocument();
    expect(mockAuthedFetch).not.toHaveBeenCalled();

    // Cancel sends nothing.
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(mockAuthedFetch).not.toHaveBeenCalled();

    await user.click(screen.getByTestId('dispatch-pause-toggle'));
    const dialogConfirm = screen
      .getAllByRole('button', { name: 'Pause dispatch' })
      .find((b) => b.getAttribute('data-testid') !== 'dispatch-pause-toggle');
    if (!dialogConfirm) throw new Error('no confirm button');
    await user.click(dialogConfirm);

    await waitFor(() => {
      expect(refresh).toHaveBeenCalled();
    });
    const [url, init] = mockAuthedFetch.mock.calls[0];
    expect(url).toBe('/api/v1/dispatch/pause');
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe('{}');
    await waitFor(() => {
      expect(screen.queryByText('Pause all dispatch?')).toBeNull();
    });
  });

  it('shows the pause and resumes after the confirm', async () => {
    const user = userEvent.setup();
    setSnapshot({
      capabilities: ['dispatch_pause'],
      dispatchPaused: { reason: 'deploy freeze', pausedAt: '2026-10-22T10:00:00Z' },
    });
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ dispatchPaused: null }), { status: 200 }),
    );
    render(<DispatchPauseToggle />);

    expect(screen.getByTestId('dispatch-paused-chip')).toHaveTextContent(
      'Dispatch paused · deploy freeze',
    );
    const toggle = screen.getByTestId('dispatch-pause-toggle');
    expect(toggle).toHaveTextContent('Resume dispatch');
    expect(toggle).toHaveAttribute('aria-pressed', 'true');

    await user.click(toggle);
    expect(screen.getByText('Resume dispatch?')).toBeInTheDocument();
    const dialogConfirm = screen
      .getAllByRole('button', { name: 'Resume dispatch' })
      .find((b) => b.getAttribute('data-testid') !== 'dispatch-pause-toggle');
    if (!dialogConfirm) throw new Error('no confirm button');
    await user.click(dialogConfirm);

    await waitFor(() => {
      expect(refresh).toHaveBeenCalled();
    });
    expect(mockAuthedFetch.mock.calls[0][0]).toBe('/api/v1/dispatch/resume');
  });

  it('toasts the daemon message when the pause fails', async () => {
    const user = userEvent.setup();
    setSnapshot({ capabilities: ['dispatch_pause'], dispatchPaused: null });
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ error: { code: 'busy', message: 'the orchestrator did not answer' } }),
        { status: 503 },
      ),
    );
    render(<DispatchPauseToggle />);
    await user.click(screen.getByTestId('dispatch-pause-toggle'));
    const dialogConfirm = screen
      .getAllByRole('button', { name: 'Pause dispatch' })
      .find((b) => b.getAttribute('data-testid') !== 'dispatch-pause-toggle');
    if (!dialogConfirm) throw new Error('no confirm button');
    await user.click(dialogConfirm);
    await waitFor(() => {
      expect(useToastStore.getState().toasts.map((t) => t.message)).toContain(
        'Pausing dispatch failed: the orchestrator did not answer',
      );
    });
    expect(refresh).not.toHaveBeenCalled();
  });
});

describe('dispatchPausedLabel', () => {
  it('names the reason and the until', () => {
    const until = '2026-10-22T14:30:00Z';
    const hhmm = new Date(until).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    expect(dispatchPausedLabel({ pausedAt: 'x' })).toBe('Dispatch paused');
    expect(dispatchPausedLabel({ pausedAt: 'x', reason: 'deploy', until })).toBe(
      `Dispatch paused · deploy · until ${hhmm}`,
    );
  });
});
