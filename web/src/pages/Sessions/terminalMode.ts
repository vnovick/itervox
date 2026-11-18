import { getApiContext } from '../../auth/apiBase';
import { scopeAllows, type Scope } from '../../auth/scope';
import type { TerminalMode } from '../../queries/sessionsTerminal';
import type { TmuxStatus } from '../../types/schemas';

/**
 * #298 — the live terminal the caller gets, or null for the snapshot only:
 * none for a viewer, on a daemon without it (Windows, older daemons), or
 * without tmux; read-write only for admin with sessions.terminal_write, on
 * the project's own dashboard. Through the hub (/hub/p/{project}/) it is
 * always read-only: the hub brokers read tickets only and never relays a
 * keystroke (internal/hub/terminal.go). The daemon and the hub enforce the
 * same rules; this only decides what to show.
 */
export function terminalModeFor(scope: Scope, tmux: TmuxStatus | undefined): TerminalMode | null {
  if (!tmux?.available || !tmux.terminal) return null;
  const { mode } = getApiContext();
  if (mode === 'hub') return null;
  if (!scopeAllows(scope, 'operator')) return null;
  if (mode === 'hubProject') return 'read';
  return scopeAllows(scope, 'admin') && tmux.terminalWrite ? 'write' : 'read';
}
