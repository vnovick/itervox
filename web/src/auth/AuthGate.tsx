import { useCallback, useEffect, useRef, type ReactNode } from 'react';
import { useAuthStore } from './authStore';
import { getToken, useTokenStore } from './tokenStore';
import { TokenEntryScreen } from './TokenEntryScreen';
import { ServerDownScreen } from './ServerDownScreen';
import { AUTH_PROBE_TIMEOUT_MS } from '../utils/timings';

/**
 * Top-level wrapper that gates the app on successful authentication.
 *
 * Mount flow:
 *   1. Capture `?token=` from the URL (if present) → tokenStore → strip from URL.
 *   2. Probe unauthenticated `/api/v1/health` to distinguish "server down"
 *      from "auth misconfigured".
 *   3. If health ok, probe `/api/v1/state` with any stored token:
 *      - 200 → status='authorized', render children.
 *      - 401 → status='needsToken', render TokenEntryScreen.
 *      - 200 but no token stored and server didn't require one → authorized.
 *   4. If health failed → status='serverDown', render ServerDownScreen with retry.
 *
 * Re-probes when authStore.status flips back to 'unknown' (e.g. after retry).
 */

function captureTokenFromUrl(): void {
  if (typeof window === 'undefined') return;
  const url = new URL(window.location.href);
  const token = url.searchParams.get('token');
  if (!token) return;
  useTokenStore.getState().setToken(token, false);
  url.searchParams.delete('token');
  const cleaned = url.pathname + (url.search ? url.search : '') + url.hash;
  window.history.replaceState(null, '', cleaned);
}

export function AuthGate({ children }: { children: ReactNode }) {
  const status = useAuthStore((s) => s.status);

  // CORE-089 — one probe at a time: a ServerDownScreen auto-retry tick (or the
  // status effect) can land while an earlier probe is still pending; it must
  // not start a second one. M6-close: each probe fetch is bounded
  // (fetchWithTimeout), so a hung request can no longer hold this guard — and
  // with it every later Retry — forever.
  const inFlight = useRef(false);

  const probe = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    try {
      await probeOnce();
    } finally {
      inFlight.current = false;
    }
  }, []);

  useEffect(() => {
    captureTokenFromUrl();
  }, []);

  useEffect(() => {
    if (status === 'unknown') {
      void probe();
    }
  }, [status, probe]);

  if (status === 'authorized') return <>{children}</>;
  if (status === 'needsToken') return <TokenEntryScreen />;
  if (status === 'serverDown') {
    return (
      <ServerDownScreen
        onRetry={() => {
          void probe();
        }}
      />
    );
  }
  // 'unknown' — a labelled spinner while probing (CORE-089).
  return (
    <div role="status" className="flex min-h-screen items-center justify-center">
      <div
        aria-hidden="true"
        className="h-6 w-6 animate-spin rounded-full border-2 border-current border-t-transparent"
      />
      <span className="sr-only">Connecting to the itervox daemon…</span>
    </div>
  );
}

/**
 * fetch() bounded by AUTH_PROBE_TIMEOUT_MS: the request is aborted and the
 * promise rejects, which the probe treats like any network failure.
 */
async function fetchWithTimeout(url: string, init: RequestInit = {}): Promise<Response> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => {
    ctrl.abort(new DOMException('probe timed out', 'TimeoutError'));
  }, AUTH_PROBE_TIMEOUT_MS);
  try {
    return await fetch(url, { ...init, signal: ctrl.signal });
  } finally {
    clearTimeout(timer);
  }
}

/** Health probe, then the authenticated state probe; sets the auth status. */
async function probeOnce(): Promise<void> {
  // Health probe — unauthenticated.
  try {
    const health = await fetchWithTimeout('/api/v1/health');
    if (!health.ok) {
      useAuthStore.getState().setStatus('serverDown');
      return;
    }
  } catch {
    useAuthStore.getState().setStatus('serverDown');
    return;
  }

  // Authenticated state probe.
  const token = getToken();
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  try {
    const res = await fetchWithTimeout('/api/v1/state', { headers });
    if (res.status === 401) {
      useTokenStore.getState().clearToken();
      useAuthStore
        .getState()
        .markUnauthorized(
          token ? 'Stored token was rejected. Paste a fresh one.' : 'Server requires an API token.',
        );
      return;
    }
    if (!res.ok) {
      useAuthStore.getState().setStatus('serverDown');
      return;
    }
    useAuthStore.getState().setStatus('authorized');
  } catch {
    useAuthStore.getState().setStatus('serverDown');
  }
}
