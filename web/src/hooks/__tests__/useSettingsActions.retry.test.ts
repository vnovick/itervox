import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook } from '@testing-library/react';
import { useSettingsActions, settingsFetchTyped } from '../useSettingsActions';
import { useItervoxStore } from '../../store/itervoxStore';
import { useToastStore } from '../../store/toastStore';

// CORE-170 — a settings save that races a WORKFLOW.md reload gets
// 503 settings_reloading + Retry-After (nothing was written). The dashboard
// retries it once, honouring Retry-After, then refreshes the snapshot — the
// operator sees no spurious error toast.

type FetchFn = (input: string, init?: RequestInit) => Promise<Response>;
let fetchMock: ReturnType<typeof vi.fn<FetchFn>>;
let refreshSpy: ReturnType<typeof vi.fn>;

function reloading(retryAfter = '1'): Response {
  return new Response(
    JSON.stringify({ error: { code: 'settings_reloading', message: 'reloaded; retry the save' } }),
    { status: 503, headers: { 'Retry-After': retryAfter } },
  );
}

beforeEach(() => {
  vi.useFakeTimers();
  useToastStore.setState({ toasts: [], _timers: new Map() });
  refreshSpy = vi.fn().mockResolvedValue(undefined);
  useItervoxStore.setState({ refreshSnapshot: refreshSpy });
  fetchMock = vi.fn<FetchFn>();
  global.fetch = fetchMock as unknown as typeof fetch;
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('useSettingsActions settings_reloading retry', () => {
  it('retries a 503 settings_reloading once after Retry-After, then refreshes; no error toast', async () => {
    fetchMock
      .mockResolvedValueOnce(reloading('1'))
      .mockResolvedValueOnce(new Response('{"ok":true}', { status: 200 }));
    const { result } = renderHook(() => useSettingsActions());
    const p = result.current.setMaxRetries(4);
    await vi.advanceTimersByTimeAsync(900);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(200);
    await expect(p).resolves.toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1][0]).toBe('/api/v1/settings/agent/max-retries');
    expect((fetchMock.mock.calls[1][1] as RequestInit).body).toBe('{"maxRetries":4}');
    expect(refreshSpy).toHaveBeenCalledTimes(1);
    expect(useToastStore.getState().toasts.filter((t) => t.variant === 'error')).toHaveLength(0);
  });

  it('surfaces the server message when the retry also fails', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(reloading('0')));
    const { result } = renderHook(() => useSettingsActions());
    const p = result.current.setMaxRetries(4);
    // Not runAllTimers: that would also fire the toast auto-dismiss.
    await vi.advanceTimersByTimeAsync(50);
    await expect(p).resolves.toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const errors = useToastStore.getState().toasts.filter((t) => t.variant === 'error');
    expect(errors).toHaveLength(1);
    expect(errors[0].message).toBe('Failed to update max retries: reloaded; retry the save');
  });

  it('settingsFetchTyped retries the same way', async () => {
    fetchMock
      .mockResolvedValueOnce(reloading('0'))
      .mockResolvedValueOnce(new Response('{}', { status: 200 }));
    const p = settingsFetchTyped('/api/v1/settings/automations', 'PUT', { automations: [] });
    await vi.runAllTimersAsync();
    await expect(p).resolves.toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
