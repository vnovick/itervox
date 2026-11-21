// Sessions panel, live terminal (#298).
//
// POST /api/v1/sessions/{id}/terminal/ticket — a single-use, 30-second ticket
// for the terminal's WebSocket (operator: read-only; "write" needs admin and
// sessions.terminal_write). One ticket per connection, never cached, so this
// is a plain request rather than a query; the socket itself lives in
// auth/terminalSocket.ts.

import { apiRequest } from '../auth/apiRequest';
import { normalizeScope, type Scope } from '../auth/scope';
import { TerminalTicketSchema, type TerminalTicket } from '../types/schemas';

export type TerminalMode = TerminalTicket['mode'];

export async function requestTerminalTicket(
  id: string,
  mode: TerminalMode,
): Promise<TerminalTicket> {
  const { data } = await apiRequest(`/api/v1/sessions/${encodeURIComponent(id)}/terminal/ticket`, {
    op: 'terminalTicket',
    method: 'POST',
    json: { mode },
  });
  return TerminalTicketSchema.parse(data);
}

/**
 * The caller's scope as the daemon sees it now: a fresh GET /api/v1/state,
 * not the snapshot stream, whose scope is the one it connected with. Used
 * after a scope loss before a read-write terminal is offered again.
 */
export async function fetchCallerScope(): Promise<Scope> {
  const { data } = await apiRequest<{ callerScope?: unknown }>('/api/v1/state', {
    op: 'callerScope',
  });
  const raw = data?.callerScope;
  // Absent: a daemon predating scopes (admin); not a string: least privilege.
  if (raw === undefined) return normalizeScope(undefined);
  return normalizeScope(typeof raw === 'string' ? raw : '');
}
