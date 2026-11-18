/**
 * The WebSocket close code the daemon sends when it ends a live terminal
 * because the caller's token lost access while it was open: revoked,
 * expired, rotated, or downgraded below the scope the terminal's mode
 * needs (internal/server/terminal_recheck.go). The close reason names why.
 *
 * Through the hub (internal/hub/terminal_relay.go) the same code carries
 * `hub_…` reasons when the caller's hub token lost access, and `member_…`
 * reasons when the project revoked the hub's own member token.
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
  hub_token_revoked: 'Your hub token was revoked, so the hub closed this terminal.',
  hub_token_expired: 'Your hub token expired, so the hub closed this terminal.',
  hub_token_rotated:
    'Your hub token was rotated, so the hub closed this terminal. Sign in with the new token to reconnect.',
  hub_scope_insufficient:
    'Your hub token no longer has the operator scope the terminal needs, so the hub closed it.',
  hub_token_store_invalid:
    'The hub cannot read its token store, so it closed this terminal until the store is fixed.',
  member_token_revoked:
    "The project revoked the hub's access token, so this terminal closed. Reconnect once the hub has a new one, or open the project's own dashboard.",
  member_token_expired: "The hub's access token on this project expired, so this terminal closed.",
  member_token_rotated:
    "The hub's access token on this project was rotated, so this terminal closed. Reconnect to open it again.",
  member_scope_insufficient:
    "The hub's access token on this project no longer has the scope the terminal needs, so it closed.",
  member_token_store_invalid:
    'The project cannot read its token store, so it closed this terminal until the store is fixed.',
};

/**
 * Why the daemon (or the hub) closed a terminal, as a sentence for the
 * terminal view, or '' for an ordinary detach (the session ended, the panel
 * closed it, a reload or shutdown).
 */
export function terminalCloseMessage(code: number, reason: string): string {
  if (code !== TERMINAL_CLOSE_ACCESS_LOST) return '';
  return (
    ACCESS_LOST_TEXT[reason] ??
    `The daemon closed this terminal because your access changed${reason ? ` (${reason})` : ''}.`
  );
}
