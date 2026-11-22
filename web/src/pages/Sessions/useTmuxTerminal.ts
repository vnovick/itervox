import { useEffect, useState, type RefObject } from 'react';
import { encodeInput, encodeResize, openTerminalSocket } from '../../auth/terminalSocket';
import type { TerminalSocket } from '../../auth/terminalSocket';
import { ApiError } from '../../auth/apiRequest';
import { UnauthorizedError } from '../../auth/UnauthorizedError';
import { requestTerminalTicket, type TerminalMode } from '../../queries/sessionsTerminal';
import { createTerminal } from './createTerminal';
import {
  isScopeLossClose,
  terminalCloseMessage,
  WRITE_TICKET_SCOPE_TEXT,
} from './terminalCloseReason';

/** Where a live terminal is: connecting, attached, detached, or refused. */
export type TerminalStatus = 'connecting' | 'live' | 'closed' | 'error';

/** How a socket closed: its WebSocket close code and reason (#278). */
export interface TerminalClose {
  code: number;
  reason: string;
}

/**
 * The socket's close code and reason as the status line shows them: any
 * code the daemon (or the browser) closed with, e.g. an access re-check's.
 */
export function closeText({ code, reason }: TerminalClose): string {
  return reason ? `closed ${String(code)}: ${reason}` : `closed ${String(code)}`;
}

/** What a live terminal is doing, and what the daemon granted. */
export interface TerminalState {
  status: TerminalStatus;
  error: string;
  /** The mode the daemon's ticket granted; null until it answered. */
  granted: TerminalMode | null;
  /** How the socket closed, once it did (any code, with its reason). */
  closed: TerminalClose | null;
  /** The token no longer holds the mode's scope (see below). */
  scopeLost: boolean;
}

const CONNECTING: TerminalState = {
  status: 'connecting',
  error: '',
  granted: null,
  closed: null,
  scopeLost: false,
};

/**
 * #298 — attaches a live terminal to session `id` inside `host`: renders
 * the terminal (createTerminal), mints a ticket, opens the socket, pipes
 * output in and (read-write only) keys out, and keeps the session's size
 * in step with the panel. Bumping `attempt` reconnects. Everything is torn
 * down (socket closed = detached; the session keeps running) on unmount.
 * `error` is the refusal ('error'), or, once 'closed', why the daemon
 * closed the terminal when the caller's access changed ('' otherwise).
 * `scopeLost` is set when the caller's token no longer holds the mode's
 * scope: the daemon closed the terminal with `scope_insufficient`, or
 * refused the ticket with 403 `insufficient_scope`. The caller then
 * reconnects read-only (TmuxLivePanel), whatever its snapshot scope says.
 */
export function useTmuxTerminal(
  host: RefObject<HTMLElement | null>,
  id: string,
  mode: TerminalMode,
  attempt: number,
): TerminalState {
  // The state of one connection; a new id, mode or attempt starts over at
  // 'connecting' (derived below, not reset inside the effect).
  const key = `${id}\n${mode}\n${String(attempt)}`;
  const [state, setState] = useState<TerminalState & { key: string }>({ key, ...CONNECTING });

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    let cancelled = false;
    let socket: TerminalSocket | null = null;
    let granted: TerminalMode | null = null;
    const setStatus = (
      status: TerminalStatus,
      error = '',
      closed: TerminalClose | null = null,
      scopeLost = false,
    ) => {
      if (!cancelled) setState({ key, status, error, granted, closed, scopeLost });
    };
    const term = createTerminal({ readOnly: mode === 'read' });
    term.open(el);
    const offData = term.onData((data) => {
      // Keys go out only when asked for AND granted read-write.
      if (mode === 'write' && granted === 'write') socket?.send(encodeInput(data));
    });
    const offResize = term.onResize((size) => {
      socket?.send(encodeResize(size));
    });
    const observer =
      typeof ResizeObserver === 'undefined'
        ? null
        : new ResizeObserver(() => {
            term.fit();
          });
    observer?.observe(el);

    requestTerminalTicket(id, mode)
      .then((ticket) => {
        if (cancelled) return;
        granted = ticket.mode;
        setStatus('connecting');
        socket = openTerminalSocket(ticket.url, term.size(), {
          onData: (bytes) => {
            term.write(bytes);
          },
          onOpen: () => {
            setStatus('live');
            term.focus();
          },
          onClose: ({ code, reason }) => {
            setStatus(
              'closed',
              terminalCloseMessage(code, reason),
              { code, reason },
              isScopeLossClose(code, reason),
            );
          },
        });
      })
      .catch((err: unknown) => {
        if (cancelled || err instanceof UnauthorizedError) return; // AuthGate handles 401
        if (err instanceof ApiError && err.status === 403 && err.code === 'insufficient_scope') {
          setStatus('error', mode === 'write' ? WRITE_TICKET_SCOPE_TEXT : err.message, null, true);
          return;
        }
        setStatus(
          'error',
          err instanceof Error ? err.message : 'The terminal could not be attached.',
        );
      });

    return () => {
      cancelled = true;
      socket?.close();
      observer?.disconnect();
      offData();
      offResize();
      term.dispose();
    };
  }, [host, id, mode, key]);

  if (state.key !== key) return CONNECTING;
  const { status, error, granted, closed, scopeLost } = state;
  return { status, error, granted, closed, scopeLost };
}
