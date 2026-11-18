import { getApiContext } from '../../auth/apiBase';
import { scopeAllows, type Scope } from '../../auth/scope';
import type { TerminalMode } from '../../queries/sessionsTerminal';
import type { TmuxStatus } from '../../types/schemas';

/**
 * #298 — the live terminal the caller gets, or null for the snapshot only:
 * none for a viewer, on a daemon without it (Windows, older daemons),
 * without tmux, or through the hub (which never proxies the terminal's
 * socket); read-write only for admin with sessions.terminal_write. The
 * daemon enforces the same rules; this only decides what to show.
 */
export function terminalModeFor(scope: Scope, tmux: TmuxStatus | undefined): TerminalMode | null {
  if (!tmux?.available || !tmux.terminal) return null;
  if (getApiContext().mode !== 'daemon') return null;
  if (!scopeAllows(scope, 'operator')) return null;
  return scopeAllows(scope, 'admin') && tmux.terminalWrite ? 'write' : 'read';
}
