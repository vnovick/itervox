// CORE-077 — the attention inbox pinned at the top of the Dashboard. Real
// mutation hooks; only the transport (authedFetch) is mocked, so the tests
// prove the inbox reuses the existing provide-input / resume / terminate
// routes and their 409 inline-input semantics.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { authedFetch } from '../../../../auth/authedFetch';
import { AttentionInbox } from '../AttentionInbox';
import { render } from '../../../../test/render';
import { axeViolations } from '../../../../test/axe';
import { makeRetryRow, makeSnapshot } from '../../../../test/fixtures/snapshots';
import { makeIssue } from '../../../../test/fixtures/issues';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { useToastStore } from '../../../../store/toastStore';
import type { StateSnapshot, TrackerIssue } from '../../../../types/schemas';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

const QUEUED_AT = '2026-09-01T00:00:00Z';

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  return input instanceof URL ? input.href : input.url;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

type Route = (url: string, init?: RequestInit) => Response | undefined;

function routeFetch(issues: TrackerIssue[], extra: Route = () => undefined) {
  mockFetch.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = urlOf(input);
    const hit = extra(url, init);
    if (hit) return Promise.resolve(hit);
    if (url === '/api/v1/issues') return Promise.resolve(json(issues));
    return Promise.resolve(json({}));
  });
}

function inputRow(identifier: string, state: 'input_required' | 'pending_input_resume') {
  return {
    identifier,
    sessionId: `s-${identifier}`,
    state,
    context: `ctx ${identifier}`,
    queuedAt: QUEUED_AT,
  };
}

function snapWithInput(extra: Partial<StateSnapshot> = {}): StateSnapshot {
  return {
    ...makeSnapshot({ inputRequired: [inputRow('ENG-1', 'input_required')] }),
    ...extra,
  };
}

function calls(path: string) {
  return mockFetch.mock.calls.filter(([u]) => urlOf(u) === path);
}

