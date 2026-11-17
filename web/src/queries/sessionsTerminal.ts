// Sessions panel, live terminal (#298).
//
// POST /api/v1/sessions/{id}/terminal/ticket — a single-use, 30-second ticket
// for the terminal's WebSocket (operator: read-only; "write" needs admin and
// sessions.terminal_write). One ticket per connection, never cached, so this
// is a plain request rather than a query; the socket itself lives in
// auth/terminalSocket.ts.

import { apiRequest } from '../auth/apiRequest';
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
