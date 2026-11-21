/**
 * The WebSocket close code the daemon sends when it ends a live terminal
 * because the caller's token lost access while it was open: revoked,
 * expired, rotated, or downgraded below the scope the terminal's mode
 * needs (internal/server/terminal_recheck.go). The close reason names why.
 */
export const TERMINAL_CLOSE_ACCESS_LOST = 4403;

const ACCESS_LOST_TEXT: Record<string, string> = {
  token_revoked: 'Your access token was revoked, so the daemon closed this terminal.',
  token_expired: 'Your access token expired, so the daemon closed this terminal.',
  token_rotated:
    'Your access token was rotated, so the daemon closed this terminal. Sign in with the new token to reconnect.',
  scope_insufficient:
    'Your access token no longer has the scope this terminal needs, so the daemon closed it. Reconnect to get the terminal your scope allows.',
  token_store_invalid:
    'The daemon cannot read its token store, so it closed this terminal until the store is fixed.',
};

/**
 * Why the daemon closed a terminal, as a sentence for the terminal view,
 * or '' for an ordinary detach (the session ended, the panel closed it, a
 * reload or shutdown).
 */
export function terminalCloseMessage(code: number, reason: string): string {
  if (code !== TERMINAL_CLOSE_ACCESS_LOST) return '';
  return (
    ACCESS_LOST_TEXT[reason] ??
    `The daemon closed this terminal because your access changed${reason ? ` (${reason})` : ''}.`
  );
}

/** The close reason of a terminal whose token fell below its mode's scope. */
export const TERMINAL_LOST_SCOPE = 'scope_insufficient';

/**
 * Whether the daemon closed the terminal because the caller's token no
 * longer holds the scope the terminal's mode needs. A read-write terminal
 * closed that way reconnects read-only: the caller scope the dashboard
 * holds comes from the snapshot stream, fixed when it connected, so it can
 * still say admin.
 */
export function isScopeLossClose(code: number, reason: string): boolean {
  return code === TERMINAL_CLOSE_ACCESS_LOST && reason === TERMINAL_LOST_SCOPE;
}

/** The text shown when the daemon refuses a read-write ticket for scope. */
export const WRITE_TICKET_SCOPE_TEXT =
  'Your access token no longer allows a read-write terminal. Reconnect for a read-only one.';
