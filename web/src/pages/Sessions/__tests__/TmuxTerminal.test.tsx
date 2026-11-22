import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import TmuxTerminal from '../TmuxTerminal';
import type { TerminalSocketHandlers } from '../../../auth/terminalSocket';
import type * as TerminalSocketModule from '../../../auth/terminalSocket';
import { ApiError } from '../../../auth/apiRequest';
import { UnauthorizedError } from '../../../auth/UnauthorizedError';

// #298 — the live terminal view: ticket → socket → renderer, keys out only
// for a read-write attach, status, reconnect, and the snapshot fallback.

const m = vi.hoisted(() => ({
  ticket: vi.fn(),
  sockets: [] as {
    path: string;
    handlers: TerminalSocketHandlers;
    sent: Uint8Array[];
    closed: boolean;
  }[],
  terms: [] as {
    opened: boolean;
    disposed: boolean;
    written: unknown[];
    data: ((d: string) => void) | null;
    resize: ((s: { cols: number; rows: number }) => void) | null;
    readOnly: boolean;
  }[],
}));

vi.mock('../../../queries/sessionsTerminal', () => ({ requestTerminalTicket: m.ticket }));

vi.mock('../../../auth/terminalSocket', async (importOriginal) => ({
  ...(await importOriginal<typeof TerminalSocketModule>()),
  openTerminalSocket: (path: string, _size: unknown, handlers: TerminalSocketHandlers) => {
    const s = { path, handlers, sent: [] as Uint8Array[], closed: false };
    m.sockets.push(s);
    return {
      send: (f: Uint8Array) => s.sent.push(f),
      close: () => {
        s.closed = true;
      },
    };
  },
}));

vi.mock('../createTerminal', () => ({
  createTerminal: ({ readOnly }: { readOnly: boolean }) => {
    const t = {
      opened: false,
      disposed: false,
      written: [] as unknown[],
      data: null as ((d: string) => void) | null,
      resize: null as ((s: { cols: number; rows: number }) => void) | null,
      readOnly,
    };
    m.terms.push(t);
    return {
      open: () => {
        t.opened = true;
      },
      write: (d: unknown) => t.written.push(d),
      onData: (cb: (d: string) => void) => {
        t.data = cb;
        return () => {
          t.data = null;
        };
      },
      onResize: (cb: (s: { cols: number; rows: number }) => void) => {
        t.resize = cb;
        return () => {
          t.resize = null;
        };
      },
      fit: vi.fn(),
      size: () => ({ cols: 100, rows: 30 }),
      focus: vi.fn(),
      dispose: () => {
        t.disposed = true;
      },
    };
  },
}));

beforeEach(() => {
  m.ticket.mockReset();
  m.sockets = [];
  m.terms = [];
});

const ticket = (mode: 'read' | 'write') => ({
  ticket: 'itvt_x',
  mode,
  expiresAt: '2026-11-17T10:00:30Z',
  url: '/api/v1/sessions/itervox-a/terminal?ticket=itvt_x',
});

