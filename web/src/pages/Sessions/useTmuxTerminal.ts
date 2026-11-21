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
): { status: TerminalStatus; error: string; scopeLost: boolean } {
  // The state of one connection; a new id, mode or attempt starts over at
  // 'connecting' (derived below, not reset inside the effect).
  const key = `${id}\n${mode}\n${String(attempt)}`;
  const [state, setState] = useState<{
    key: string;
    status: TerminalStatus;
    error: string;
    scopeLost: boolean;
  }>({
    key,
    status: 'connecting',
    error: '',
    scopeLost: false,
  });

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    let cancelled = false;
    let socket: TerminalSocket | null = null;
    const setStatus = (status: TerminalStatus, error = '', scopeLost = false) => {
      if (!cancelled) setState({ key, status, error, scopeLost });
    };
    const term = createTerminal({ readOnly: mode === 'read' });
    term.open(el);
    const offData = term.onData((data) => {
      if (mode === 'write') socket?.send(encodeInput(data));
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
        socket = openTerminalSocket(ticket.url, term.size(), {
          onData: (bytes) => {
            term.write(bytes);
          },
          onOpen: () => {
            setStatus('live');
            term.focus();
          },
          onClose: ({ code, reason }) => {
            setStatus('closed', terminalCloseMessage(code, reason), isScopeLossClose(code, reason));
          },
        });
      })
      .catch((err: unknown) => {
        if (cancelled || err instanceof UnauthorizedError) return; // AuthGate handles 401
        if (err instanceof ApiError && err.status === 403 && err.code === 'insufficient_scope') {
          setStatus('error', mode === 'write' ? WRITE_TICKET_SCOPE_TEXT : err.message, true);
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

  return state.key === key
    ? { status: state.status, error: state.error, scopeLost: state.scopeLost }
    : { status: 'connecting', error: '', scopeLost: false };
}
