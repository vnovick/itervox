import { apiUrl } from './apiBase';

/**
 * #298 — the live terminal's WebSocket: THE ONLY raw `WebSocket` in
 * `web/src` (ESLint's `no-restricted-globals` refuses it anywhere else).
 *
 * Why it is not `authedFetch` / `openAuthedEventStream`: a browser cannot
 * set an `Authorization` header on a WebSocket. The socket instead carries a
 * short-lived, single-use ticket in its query, which the caller first mints
 * with an authed POST (`requestTerminalTicket` in `queries/sessionsTerminal.ts`,
 * through `apiRequest` → `authedFetch`, so a 401 still reaches the AuthGate).
 * The stored bearer token never goes into a socket URL. The URL comes from
 * the daemon's ticket response and is mapped through `apiUrl`, exactly like
 * every other API call, so it follows the configured API base and origin.
 *
 * Wire format (internal/server/terminal.go): the daemon sends binary frames
 * of raw terminal output; the browser sends binary frames whose first byte
 * is the op — 0x00 + typed bytes (UTF-8), 0x01 + cols + rows (uint16,
 * big-endian) for a resize.
 */

export const TERMINAL_OP_INPUT = 0x00;
export const TERMINAL_OP_RESIZE = 0x01;

/** A terminal size in character cells. */
export interface TerminalSize {
  cols: number;
  rows: number;
}

const encoder = new TextEncoder();

/** The frame that types `data` into the session. */
export function encodeInput(data: string): Uint8Array {
  const bytes = encoder.encode(data);
  const frame = new Uint8Array(bytes.length + 1);
  frame[0] = TERMINAL_OP_INPUT;
  frame.set(bytes, 1);
  return frame;
}

/** The frame that resizes the session's terminal. */
export function encodeResize({ cols, rows }: TerminalSize): Uint8Array {
  const frame = new Uint8Array(5);
  const view = new DataView(frame.buffer);
  view.setUint8(0, TERMINAL_OP_RESIZE);
  view.setUint16(1, Math.max(0, Math.min(cols, 0xffff)));
  view.setUint16(3, Math.max(0, Math.min(rows, 0xffff)));
  return frame;
}

/**
 * The socket URL of a ticket's `url` (`/api/v1/...?ticket=…`), with the
 * initial size: mapped through `apiUrl`, resolved against the page, and
 * switched to ws:/wss:.
 */
export function terminalSocketUrl(
  path: string,
  size: TerminalSize,
  base: string = window.location.href,
): string {
  const url = new URL(apiUrl(path), base);
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  url.searchParams.set('cols', String(size.cols));
  url.searchParams.set('rows', String(size.rows));
  return url.toString();
}

export interface TerminalSocketHandlers {
  /** Terminal output from the daemon. */
  onData: (bytes: Uint8Array) => void;
  onOpen?: () => void;
  /** The socket closed (the daemon detached, the session ended, or close()). */
  onClose?: (event: { code: number; reason: string }) => void;
}

/** An open terminal socket. `send` drops frames until it is open. */
export interface TerminalSocket {
  send: (frame: Uint8Array) => void;
  close: () => void;
}

/** Opens the socket of a ticket's `url`. */
export function openTerminalSocket(
  path: string,
  size: TerminalSize,
  handlers: TerminalSocketHandlers,
): TerminalSocket {
  const ws = new WebSocket(terminalSocketUrl(path, size));
  ws.binaryType = 'arraybuffer';
  ws.onopen = () => {
    handlers.onOpen?.();
  };
  ws.onmessage = (ev: MessageEvent) => {
    if (ev.data instanceof ArrayBuffer) handlers.onData(new Uint8Array(ev.data));
    else if (typeof ev.data === 'string') handlers.onData(encoder.encode(ev.data));
  };
  ws.onclose = (ev: CloseEvent) => {
    handlers.onClose?.({ code: ev.code, reason: ev.reason });
  };
  return {
    send: (frame) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(frame);
    },
    close: () => {
      ws.onclose = null;
      ws.close();
    },
  };
}
