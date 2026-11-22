import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import type { ReactElement } from 'react';
import { configureApiContext } from '../../../auth/apiBase';
import { useTokenStore } from '../../../auth/tokenStore';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeTestQueryClient } from '../../../test/queryClient';
import { resetAllStores } from '../../../test/resetStores';
import { quickstartScenario } from '../../../test/fixtures/scenarios';
import TerminalWindow from '..';
import {
  isTerminalWindowPath,
  TERMINAL_WINDOW_PATH,
  terminalWindowSession,
} from '../terminalWindowPath';

// #278 phase 2 — the desktop app's terminal window: the page renders only
// the live terminal of the session in ?id=, through the dashboard's own
// TmuxTerminal (ticket via authedFetch, socket via auth/terminalSocket).
// Here the real TmuxTerminal runs; only the network (fetch, WebSocket) and
// the renderer (createTerminal) are fakes.

const m = vi.hoisted(() => ({
  terms: [] as { readOnly: boolean; disposed: boolean }[],
}));

vi.mock('../../Sessions/createTerminal', () => ({
  createTerminal: ({ readOnly }: { readOnly: boolean }) => {
    const t = { readOnly, disposed: false };
    m.terms.push(t);
    return {
      open: vi.fn(),
      write: vi.fn(),
      onData: () => () => undefined,
      onResize: () => () => undefined,
      fit: vi.fn(),
      size: () => ({ cols: 120, rows: 40 }),
      focus: vi.fn(),
      dispose: () => {
        t.disposed = true;
      },
    };
  },
}));

class FakeSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static instances: FakeSocket[] = [];
  readyState = FakeSocket.CONNECTING;
  binaryType = 'blob';
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  constructor(public url: string) {
    FakeSocket.instances.push(this);
  }
  send() {
    /* frames are not under test here */
  }
  close() {
    this.closed = true;
  }
}

interface Call {
  url: string;
  method: string;
  auth: string | null;
  body: string;
}

const TOKEN = 'app-held-token';

function sessionsBody(over: { terminal?: boolean; terminalWrite?: boolean } = {}) {
  return {
    generatedAt: '2026-11-20T10:00:00Z',
    tmux: {
      available: true,
      installHint: '',
      control: true,
      terminal: over.terminal ?? true,
      terminalWrite: over.terminalWrite ?? false,
    },
    sessions: [
      {
        id: 'itervox-abc123',
        runId: '',
        owner: 'itervox',
        kind: 'tmux',
        identifier: '',
        title: 'Fix the flaky test',
        status: 'running',
        controllable: true,
        messages: [],
      },
      {
        id: 'sess-1',
        runId: 'run-1',
        owner: 'itervox',
        identifier: 'ENG-1',
        status: 'running',
        controllable: false,
        messages: [],
      },
    ],
  };
}

let calls: Call[] = [];

function stubNetwork(
  body = sessionsBody(),
  grant: 'read' | 'write' = 'read',
  prefix = '',
  state: Record<string, unknown> = {},
) {
  calls = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init: RequestInit = {}) => {
      // Under the hub every API URL carries its /hub/p/{project} base.
      const input = url.startsWith(`${prefix}/api/`) ? url.slice(prefix.length) : `!${url}`;
      const headers = new Headers(init.headers);
      calls.push({
        url,
        method: init.method ?? 'GET',
        auth: headers.get('Authorization'),
        body: typeof init.body === 'string' ? init.body : '',
      });
      const json = (status: number, data: unknown) =>
        Promise.resolve(
          new Response(JSON.stringify(data), {
            status,
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      if (input.startsWith('/api/v1/sessions/itervox-abc123/terminal/ticket')) {
        return json(201, {
          ticket: 'itvt_one',
          mode: grant,
          expiresAt: '2026-11-20T10:00:30Z',
          url: '/api/v1/sessions/itervox-abc123/terminal?ticket=itvt_one',
        });
      }
      if (input.startsWith('/api/v1/sessions')) return json(200, body);
      if (input.startsWith('/api/v1/state'))
        return json(200, { ...quickstartScenario.snapshot, projectName: 'app', ...state });
      return json(404, {});
    }),
  );
  vi.stubGlobal('WebSocket', FakeSocket);
}

function at(search: string, prefix = '') {
  window.history.replaceState(null, '', `${prefix}${TERMINAL_WINDOW_PATH}${search}`);
  configureApiContext(`${prefix}${TERMINAL_WINDOW_PATH}`);
}

function renderPage(ui: ReactElement = <TerminalWindow />) {
  return render(<QueryClientProvider client={makeTestQueryClient()}>{ui}</QueryClientProvider>);
}