describe('TmuxTerminal', () => {
  it('attaches read-write: output is drawn, keys and resizes are sent', async () => {
    m.ticket.mockResolvedValue(ticket('write'));
    const { unmount } = render(<TmuxTerminal id="itervox-a" mode="write" onSnapshot={vi.fn()} />);
    expect(screen.getByTestId('tmux-terminal-status')).toHaveTextContent('Connecting…');
    await waitFor(() => {
      expect(m.sockets).toHaveLength(1);
    });
    expect(m.ticket).toHaveBeenCalledWith('itervox-a', 'write');
    expect(m.sockets[0].path).toBe(ticket('write').url);
    act(() => {
      m.sockets[0].handlers.onOpen?.();
      m.sockets[0].handlers.onData(new Uint8Array([104, 105]));
    });
    expect(screen.getByTestId('tmux-terminal-status')).toHaveTextContent('Live');
    expect(screen.getByTestId('tmux-terminal-mode')).toHaveTextContent(
      'typing goes to the session',
    );
    const term = m.terms[0];
    expect(term.readOnly).toBe(false);
    expect(term.written).toEqual([new Uint8Array([104, 105])]);
    term.data?.('ls\r');
    term.resize?.({ cols: 90, rows: 20 });
    expect(m.sockets[0].sent.map((f) => f[0])).toEqual([0x00, 0x01]);
    unmount();
    expect(m.sockets[0].closed).toBe(true);
    expect(term.disposed).toBe(true);
  });

  it('attaches read-only: nothing typed is sent', async () => {
    m.ticket.mockResolvedValue(ticket('read'));
    render(<TmuxTerminal id="itervox-a" mode="read" onSnapshot={vi.fn()} />);
    await waitFor(() => {
      expect(m.sockets).toHaveLength(1);
    });
    expect(screen.getByTestId('tmux-terminal-mode')).toHaveTextContent('read-only');
    expect(m.terms[0].readOnly).toBe(true);
    m.terms[0].data?.('rm -rf /\r');
    expect(m.sockets[0].sent).toHaveLength(0);
    expect(screen.getByRole('application')).toHaveAccessibleName('Session terminal (read-only)');
  });

  it('follows the mode the daemon granted: write asked, read granted', async () => {
    m.ticket.mockResolvedValue(ticket('read'));
    render(<TmuxTerminal id="itervox-a" mode="write" />);
    await waitFor(() => {
      expect(m.sockets).toHaveLength(1);
    });
    expect(screen.getByTestId('tmux-terminal-mode')).toHaveTextContent('read-only');
    m.terms[0].data?.('ls\r');
    expect(m.sockets[0].sent).toHaveLength(0);
    // Without onSnapshot (the desktop terminal window) there is no fallback.
    expect(screen.queryByRole('button', { name: 'Snapshot view' })).toBeNull();
    act(() => {
      m.sockets[0].handlers.onClose?.({ code: 1006, reason: '' });
    });
    expect(screen.getByTestId('tmux-terminal-close')).toHaveTextContent('closed 1006');
  });

  it('shows a refused ticket and falls back to the snapshot view', async () => {
    m.ticket.mockRejectedValue(new Error('this session is not running in a tmux pane'));
    const onSnapshot = vi.fn();
    render(<TmuxTerminal id="itervox-a" mode="read" onSnapshot={onSnapshot} />);
    expect(await screen.findByTestId('tmux-terminal-error')).toHaveTextContent(
      'this session is not running in a tmux pane',
    );
    expect(screen.getByTestId('tmux-terminal-status')).toHaveTextContent('Not attached');
    await userEvent.click(screen.getByRole('button', { name: 'Snapshot view' }));
    expect(onSnapshot).toHaveBeenCalled();
  });

  it('leaves a 401 to the AuthGate', async () => {
    m.ticket.mockRejectedValue(new UnauthorizedError());
    render(<TmuxTerminal id="itervox-a" mode="read" onSnapshot={vi.fn()} />);
    await waitFor(() => {
      expect(m.ticket).toHaveBeenCalled();
    });
    expect(screen.queryByTestId('tmux-terminal-error')).toBeNull();
  });

  it('reconnects with a new ticket after a detach', async () => {
    m.ticket.mockResolvedValue(ticket('read'));
    render(<TmuxTerminal id="itervox-a" mode="read" onSnapshot={vi.fn()} />);
    await waitFor(() => {
      expect(m.sockets).toHaveLength(1);
    });
    act(() => {
      m.sockets[0].handlers.onClose?.({ code: 1000, reason: '' });
    });
    expect(screen.getByTestId('tmux-terminal-status')).toHaveTextContent('Detached');
    await userEvent.click(screen.getByRole('button', { name: 'Reconnect' }));
    await waitFor(() => {
      expect(m.sockets).toHaveLength(2);
    });
    expect(m.ticket).toHaveBeenCalledTimes(2);
    expect(m.terms[0].disposed).toBe(true);
    expect(screen.getByTestId('tmux-terminal-status')).toHaveTextContent('Connecting…');
    expect(screen.queryByTestId('tmux-terminal-closed-reason')).toBeNull();
  });

  it('says why the daemon closed the terminal when access changed', async () => {
    m.ticket.mockResolvedValue(ticket('write'));
    render(<TmuxTerminal id="itervox-a" mode="write" onSnapshot={vi.fn()} />);
    await waitFor(() => {
      expect(m.sockets).toHaveLength(1);
    });
    act(() => {
      m.sockets[0].handlers.onOpen?.();
      m.sockets[0].handlers.onClose?.({ code: 4403, reason: 'scope_insufficient' });
    });
    expect(screen.getByTestId('tmux-terminal-status')).toHaveTextContent('Detached');
    expect(screen.getByRole('status')).toHaveTextContent(
      'Your access token no longer has the scope this terminal needs',
    );
    // A reconnect starts over without the old reason.
    await userEvent.click(screen.getByRole('button', { name: 'Reconnect' }));
    await waitFor(() => {
      expect(m.sockets).toHaveLength(2);
    });
    expect(screen.queryByTestId('tmux-terminal-closed-reason')).toBeNull();
    act(() => {
      m.sockets[1].handlers.onClose?.({ code: 4403, reason: 'token_revoked' });
    });
    expect(screen.getByTestId('tmux-terminal-closed-reason')).toHaveTextContent(
      'Your access token was revoked',
    );
  });

  it('shows no reason for an ordinary detach', async () => {
    m.ticket.mockResolvedValue(ticket('read'));
    render(<TmuxTerminal id="itervox-a" mode="read" onSnapshot={vi.fn()} />);
    await waitFor(() => {
      expect(m.sockets).toHaveLength(1);
    });
    act(() => {
      m.sockets[0].handlers.onClose?.({ code: 1006, reason: '' });
    });
    expect(screen.getByTestId('tmux-terminal-status')).toHaveTextContent('Detached');
    expect(screen.queryByTestId('tmux-terminal-closed-reason')).toBeNull();
  });

  it('hands a scope-lost read-write terminal back for a read-only reconnect', async () => {
    m.ticket.mockResolvedValue(ticket('write'));
    const onWriteLost = vi.fn();
    render(
      <TmuxTerminal id="itervox-a" mode="write" onSnapshot={vi.fn()} onWriteLost={onWriteLost} />,
    );
    await waitFor(() => {
      expect(m.sockets).toHaveLength(1);
    });
    act(() => {
      m.sockets[0].handlers.onOpen?.();
      m.sockets[0].handlers.onClose?.({ code: 4403, reason: 'scope_insufficient' });
    });
    await userEvent.click(screen.getByRole('button', { name: 'Reconnect' }));
    expect(onWriteLost).toHaveBeenCalledTimes(1);
    // No second read-write ticket: the panel reconnects read-only.
    expect(m.ticket).toHaveBeenCalledTimes(1);
  });

  it('treats a 403 insufficient_scope on a write ticket as a scope loss', async () => {
    m.ticket.mockRejectedValue(
      new ApiError('this action needs the admin scope', 403, 'insufficient_scope'),
    );
    const onWriteLost = vi.fn();
    render(
      <TmuxTerminal id="itervox-a" mode="write" onSnapshot={vi.fn()} onWriteLost={onWriteLost} />,
    );
    expect(await screen.findByTestId('tmux-terminal-error')).toHaveTextContent(
      'no longer allows a read-write terminal',
    );
    await userEvent.click(screen.getByRole('button', { name: 'Reconnect' }));
    expect(onWriteLost).toHaveBeenCalledTimes(1);
    expect(m.ticket).toHaveBeenCalledTimes(1);
  });

  it('reconnects in place after an ordinary detach even with onWriteLost', async () => {
    m.ticket.mockResolvedValue(ticket('write'));
    const onWriteLost = vi.fn();
    render(
      <TmuxTerminal id="itervox-a" mode="write" onSnapshot={vi.fn()} onWriteLost={onWriteLost} />,
    );
    await waitFor(() => {
      expect(m.sockets).toHaveLength(1);
    });
    act(() => {
      m.sockets[0].handlers.onClose?.({ code: 4403, reason: 'token_revoked' });
    });
    await userEvent.click(screen.getByRole('button', { name: 'Reconnect' }));
    await waitFor(() => {
      expect(m.sockets).toHaveLength(2);
    });
    expect(onWriteLost).not.toHaveBeenCalled();
    expect(m.ticket).toHaveBeenLastCalledWith('itervox-a', 'write');
  });

  it('offers a read-write check on a held read-only terminal', async () => {
    m.ticket.mockResolvedValue(ticket('read'));
    const onRetryWrite = vi.fn();
    render(
      <TmuxTerminal id="itervox-a" mode="read" onSnapshot={vi.fn()} onRetryWrite={onRetryWrite} />,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Check read-write access' }));
    expect(onRetryWrite).toHaveBeenCalled();
  });
});
