import { useEffect } from 'react';
import { useNavigate } from 'react-router';

// Embedded mode: the dashboard shown inside the desktop app's window, next
// to the app's own project rail. The app's webview initialization script
// (desktop/src-tauri/src/dashboard.rs) sets `window.__ITERVOX_SHELL__ =
// 'desktop'` before this bundle runs; the flag is read once, at boot, and
// never written back to a URL. Embedded, the shell drops its own sidebar
// (the app has the rail and a native Go menu) and lands on the issue view.
//
// `?embedded=1` turns it on in the dev server only (the route-mocked e2e
// harness runs `pnpm dev`); a production build ignores it, so a shared link
// can never switch a browser into the app's layout.

declare global {
  interface Window {
    __ITERVOX_SHELL__?: string;
    /** Set in embedded mode: the desktop app's Go menu navigates with it. */
    __itervoxNavigate?: (path: string) => boolean;
  }
}

/** The value the desktop app's initialization script sets. */
export const DESKTOP_SHELL = 'desktop';
/** Dev-server-only query switch (the e2e harness). */
export const EMBEDDED_QUERY_PARAM = 'embedded';

/** Reads the embedded flag from a window (pure: testable without caching). */
export function detectEmbedded(win: Pick<Window, '__ITERVOX_SHELL__' | 'location'>, dev: boolean) {
  if (win.__ITERVOX_SHELL__ === DESKTOP_SHELL) return true;
  if (!dev) return false;
  return new URLSearchParams(win.location.search).get(EMBEDDED_QUERY_PARAM) === '1';
}

let cached: boolean | null = null;

/** The embedded flag, read once per page load. */
export function isEmbedded(): boolean {
  if (cached === null) {
    cached = typeof window !== 'undefined' && detectEmbedded(window, import.meta.env.DEV);
  }
  return cached;
}

/** Tests only: forget the cached flag (or pin it). */
export function resetEmbeddedForTests(value: boolean | null = null): void {
  cached = value;
}

/** The embedded flag as a hook (constant for the page's lifetime). */
export function useEmbedded(): boolean {
  return isEmbedded();
}

/**
 * A path the native menu may navigate to: an in-app absolute path. Never a
 * scheme, a protocol-relative `//host`, a backslash or a control character,
 * so the hook can only move within the dashboard.
 */
export function isInAppPath(path: unknown): path is string {
  if (typeof path !== 'string' || path.length === 0 || path.length > 512) return false;
  if (!path.startsWith('/') || path.startsWith('//')) return false;
  // eslint-disable-next-line no-control-regex
  return !/[\\\u0000-\u001f]/.test(path);
}

/**
 * Embedded only: exposes `window.__itervoxNavigate(path)` for the desktop
 * app's Go menu, which evaluates it in this webview. Returns whether the
 * path was accepted. Removed again on unmount. Mounted once, in the router.
 */
export function useEmbeddedNavigateHook(): void {
  const navigate = useNavigate();
  const embedded = useEmbedded();
  useEffect(() => {
    if (!embedded) return;
    const hook = (path: string) => {
      if (!isInAppPath(path)) return false;
      void navigate(path);
      return true;
    };
    window.__itervoxNavigate = hook;
    return () => {
      if (window.__itervoxNavigate === hook) delete window.__itervoxNavigate;
    };
  }, [embedded, navigate]);
}

/** React Router's key of the page load's first history entry. */
const FIRST_ENTRY_KEY = 'default';

/**
 * Embedded only: opening a project lands on the issue view. True on the page
 * load's first history entry of the board's index route when the URL asks
 * for nothing else (a deep link such as `?issue=` or `?newTask=1` keeps the
 * board). A later visit of "/" (the Board button) is a new history entry,
 * so it stays on the board. Pure, so StrictMode's double render agrees.
 */
export function isEmbeddedLanding(location: { search: string; key: string }): boolean {
  if (!isEmbedded() || location.key !== FIRST_ENTRY_KEY) return false;
  const params = new URLSearchParams(location.search);
  params.delete('token');
  params.delete(EMBEDDED_QUERY_PARAM);
  return params.toString() === '';
}
