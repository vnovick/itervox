// CORE-096 — opt-in browser notifications for input_required and failures.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, renderHook, screen } from '@testing-library/react';
import { useAttentionNotifications } from '../useAttentionNotifications';
import { useItervoxStore } from '../../store/itervoxStore';
import { useNotifyPrefsStore } from '../../store/notifyPrefsStore';
import { makeRetryRow, makeSnapshot } from '../../test/fixtures/snapshots';
import type { StateSnapshot } from '../../types/schemas';

const shown: { title: string; tag?: string }[] = [];
// A constructible stand-in for the Notification API (records what was shown).
const FakeNotification = Object.assign(
  vi.fn(function (this: unknown, title: string, opts?: NotificationOptions) {
    shown.push({ title, tag: opts?.tag });
  }),
  {
    permission: 'granted' as NotificationPermission,
    requestPermission: vi.fn(() => Promise.resolve(FakeNotification.permission)),
  },
);

let visibility: DocumentVisibilityState = 'hidden';

beforeEach(() => {
  shown.length = 0;
  FakeNotification.permission = 'granted';
  FakeNotification.requestPermission.mockClear();
  vi.stubGlobal('Notification', FakeNotification);
  vi.stubGlobal('isSecureContext', true);
  visibility = 'hidden';
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility);
  localStorage.clear();
  useNotifyPrefsStore.setState({ optIn: true });
  useItervoxStore.setState({ snapshot: null });
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const input = (identifier: string, queuedAt = '2026-09-27T10:00:00Z') => ({
  identifier,
  sessionId: 's',
  state: 'input_required' as const,
  context: 'Need a decision',
  queuedAt,
});

/** Attaches pause reasons in the `pauseReasons` map shape (sibling of pausedWithPR). */
function withReasons(snapshot: StateSnapshot, reasons: Record<string, string>): StateSnapshot {
  return { ...snapshot, pauseReasons: reasons } as StateSnapshot;
}

function push(snapshot: StateSnapshot) {
  act(() => {
    useItervoxStore.setState({ snapshot });
  });
}

describe('useAttentionNotifications', () => {
  it('requests permission only from a click handler', async () => {
    FakeNotification.permission = 'default';
    useNotifyPrefsStore.setState({ optIn: false });
    function Toggle() {
      const { enable } = useAttentionNotifications();
      return (
        <button type="button" onClick={() => void enable()}>
          Enable
        </button>
      );
    }
    render(<Toggle />);
    push(makeSnapshot({ inputRequired: [input('ENG-1')] }));
    push(makeSnapshot({ inputRequired: [input('ENG-1'), input('ENG-2')] }));
    expect(FakeNotification.requestPermission).not.toHaveBeenCalled();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Enable' }));
      await Promise.resolve();
    });
    expect(FakeNotification.requestPermission).toHaveBeenCalledTimes(1);
  });

  it('no-ops when not a secure context', () => {
    vi.stubGlobal('isSecureContext', false);
    const { result } = renderHook(() => useAttentionNotifications());
    expect(result.current.supported).toBe(false);
    push(makeSnapshot());
    push(makeSnapshot({ inputRequired: [input('ENG-1')] }));
    expect(shown).toEqual([]);
  });

  it('fires one notification per input_required transition and none on repeated snapshots', () => {
    renderHook(() => useAttentionNotifications());
    push(makeSnapshot()); // baseline
    push(makeSnapshot({ inputRequired: [input('ENG-1')] }));
    push(makeSnapshot({ inputRequired: [input('ENG-1')] }));
    push(makeSnapshot({ inputRequired: [input('ENG-1')], generatedAt: 'x' }));
    expect(shown).toHaveLength(1);
    expect(shown[0].title).toMatch(/ENG-1 needs your input/);
    // A reload in the same daemon session (fresh baseline) does not re-fire.
    act(() => {
      useItervoxStore.setState({ snapshot: null });
    });
    push(makeSnapshot());
    push(makeSnapshot({ inputRequired: [input('ENG-1')] }));
    expect(shown).toHaveLength(1);
    // A new question (new queuedAt) does fire.
    push(makeSnapshot({ inputRequired: [input('ENG-1', '2026-09-27T11:00:00Z')] }));
    push(makeSnapshot());
    push(makeSnapshot({ inputRequired: [input('ENG-1', '2026-09-27T11:00:00Z')] }));
    expect(shown).toHaveLength(2);
  });

  it('fires one notification when an identifier enters paused and none when it enters retrying', () => {
    renderHook(() => useAttentionNotifications());
    push(makeSnapshot());
    push(makeSnapshot({ retrying: [makeRetryRow({ identifier: 'ENG-3' })] }));
    expect(shown).toEqual([]);
    push(withReasons(makeSnapshot({ paused: ['ENG-3'] }), { 'ENG-3': 'retries_exhausted' }));
    push(withReasons(makeSnapshot({ paused: ['ENG-3'] }), { 'ENG-3': 'retries_exhausted' }));
    expect(shown).toHaveLength(1);
    expect(shown[0].title).toMatch(/ENG-3/);
    // Paused while also retrying is not a final failure.
    push(
      withReasons(
        makeSnapshot({
          paused: ['ENG-3', 'ENG-4'],
          retrying: [makeRetryRow({ identifier: 'ENG-4' })],
        }),
        { 'ENG-3': 'retries_exhausted', 'ENG-4': 'retries_exhausted' },
      ),
    );
    expect(shown).toHaveLength(1);
  });

  // BH-M6-3 — only a failure pause notifies; an operator's own pause, cancel
  // or dismissed question never raises "run failed and will not be retried".
  it('a user pause (user_cancelled / user_dismissed_input) triggers no notification', () => {
    renderHook(() => useAttentionNotifications());
    push(makeSnapshot());
    push(withReasons(makeSnapshot({ paused: ['ENG-5'] }), { 'ENG-5': 'user_cancelled' }));
    push(
      withReasons(makeSnapshot({ paused: ['ENG-5', 'ENG-6'] }), {
        'ENG-5': 'user_cancelled',
        'ENG-6': 'user_dismissed_input',
      }),
    );
    expect(shown).toEqual([]);
  });

  // Mirrors the failure values of internal/orchestrator/state.go PauseReason*.
  it.each(['retries_exhausted', 'transition_failed'])(
    'a failure pause (%s) triggers one notification',
    (reason) => {
      renderHook(() => useAttentionNotifications());
      push(makeSnapshot());
      push(withReasons(makeSnapshot({ paused: ['ENG-7'] }), { 'ENG-7': reason }));
      expect(shown).toHaveLength(1);
    },
  );

  it.each(['max_retries', 'stalled', 'something_new'])(
    'a reason the daemon does not define as a failure (%s) triggers no notification',
    (reason) => {
      renderHook(() => useAttentionNotifications());
      push(makeSnapshot());
      push(withReasons(makeSnapshot({ paused: ['ENG-7'] }), { 'ENG-7': reason }));
      expect(shown).toEqual([]);
    },
  );

  it('a failure reason delivered as a paused row (pausedRows[].pauseReason) also notifies', () => {
    renderHook(() => useAttentionNotifications());
    push(makeSnapshot());
    push({
      ...makeSnapshot({ paused: ['ENG-8'] }),
      pausedRows: [{ identifier: 'ENG-8', pauseReason: 'retries_exhausted' }],
    } as StateSnapshot);
    expect(shown).toHaveLength(1);
  });

  it('a pause with no reason (older daemon) triggers no notification', () => {
    renderHook(() => useAttentionNotifications());
    push(makeSnapshot());
    push(makeSnapshot({ paused: ['ENG-9'] }));
    expect(shown).toEqual([]);
  });

  it('does not fire while the document is visible', () => {
    visibility = 'visible';
    renderHook(() => useAttentionNotifications());
    push(makeSnapshot());
    push(makeSnapshot({ inputRequired: [input('ENG-1')] }));
    expect(shown).toEqual([]);
  });

  it('does nothing until the operator opts in, and never prompts when denied', () => {
    useNotifyPrefsStore.setState({ optIn: false });
    const { result } = renderHook(() => useAttentionNotifications());
    push(makeSnapshot());
    push(makeSnapshot({ inputRequired: [input('ENG-1')] }));
    expect(shown).toEqual([]);
    FakeNotification.permission = 'denied';
    const { result: denied } = renderHook(() => useAttentionNotifications());
    expect(denied.current.supported).toBe(false);
    expect(result.current.enabled).toBe(false);
  });

  it('clicking a notification focuses the tab and opens the issue; disable turns it off', () => {
    const instances: { onclick: (() => void) | null }[] = [];
    FakeNotification.mockImplementation(function (this: { onclick: (() => void) | null }) {
      instances.push(this);
      shown.push({ title: 'x' });
    });
    const focus = vi.spyOn(window, 'focus').mockImplementation(() => undefined);
    const { result } = renderHook(() => useAttentionNotifications());
    push(makeSnapshot());
    push(makeSnapshot({ inputRequired: [input('ENG-7')] }));
    expect(instances).toHaveLength(1);
    act(() => {
      instances[0].onclick?.();
    });
    expect(focus).toHaveBeenCalled();
    expect(useItervoxStore.getState().selectedIdentifier).toBe('ENG-7');
    act(() => {
      result.current.disable();
    });
    expect(useNotifyPrefsStore.getState().optIn).toBe(false);
  });
});
