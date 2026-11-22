import { getApiContext } from '../../auth/apiBase';

/**
 * #278 phase 2 — the terminal-only page the desktop app opens in its own
 * window: `/sessions/terminal?id=<session id>`. App renders it before the
 * router, so the window carries the live terminal of one session and
 * nothing else (no sidebar, no event stream, no notifications). The
 * desktop app builds the same path (desktop/src-tauri/src/terminal_window.rs
 * `TERMINAL_PAGE_PATH`, drift-checked by its tests).
 */
export const TERMINAL_WINDOW_PATH = '/sessions/terminal';

/**
 * Whether `pathname` is the terminal window's page: on a daemon's own
 * dashboard, or (#334) on a project's dashboard through the hub,
 * `/hub/p/{project}/sessions/terminal`, where the terminal is the hub's
 * read-only pass-through (terminalModeFor). Never the hub's own pages.
 */
export function isTerminalWindowPath(pathname: string = window.location.pathname): boolean {
  const ctx = getApiContext();
  if (ctx.mode === 'hub') return false;
  let path = pathname;
  if (ctx.mode === 'hubProject') {
    if (!pathname.startsWith(`${ctx.basename}/`)) return false;
    path = pathname.slice(ctx.basename.length);
  }
  return path.replace(/\/+$/, '') === TERMINAL_WINDOW_PATH;
}

/** The session the page shows: `?id=`, or '' when absent. */
export function terminalWindowSession(search: string = window.location.search): string {
  return new URLSearchParams(search).get('id')?.trim() ?? '';
}