beforeEach(() => {
  resetAllStores();
  useItervoxStore.setState({ snapshot: null });
  useTokenStore.getState().setToken(TOKEN, false);
  FakeSocket.instances = [];
  m.terms = [];
});

afterEach(() => {
  vi.unstubAllGlobals();
  window.history.replaceState(null, '', '/');
  configureApiContext('/');
});

describe('terminal window path', () => {
  it('is the daemon dashboard path, or a project through the hub, never a hub page', () => {
    configureApiContext('/');
    expect(isTerminalWindowPath('/sessions/terminal')).toBe(true);
    expect(isTerminalWindowPath('/sessions/terminal/')).toBe(true);
    expect(isTerminalWindowPath('/sessions')).toBe(false);
    expect(isTerminalWindowPath('/sessions/terminalx')).toBe(false);
    // #334: a project's dashboard through the hub (read-only terminal).
    configureApiContext('/hub/p/app/sessions/terminal');
    expect(isTerminalWindowPath('/hub/p/app/sessions/terminal')).toBe(true);
    expect(isTerminalWindowPath('/hub/p/app/sessions/terminal/')).toBe(true);
    expect(isTerminalWindowPath('/hub/p/app/sessions')).toBe(false);
    expect(isTerminalWindowPath('/sessions/terminal')).toBe(false);
    expect(isTerminalWindowPath('/hub/p/other/sessions/terminal')).toBe(false);
    // The hub's own pages never are.
    configureApiContext('/hub/');
    expect(isTerminalWindowPath('/hub/')).toBe(false);
    expect(isTerminalWindowPath('/sessions/terminal')).toBe(false);
  });

  it('reads the session from ?id=', () => {
    expect(terminalWindowSession('?id=itervox-abc123')).toBe('itervox-abc123');
    expect(terminalWindowSession('?id=%20a%2Fb%20')).toBe('a/b');
    expect(terminalWindowSession('')).toBe('');
  });
});

