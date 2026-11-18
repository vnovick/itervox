import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { configureApiContext, configureApiOrigin } from '../apiBase';
import {
  encodeInput,
  encodeResize,
  openTerminalSocket,
  terminalSocketUrl,
  TERMINAL_OP_INPUT,
  TERMINAL_OP_RESIZE,
} from '../terminalSocket';

// #298 — the live terminal's socket: the wire frames, the URL (through
// apiUrl, ws:/wss:), and the one raw WebSocket's behaviour.

class FakeSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static instances: FakeSocket[] = [];
  readyState = FakeSocket.CONNECTING;
  binaryType = 'blob';
  sent: Uint8Array[] = [];
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  constructor(public url: string) {
    FakeSocket.instances.push(this);
  }
  send(frame: Uint8Array) {
    this.sent.push(frame);
  }
  close() {
    this.closed = true;
  }
}

describe('terminal socket frames', () => {
  it('prefixes typed bytes with the input op, UTF-8 encoded', () => {
    const frame = encodeInput('é\r');
    expect(Array.from(frame)).toEqual([TERMINAL_OP_INPUT, 0xc3, 0xa9, 0x0d]);
  });

  it('encodes a resize as op + big-endian cols and rows, clamped', () => {
    expect(Array.from(encodeResize({ cols: 300, rows: 40 }))).toEqual([
      TERMINAL_OP_RESIZE,
      0x01,
      0x2c,
      0x00,
      0x28,
    ]);
    expect(Array.from(encodeResize({ cols: -1, rows: 70000 }))).toEqual([
      TERMINAL_OP_RESIZE,
      0,
      0,
      0xff,
      0xff,
    ]);
  });
});

describe('terminalSocketUrl', () => {
  afterEach(() => {
    configureApiContext('/');
    configureApiOrigin(null);
  });

  it('maps the ticket path onto ws: on the page origin with the size', () => {
    const url = terminalSocketUrl(
      '/api/v1/sessions/itervox-a/terminal?ticket=itvt_x',
      { cols: 100, rows: 30 },
      'http://127.0.0.1:8090/sessions',
    );
    expect(url).toBe(
      'ws://127.0.0.1:8090/api/v1/sessions/itervox-a/terminal?ticket=itvt_x&cols=100&rows=30',
    );
  });

  it('uses wss: on an https page and follows a configured API origin', () => {
    configureApiOrigin('https://daemon.example');
    const url = terminalSocketUrl(
      '/api/v1/sessions/s/terminal?ticket=t',
      { cols: 80, rows: 24 },
      'https://dash.example/sessions',
    );
    expect(url).toBe('wss://daemon.example/api/v1/sessions/s/terminal?ticket=t&cols=80&rows=24');
  });

  it("under the hub, opens the hub's socket route for the project", () => {
    configureApiContext('/hub/p/alpha/sessions');
    const url = terminalSocketUrl(
      '/api/v1/sessions/s/terminal?ticket=t',
      { cols: 80, rows: 24 },
      'http://127.0.0.1:8095/hub/p/alpha/sessions',
    );
    expect(url).toBe(
      'ws://127.0.0.1:8095/hub/p/alpha/api/v1/sessions/s/terminal?ticket=t&cols=80&rows=24',
    );
  });
});

describe('openTerminalSocket', () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    vi.stubGlobal('WebSocket', FakeSocket);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('delivers binary and text output, sends only while open, and closes quietly', () => {
    const onData = vi.fn();
    const onOpen = vi.fn();
    const onClose = vi.fn();
    const socket = openTerminalSocket(
      '/api/v1/sessions/s/terminal?ticket=t',
      { cols: 80, rows: 24 },
      { onData, onOpen, onClose },
    );
    const ws = FakeSocket.instances[0];
    expect(ws.binaryType).toBe('arraybuffer');
    expect(ws.url).toMatch(/^ws:\/\/.*\/api\/v1\/sessions\/s\/terminal\?ticket=t&cols=80&rows=24$/);

    socket.send(encodeInput('early'));
    expect(ws.sent).toHaveLength(0);
    ws.readyState = FakeSocket.OPEN;
    ws.onopen?.();
    expect(onOpen).toHaveBeenCalled();
    socket.send(encodeInput('x'));
    expect(ws.sent).toHaveLength(1);

    ws.onmessage?.({ data: new Uint8Array([104, 105]).buffer });
    ws.onmessage?.({ data: 'ok' });
    expect(onData).toHaveBeenNthCalledWith(1, new Uint8Array([104, 105]));
    expect(onData).toHaveBeenNthCalledWith(2, new TextEncoder().encode('ok'));

    ws.onclose?.({ code: 1006, reason: '' });
    expect(onClose).toHaveBeenCalledWith({ code: 1006, reason: '' });

    socket.close();
    expect(ws.closed).toBe(true);
    expect(ws.onclose).toBeNull();
  });
});
