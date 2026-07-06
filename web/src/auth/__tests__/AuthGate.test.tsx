import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { AuthGate } from '../AuthGate';
import { useAuthStore } from '../authStore';
import { useTokenStore } from '../tokenStore';

// Mock the heavy child components so we only assert which one AuthGate picks.
vi.mock('../TokenEntryScreen', () => ({
  TokenEntryScreen: () => <div data-testid="token-entry">token entry</div>,
}));
vi.mock('../ServerDownScreen', () => ({
  ServerDownScreen: ({ onRetry }: { onRetry: () => void }) => (
    <button data-testid="server-down" type="button" onClick={onRetry}>
      retry
    </button>
  ),
}));

beforeEach(() => {
  useAuthStore.setState({ status: 'unknown', rejectedReason: null });
  useTokenStore.setState({ token: null });
  sessionStorage.clear();
  localStorage.clear();
  // Reset URL between tests so ?token= from one test doesn't leak.
  window.history.replaceState(null, '', '/');
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('AuthGate', () => {
  it('renders children once health + state probes both return 200', async () => {
    global.fetch = vi
      .fn()
      .mockResolvedValueOnce(new Response('{}', { status: 200 })) // /health
      .mockResolvedValueOnce(new Response('{}', { status: 200 })) as unknown as typeof fetch; // /state

    render(
      <AuthGate>
        <div data-testid="app">app</div>
      </AuthGate>,
    );

    await waitFor(() => {
      expect(screen.getByTestId('app')).toBeInTheDocument();
    });
    expect(useAuthStore.getState().status).toBe('authorized');
  });

  it('shows TokenEntryScreen when /state returns 401', async () => {
    global.fetch = vi
      .fn()
      .mockResolvedValueOnce(new Response('{}', { status: 200 })) // /health
      .mockResolvedValueOnce(new Response('nope', { status: 401 })) as unknown as typeof fetch; // /state

    render(
      <AuthGate>
        <div data-testid="app">app</div>
      </AuthGate>,
    );

    await waitFor(() => {
      expect(screen.getByTestId('token-entry')).toBeInTheDocument();
    });
    expect(useAuthStore.getState().status).toBe('needsToken');
  });

  it('shows ServerDownScreen when /health rejects', async () => {
    global.fetch = vi
      .fn()
      .mockRejectedValueOnce(new Error('network down')) as unknown as typeof fetch;

    render(
      <AuthGate>
        <div data-testid="app">app</div>
      </AuthGate>,
    );

    await waitFor(() => {
      expect(screen.getByTestId('server-down')).toBeInTheDocument();
    });
    expect(useAuthStore.getState().status).toBe('serverDown');
  });

  it('captures ?token= from URL into sessionStorage and strips it', async () => {
    window.history.replaceState(null, '', '/?token=from-url');
    global.fetch = vi
      .fn()
      .mockResolvedValueOnce(new Response('{}', { status: 200 }))
      .mockResolvedValueOnce(new Response('{}', { status: 200 })) as unknown as typeof fetch;

    render(
      <AuthGate>
        <div data-testid="app">app</div>
      </AuthGate>,
    );

    await waitFor(() => {
      expect(screen.getByTestId('app')).toBeInTheDocument();
    });
    expect(useTokenStore.getState().token).toBe('from-url');
    // The ?token= should have been scrubbed from the URL.
    expect(window.location.search).toBe('');
  });

  it('sends Authorization header on /state probe when a token is present', async () => {
    useTokenStore.getState().setToken('my-token', false);
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response('{}', { status: 200 })) // /health
      .mockResolvedValueOnce(new Response('{}', { status: 200 })); // /state
    global.fetch = fetchMock as unknown as typeof fetch;

    render(
      <AuthGate>
        <div data-testid="app">app</div>
      </AuthGate>,
    );

    await waitFor(() => {
      expect(screen.getByTestId('app')).toBeInTheDocument();
    });

    // Second call is /state — inspect its headers.
    const stateCall = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(stateCall[0]).toBe('/api/v1/state');
    const headers = (stateCall[1].headers ?? {}) as Record<string, string>;
    expect(headers.Authorization).toBe('Bearer my-token');
  });

  // Pins the URL-capture-vs-probe ordering contract (T-06): when AuthGate
  // mounts with BOTH a stale token in tokenStore AND a fresh ?token= in the
  // URL, the URL-capture useEffect must run before the probe so /state is
  // probed with the FRESH token, not the stale one. Without this, a user
  // would silently authenticate with a token they thought they were
  // replacing.
  it('uses URL token over stale stored token on initial mount', async () => {
    useTokenStore.getState().setToken('stale-token', false);
    window.history.replaceState(null, '', '/?token=fresh-token');

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response('{}', { status: 200 })) // /health
      .mockResolvedValueOnce(new Response('{}', { status: 200 })); // /state
    global.fetch = fetchMock as unknown as typeof fetch;

    render(
      <AuthGate>
        <div data-testid="app">app</div>
      </AuthGate>,
    );

    await waitFor(() => {
      expect(screen.getByTestId('app')).toBeInTheDocument();
    });

    const stateCall = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(stateCall[0]).toBe('/api/v1/state');
    const headers = (stateCall[1].headers ?? {}) as Record<string, string>;
    expect(headers.Authorization).toBe('Bearer fresh-token');
    expect(useTokenStore.getState().token).toBe('fresh-token');
    expect(window.location.search).toBe('');
  });
});