describe('TerminalWindow', () => {
  it('renders only the live terminal, authed, over the ticketed socket', async () => {
    stubNetwork();
    at('?id=itervox-abc123');
    renderPage();
    expect(await screen.findByTestId('tmux-terminal')).toBeInTheDocument();
    await waitFor(() => {
      expect(FakeSocket.instances).toHaveLength(1);
    });
    // Only the terminal: no navigation, no board.
    expect(screen.queryByRole('navigation')).toBeNull();
    expect(screen.getByTestId('terminal-window-session')).toHaveTextContent('Fix the flaky test');
    expect(screen.getByTestId('terminal-window-project')).toHaveTextContent('app');
    expect(document.title).toBe('Fix the flaky test — app — Live terminal');
    // Every request carries the held token in a header (authedFetch) ...
    expect(calls.length).toBeGreaterThanOrEqual(3);
    for (const c of calls) expect(c.auth).toBe(`Bearer ${TOKEN}`);
    const ticket = calls.find((c) => c.url.includes('/terminal/ticket'));
    expect(ticket?.method).toBe('POST');
    expect(JSON.parse(ticket?.body ?? '{}')).toEqual({ mode: 'read' });
    // ... and the socket carries only the single-use ticket, never the token.
    const ws = new URL(FakeSocket.instances[0].url);
    expect(ws.protocol).toBe('ws:');
    expect(ws.pathname).toBe('/api/v1/sessions/itervox-abc123/terminal');
    expect(ws.searchParams.get('ticket')).toBe('itvt_one');
    expect(ws.searchParams.get('cols')).toBe('120');
    expect(FakeSocket.instances[0].url).not.toContain(TOKEN);
    expect(window.location.href).not.toContain(TOKEN);
    act(() => {
      FakeSocket.instances[0].onopen?.();
    });
    expect(screen.getByTestId('tmux-terminal-status')).toHaveTextContent('Live');
    expect(screen.getByTestId('tmux-terminal-mode')).toHaveTextContent('read-only');
    expect(m.terms[0].readOnly).toBe(true);
    // No snapshot fallback in the window.
    expect(screen.queryByRole('button', { name: 'Snapshot view' })).toBeNull();
  });

  it('asks for read-write as admin with terminal_write, and shows what was granted', async () => {
    stubNetwork(sessionsBody({ terminalWrite: true }), 'read');
    at('?id=itervox-abc123');
    renderPage();
    await waitFor(() => {
      expect(FakeSocket.instances).toHaveLength(1);
    });
    const ticket = calls.find((c) => c.url.includes('/terminal/ticket'));
    expect(JSON.parse(ticket?.body ?? '{}')).toEqual({ mode: 'write' });
    // The daemon granted read-only: the mode shown follows its answer.
    expect(screen.getByTestId('tmux-terminal-mode')).toHaveTextContent('read-only');
    expect(screen.getByRole('application')).toHaveAccessibleName('Session terminal (read-only)');
  });

  it('shows the close code and reason the daemon closed with', async () => {
    stubNetwork();
    at('?id=itervox-abc123');
    const { unmount } = renderPage();
    await waitFor(() => {
      expect(FakeSocket.instances).toHaveLength(1);
    });
    act(() => {
      FakeSocket.instances[0].onopen?.();
      FakeSocket.instances[0].onclose?.({ code: 4403, reason: 'token revoked' });
    });
    expect(screen.getByTestId('tmux-terminal-status')).toHaveTextContent('Detached');
    expect(screen.getByTestId('tmux-terminal-close')).toHaveTextContent(
      'closed 4403: token revoked',
    );
    expect(screen.getByRole('button', { name: 'Reconnect' })).toBeInTheDocument();
    // Closing the window (unmount) detaches: the socket closes, nothing else.
    unmount();
    expect(m.terms[0].disposed).toBe(true);
  });

  it('closing the window closes the socket', async () => {
    stubNetwork();
    at('?id=itervox-abc123');
    const { unmount } = renderPage();
    await waitFor(() => {
      expect(FakeSocket.instances).toHaveLength(1);
    });
    unmount();
    expect(FakeSocket.instances[0].closed).toBe(true);
  });

  it.each([
    ['', 'No session chosen'],
    ['?id=gone', 'No session gone'],
    ['?id=sess-1', 'This session has no live terminal'],
  ])('explains why %s has no terminal', async (search, text) => {
    stubNetwork();
    at(search);
    renderPage();
    expect(await screen.findByText(text)).toBeInTheDocument();
    expect(screen.queryByTestId('tmux-terminal')).toBeNull();
    expect(FakeSocket.instances).toHaveLength(0);
  });

  it('explains a daemon without the live terminal', async () => {
    stubNetwork(sessionsBody({ terminal: false }));
    at('?id=itervox-abc123');
    renderPage();
    expect(await screen.findByText('No live terminal for this daemon')).toBeInTheDocument();
    expect(screen.getByText(/tmux is missing, it runs on Windows/)).toBeInTheDocument();
  });

  // #334: the desktop app opens the same page from the hub's dashboard of a
  // project read through the hub. Every request goes to the hub's proxied
  // API with the held (hub) token, the ticket asks for read only — even
  // for an admin token on a daemon allowing read-write — and the socket is
  // the hub's route for the brokered ticket. No keystroke is ever sent.
  it('through the hub: read-only, the hub API and the hub socket route', async () => {
    const prefix = '/hub/p/app';
    stubNetwork(sessionsBody({ terminalWrite: true }), 'read', prefix, { callerScope: 'admin' });
    at('?id=itervox-abc123', prefix);
    renderPage();
    await waitFor(() => {
      expect(FakeSocket.instances).toHaveLength(1);
    });
    expect(calls.length).toBeGreaterThanOrEqual(3);
    for (const c of calls) {
      expect(c.url.startsWith(`${prefix}/api/v1/`)).toBe(true);
      expect(c.auth).toBe(`Bearer ${TOKEN}`);
    }
    const ticket = calls.find((c) => c.url.includes('/terminal/ticket'));
    expect(ticket?.url).toBe(`${prefix}/api/v1/sessions/itervox-abc123/terminal/ticket`);
    expect(JSON.parse(ticket?.body ?? '{}')).toEqual({ mode: 'read' });
    const ws = new URL(FakeSocket.instances[0].url);
    expect(ws.pathname).toBe(`${prefix}/api/v1/sessions/itervox-abc123/terminal`);
    expect(ws.searchParams.get('ticket')).toBe('itvt_one');
    expect(FakeSocket.instances[0].url).not.toContain(TOKEN);
    act(() => {
      FakeSocket.instances[0].onopen?.();
    });
    expect(screen.getByTestId('tmux-terminal-mode')).toHaveTextContent('read-only');
    expect(m.terms[0].readOnly).toBe(true);
    expect(screen.queryByRole('navigation')).toBeNull();
  });

  it('explains a token below operator', async () => {
    stubNetwork();
    at('?id=itervox-abc123');
    vi.stubGlobal(
      'fetch',
      vi.fn((input: string) => {
        const data = input.startsWith('/api/v1/state')
          ? { ...quickstartScenario.snapshot, callerScope: 'viewer' }
          : sessionsBody();
        return Promise.resolve(new Response(JSON.stringify(data), { status: 200 }));
      }),
    );
    renderPage();
    expect(await screen.findByText(/needs the operator scope/)).toBeInTheDocument();
  });
});
