// CORE-089 — AuthGate with the REAL ServerDownScreen (AuthGate.test.tsx mocks
// it, which is how the double probe on Retry went unnoticed).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { AuthGate } from '../AuthGate';
import { useAuthStore } from '../authStore';
import { useTokenStore } from '../tokenStore';

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  return input instanceof URL ? input.pathname : input.url;
}

let fetchSpy: ReturnType<typeof vi.fn>;
const healthCalls = () =>
  fetchSpy.mock.calls.filter(([input]) => urlOf(input as RequestInfo).endsWith('/api/v1/health'))
    .length;

beforeEach(() => {
  useAuthStore.setState({ status: 'serverDown', rejectedReason: null });
  useTokenStore.setState({ token: null });
  window.history.replaceState(null, '', '/');
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

async function flush() {
  // Let pending promise callbacks and effects run.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe('AuthGate with the real ServerDownScreen (CORE-089)', () => {
  it('manual retry probes /api/v1/health once', async () => {
    // Health stays down, so the screen comes back after the probe.
    fetchSpy = vi.fn(() => Promise.reject(new TypeError('network down')));
    global.fetch = fetchSpy as unknown as typeof fetch;
    render(
      <AuthGate>
        <div>app</div>
      </AuthGate>,
    );
    expect(screen.getByText(/can't reach the daemon/i)).toBeInTheDocument();
    expect(healthCalls()).toBe(0);

    fireEvent.click(screen.getByRole('button', { name: /retry/i }));
    await flush();
    await flush();

    expect(healthCalls()).toBe(1);
    expect(await screen.findByText(/can't reach the daemon/i)).toBeInTheDocument();
  });

  it('auto-retry tick during an in-flight probe does not start a second health request', async () => {
    vi.useFakeTimers();
    let resolveHealth: ((r: Response) => void) | null = null;
    fetchSpy = vi.fn((input: RequestInfo | URL) => {
      if (urlOf(input).endsWith('/api/v1/health')) {
        return new Promise<Response>((resolve) => {
          resolveHealth = resolve;
        });
      }
      return Promise.resolve(new Response('{}', { status: 200 }));
    });
    global.fetch = fetchSpy as unknown as typeof fetch;
    render(
      <AuthGate>
        <div data-testid="app">app</div>
      </AuthGate>,
    );
    // First auto-retry tick starts a probe whose health request hangs.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(healthCalls()).toBe(1);
    // The next tick fires while that probe is still pending: no new request.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(healthCalls()).toBe(1);
    // The pending probe finishes; the app renders and the timer is gone.
    await act(async () => {
      resolveHealth?.(new Response('{}', { status: 200 }));
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByTestId('app')).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
    });
    expect(healthCalls()).toBe(1);
  });

  it('the probing spinner is announced as a status', () => {
    fetchSpy = vi.fn(() => new Promise<Response>(() => undefined));
    global.fetch = fetchSpy as unknown as typeof fetch;
    useAuthStore.setState({ status: 'unknown', rejectedReason: null });
    render(
      <AuthGate>
        <div>app</div>
      </AuthGate>,
    );
    expect(screen.getByRole('status')).toHaveTextContent(/connecting to the itervox daemon/i);
  });
});

// M6-close (BH-M6 LOW) — a hung probe used to leave Retry stuck forever: the
// in-flight guard dropped every later tick while the first fetch never settled.
describe('AuthGate probe timeout (M6-close)', () => {
  it('a hung health fetch times out, shows the server-down screen, and the auto-retry recovers', async () => {
    vi.useFakeTimers();
    let calls = 0;
    fetchSpy = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (urlOf(input).endsWith('/api/v1/health')) {
        calls += 1;
        if (calls === 1) {
          // Hangs until aborted by the probe's timeout signal.
          return new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener('abort', () => {
              reject(new DOMException('timed out', 'TimeoutError'));
            });
          });
        }
      }
      return Promise.resolve(new Response('{}', { status: 200 }));
    });
    global.fetch = fetchSpy as unknown as typeof fetch;
    useAuthStore.setState({ status: 'unknown', rejectedReason: null });
    render(
      <AuthGate>
        <div data-testid="app">app</div>
      </AuthGate>,
    );
    expect(healthCalls()).toBe(1);
    // The first probe hangs; after the timeout it fails as "server down".
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(screen.getByText(/can't reach the daemon/i)).toBeInTheDocument();
    // The next auto-retry tick probes again (the guard was released) and recovers.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(healthCalls()).toBe(2);
    expect(screen.getByTestId('app')).toBeInTheDocument();
  });
});