beforeEach(() => {
  mockFetch.mockReset();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('AttentionInbox', () => {
  it('hidden when the queue is empty', () => {
    routeFetch([]);
    render(<AttentionInbox onSelectIssue={vi.fn()} />, { snapshot: makeSnapshot() });
    // No list and no actions — only a one-line "all caught up" state.
    expect(screen.queryByRole('list')).not.toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
    expect(screen.getByTestId('attention-inbox-empty')).toHaveTextContent(/nothing needs you/i);
  });

  it('inline reply posts provide-input', async () => {
    routeFetch([makeIssue({ identifier: 'ENG-1' })], (url) =>
      url === '/api/v1/issues/ENG-1/provide-input' ? json({ ok: true }) : undefined,
    );
    render(<AttentionInbox onSelectIssue={vi.fn()} />, { snapshot: snapWithInput() });
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-1' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-1' }), {
      target: { value: 'use main' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Send reply to ENG-1' }));
    await waitFor(() => {
      expect(calls('/api/v1/issues/ENG-1/provide-input')).toHaveLength(1);
    });
    const init = calls('/api/v1/issues/ENG-1/provide-input')[0]?.[1];
    expect(init?.method).toBe('POST');
    expect(JSON.parse(init?.body as string)).toEqual({ message: 'use main' });
    // No optimistic removal: the row stays until the next snapshot drops it.
    expect(screen.getByTestId('attention-item-needs_input-ENG-1')).toBeInTheDocument();
    act(() => {
      useItervoxStore.setState({ snapshot: makeSnapshot() });
    });
    expect(screen.queryByTestId('attention-item-needs_input-ENG-1')).not.toBeInTheDocument();
  });

  it('Resume and Discard call the existing hooks', async () => {
    routeFetch([makeIssue({ identifier: 'ENG-3' })]);
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: makeSnapshot({ paused: ['ENG-3'] }),
    });
    fireEvent.click(screen.getByRole('button', { name: 'Resume ENG-3' }));
    await waitFor(() => {
      expect(calls('/api/v1/issues/ENG-3/resume')).toHaveLength(1);
    });
    // Discard confirms first (M5-B1): nothing is sent on the first click.
    act(() => {
      useItervoxStore.setState({ snapshot: makeSnapshot({ paused: ['ENG-3'] }) });
    });
    fireEvent.click(screen.getByRole('button', { name: 'Discard ENG-3' }));
    expect(calls('/api/v1/issues/ENG-3/terminate')).toHaveLength(0);
    fireEvent.click(screen.getByRole('button', { name: 'Yes, discard' }));
    await waitFor(() => {
      expect(calls('/api/v1/issues/ENG-3/terminate')).toHaveLength(1);
    });
  });

  it('failed reply shows an error toast and keeps the draft', async () => {
    routeFetch([], (url) =>
      url === '/api/v1/issues/ENG-1/provide-input'
        ? json({ error: { code: 'boom', message: 'daemon said no' } }, 500)
        : undefined,
    );
    render(<AttentionInbox onSelectIssue={vi.fn()} />, { snapshot: snapWithInput() });
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-1' }));
    const box = screen.getByRole('textbox', { name: 'Reply to the agent on ENG-1' });
    fireEvent.change(box, { target: { value: 'keep me' } });
    fireEvent.click(screen.getByRole('button', { name: 'Send reply to ENG-1' }));
    await waitFor(() => {
      expect(useToastStore.getState().toasts.map((t) => t.message)).toContain('daemon said no');
    });
    expect(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-1' })).toHaveValue(
      'keep me',
    );
  });

  it('an issue in two groups renders once per group with distinct actions', () => {
    routeFetch([makeIssue({ identifier: 'ENG-3' })]);
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: makeSnapshot({
        paused: ['ENG-3'],
        recentFailures: [
          {
            kind: 'worker_stalled',
            identifier: 'ENG-3',
            message: 'no output for 10m',
            occurredAt: QUEUED_AT,
            recordedAt: QUEUED_AT,
            count: 1,
          },
        ],
      }),
    });
    const paused = screen.getByTestId('attention-item-paused-ENG-3');
    const failed = screen.getByTestId('attention-item-failed-ENG-3');
    expect(within(paused).getByRole('button', { name: 'Resume ENG-3' })).toBeInTheDocument();
    expect(within(failed).queryByRole('button', { name: 'Resume ENG-3' })).not.toBeInTheDocument();
    expect(within(failed).getByRole('button', { name: 'Open ENG-3' })).toBeInTheDocument();
    expect(within(failed).getByText(/stalled/i)).toBeInTheDocument();
  });

  it('inline reply is replaced by the tracker notice when inlineInput is on', () => {
    routeFetch([]);
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: snapWithInput({ inlineInput: true }),
    });
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-1' }));
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
    expect(screen.getByTestId('input-required-inline-notice')).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: 'Dismiss input request for ENG-1' }),
    ).toBeInTheDocument();
  });

  it('409 inline_input_enabled refreshes the snapshot and shows the inline-input toast', async () => {
    routeFetch([], (url) => {
      if (url === '/api/v1/issues/ENG-1/provide-input') {
        return json({ error: { code: 'inline_input_enabled', message: 'inline input on' } }, 409);
      }
      if (url === '/api/v1/state') return json(snapWithInput({ inlineInput: true }));
      return undefined;
    });
    render(<AttentionInbox onSelectIssue={vi.fn()} />, { snapshot: snapWithInput() });
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-1' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-1' }), {
      target: { value: 'hi' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Send reply to ENG-1' }));
    await waitFor(() => {
      expect(calls('/api/v1/state')).toHaveLength(1);
    });
    await waitFor(() => {
      expect(
        useToastStore
          .getState()
          .toasts.map((t) => t.message)
          .join('\n'),
      ).toMatch(/Inline input is on/);
    });
    await waitFor(() => {
      expect(screen.getByTestId('input-required-inline-notice')).toBeInTheDocument();
    });
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
  });

  it('pending_input_resume rows render read-only (no Reply, meta reply pending)', () => {
    routeFetch([]);
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: makeSnapshot({ inputRequired: [inputRow('ENG-8', 'pending_input_resume')] }),
    });
    const row = screen.getByTestId('attention-item-resuming-ENG-8');
    expect(within(row).getByText(/reply pending/i)).toBeInTheDocument();
    expect(within(row).queryByRole('button', { name: /Reply/ })).not.toBeInTheDocument();
    expect(within(row).queryByRole('textbox')).not.toBeInTheDocument();
  });

  it('drafts are kept per issue when switching rows', () => {
    routeFetch([]);
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: makeSnapshot({
        inputRequired: [inputRow('ENG-1', 'input_required'), inputRow('ENG-2', 'input_required')],
      }),
    });
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-1' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-1' }), {
      target: { value: 'draft one' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-2' }));
    expect(
      screen.queryByRole('textbox', { name: 'Reply to the agent on ENG-1' }),
    ).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-2' }), {
      target: { value: 'draft two' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-1' }));
    expect(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-1' })).toHaveValue(
      'draft one',
    );
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-2' }));
    expect(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-2' })).toHaveValue(
      'draft two',
    );
  });

  it('lists failed, stalled, backend_limited and degraded outbox items', () => {
    routeFetch([]);
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: makeSnapshot({
        recentFailures: [
          {
            kind: 'worker_failed',
            identifier: 'ENG-10',
            message: 'exit 1',
            occurredAt: QUEUED_AT,
            recordedAt: QUEUED_AT,
            count: 1,
          },
          {
            kind: 'worker_stalled',
            identifier: 'ENG-11',
            message: 'stall',
            occurredAt: QUEUED_AT,
            recordedAt: QUEUED_AT,
            count: 2,
          },
          // Not an issue failure: stays in the FailuresPanel only.
          {
            kind: 'tracker_poll',
            message: 'timeout',
            occurredAt: QUEUED_AT,
            recordedAt: QUEUED_AT,
            count: 1,
          },
        ],
        backendHealth: [
          { backend: 'codex', status: 'limited', limitedUntil: '2026-09-01T05:00:00Z' },
          { backend: 'claude', status: 'healthy', limitedUntil: null },
        ],
        outboxEntries: [
          {
            id: 'ob-1',
            kind: 'comment',
            identifier: 'ENG-12',
            attempts: 7,
            degraded: true,
            lastError: '502',
            enqueuedAt: QUEUED_AT,
            nextAttemptAt: QUEUED_AT,
          },
          {
            id: 'ob-2',
            kind: 'comment',
            identifier: 'ENG-13',
            attempts: 1,
            enqueuedAt: QUEUED_AT,
            nextAttemptAt: QUEUED_AT,
          },
        ],
      }),
    });
    expect(screen.getByTestId('attention-item-failed-ENG-10')).toHaveTextContent(/failed/i);
    expect(screen.getByTestId('attention-item-failed-ENG-11')).toHaveTextContent(/stalled/i);
    expect(screen.getByTestId('attention-item-backend_limited-codex')).toHaveTextContent(/codex/i);
    expect(screen.queryByTestId('attention-item-backend_limited-claude')).not.toBeInTheDocument();
    expect(screen.getByTestId('attention-item-outbox-ob-1')).toHaveTextContent('ENG-12');
    expect(screen.queryByTestId('attention-item-outbox-ob-2')).not.toBeInTheDocument();
  });

  it('is an accessible list with labelled actions and Open selects the issue', async () => {
    routeFetch([]);
    const onSelect = vi.fn();
    const { container } = render(<AttentionInbox onSelectIssue={onSelect} />, {
      snapshot: {
        ...snapWithInput(),
        retrying: [makeRetryRow({ identifier: 'ENG-4' })],
        paused: ['ENG-3'],
      },
    });
    const region = screen.getByRole('region', { name: /needs attention/i });
    expect(within(region).getAllByRole('list').length).toBeGreaterThan(0);
    // No live region: the inbox must not re-announce on every snapshot push.
    expect(region.querySelector('[aria-live]')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Open ENG-4' }));
    expect(onSelect).toHaveBeenCalledWith('ENG-4');
    expect(await axeViolations(container)).toEqual([]);
  });

  // M5-close — focus used to fall to <body> after Send (the button disables
  // while sending and again when the draft clears) and when the replied row
  // leaves the inbox.
  it('keeps focus in the reply box after Send and moves it to the heading when the row leaves', async () => {
    routeFetch([], (url) =>
      url === '/api/v1/issues/ENG-1/provide-input' ? json({ ok: true }) : undefined,
    );
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: {
        ...snapWithInput(),
        paused: ['ENG-3'],
      },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-1' }));
    const box = screen.getByRole('textbox', { name: 'Reply to the agent on ENG-1' });
    fireEvent.change(box, { target: { value: 'go' } });
    const send = screen.getByRole('button', { name: 'Send reply to ENG-1' });
    act(() => {
      send.focus();
    });
    fireEvent.click(send);
    expect(box).toHaveFocus();
    await waitFor(() => {
      expect(calls('/api/v1/issues/ENG-1/provide-input')).toHaveLength(1);
    });
    await waitFor(() => {
      expect(box).toHaveValue('');
    });
    expect(box).toHaveFocus();
    act(() => {
      useItervoxStore.setState({ snapshot: makeSnapshot({ paused: ['ENG-3'] }) });
    });
    expect(screen.getByRole('heading', { name: /needs attention/i })).toHaveFocus();
  });

  // M5-close BH-M5-3 — rows share one mutation per hook. Per-call mutate
  // callbacks fire only for the latest call, and isPending/variables describe
  // only the latest call, so row A re-enabled while in flight and kept its
  // draft after success.
  it('two concurrent replies each stay disabled while in flight and each clear their own draft', async () => {
    const pending = new Map<string, (r: Response) => void>();
    mockFetch.mockImplementation((input: RequestInfo | URL) => {
      const url = urlOf(input);
      const m = /^\/api\/v1\/issues\/([^/]+)\/provide-input$/.exec(url);
      if (m) {
        return new Promise<Response>((resolve) => {
          pending.set(m[1], resolve);
        });
      }
      if (url === '/api/v1/issues') return Promise.resolve(json([]));
      return Promise.resolve(json({}));
    });
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: makeSnapshot({
        inputRequired: [inputRow('ENG-1', 'input_required'), inputRow('ENG-2', 'input_required')],
      }),
    });
    const sendFor = async (id: string, text: string) => {
      fireEvent.click(screen.getByRole('button', { name: `Reply to ${id}` }));
      fireEvent.change(screen.getByRole('textbox', { name: `Reply to the agent on ${id}` }), {
        target: { value: text },
      });
      fireEvent.click(screen.getByRole('button', { name: `Send reply to ${id}` }));
      await waitFor(() => {
        expect(pending.has(id)).toBe(true);
      });
    };
    await sendFor('ENG-1', 'one');
    await sendFor('ENG-2', 'two');
    // Back on ENG-1 while both are in flight: still disabled (no double send).
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-1' }));
    expect(screen.getByRole('button', { name: 'Send reply to ENG-1' })).toBeDisabled();
    expect(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-1' })).toHaveValue('one');

    await act(async () => {
      pending.get('ENG-1')?.(json({ ok: true }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-1' })).toHaveValue('');
    });
    await act(async () => {
      pending.get('ENG-2')?.(json({ ok: true }));
      await Promise.resolve();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Reply to ENG-2' }));
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-2' })).toHaveValue('');
    });
    expect(calls('/api/v1/issues/ENG-1/provide-input')).toHaveLength(1);
    expect(calls('/api/v1/issues/ENG-2/provide-input')).toHaveLength(1);
  });

  // Resume removes the row optimistically, so the shared-state bug shows on
  // Discard (no optimistic removal): the first row re-enabled once the second
  // call became the mutation's latest.
  it('Discard on two paused rows keeps both disabled while both are in flight', async () => {
    const pending: ((r: Response) => void)[] = [];
    mockFetch.mockImplementation((input: RequestInfo | URL) => {
      const url = urlOf(input);
      if (url.endsWith('/terminate')) {
        return new Promise<Response>((resolve) => {
          pending.push(resolve);
        });
      }
      if (url === '/api/v1/issues') return Promise.resolve(json([]));
      return Promise.resolve(json({}));
    });
    render(<AttentionInbox onSelectIssue={vi.fn()} />, {
      snapshot: makeSnapshot({ paused: ['ENG-3', 'ENG-4'] }),
    });
    for (const id of ['ENG-3', 'ENG-4']) {
      const row = screen.getByTestId(`attention-item-paused-${id}`);
      fireEvent.click(within(row).getByRole('button', { name: `Discard ${id}` }));
      fireEvent.click(within(row).getByRole('button', { name: 'Yes, discard' }));
    }
    await waitFor(() => {
      expect(pending).toHaveLength(2);
    });
    for (const id of ['ENG-3', 'ENG-4']) {
      const row = screen.getByTestId(`attention-item-paused-${id}`);
      expect(within(row).getByRole('button', { name: 'Discarding…' })).toBeDisabled();
    }
    await act(async () => {
      for (const resolve of pending) resolve(json({ ok: true }));
      await Promise.resolve();
    });
  });
});
