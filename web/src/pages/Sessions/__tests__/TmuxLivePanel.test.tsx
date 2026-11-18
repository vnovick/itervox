import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { configureApiContext } from '../../../auth/apiBase';
import type { Scope } from '../../../auth/scope';
import { useUIStore } from '../../../store/uiStore';
import type { TmuxStatus } from '../../../types/schemas';
import { TmuxLivePanel } from '../TmuxLivePanel';
import { terminalModeFor } from '../terminalMode';

// #298 — which screen a controllable tmux session gets: the live terminal
// (read-only, or read-write for admin with sessions.terminal_write) or the
// polled snapshot (viewers, older or Windows daemons, no tmux, the hub,
// and on request).

const m = vi.hoisted(() => ({ scope: 'operator' as Scope }));

vi.mock('../../../hooks/useCallerScope', () => ({ useCallerScope: () => m.scope }));

vi.mock('../TmuxTerminal', () => ({
  default: ({ mode, onSnapshot }: { mode: string; onSnapshot: () => void }) => (
    <div data-testid="live-terminal" data-mode={mode}>
      <button type="button" onClick={onSnapshot}>
        Snapshot view
      </button>
    </div>
  ),
}));

vi.mock('../../../queries/sessionsTmux', () => ({
  useSessionScreen: (id: string) => ({ data: { text: `screen of ${id}` }, isError: false }),
}));

const tmux = (over: Partial<TmuxStatus> = {}): TmuxStatus => ({
  available: true,
  installHint: '',
  control: false,
  terminal: true,
  terminalWrite: false,
  ...over,
});

beforeEach(() => {
  m.scope = 'operator';
  useUIStore.setState({ sessionsLiveTerminal: true });
});
afterEach(() => {
  configureApiContext('/');
});

describe('terminalModeFor', () => {
  it('follows scope, platform, tmux, the opt-in and the hub', () => {
    expect(terminalModeFor('viewer', tmux())).toBeNull();
    expect(terminalModeFor('operator', tmux())).toBe('read');
    expect(terminalModeFor('operator', tmux({ terminalWrite: true }))).toBe('read');
    expect(terminalModeFor('admin', tmux())).toBe('read');
    expect(terminalModeFor('admin', tmux({ terminalWrite: true }))).toBe('write');
    expect(terminalModeFor('admin', tmux({ terminal: false }))).toBeNull();
    expect(terminalModeFor('admin', tmux({ terminal: undefined }))).toBeNull();
    expect(terminalModeFor('admin', tmux({ available: false }))).toBeNull();
    expect(terminalModeFor('admin', undefined)).toBeNull();
    configureApiContext('/hub/p/alpha/sessions');
    expect(terminalModeFor('admin', tmux({ terminalWrite: true }))).toBeNull();
  });
});

describe('TmuxLivePanel', () => {
  it('shows a viewer the snapshot only', () => {
    m.scope = 'viewer';
    render(<TmuxLivePanel id="itervox-a" tmux={tmux()} />);
    expect(screen.getByTestId('tmux-screen')).toHaveTextContent('screen of itervox-a');
    expect(screen.queryByTestId('live-terminal')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Live terminal' })).toBeNull();
  });

  it('opens the live terminal and switches to the snapshot and back', async () => {
    m.scope = 'admin';
    render(<TmuxLivePanel id="itervox-a" tmux={tmux({ terminalWrite: true })} />);
    const live = await screen.findByTestId('live-terminal');
    expect(live).toHaveAttribute('data-mode', 'write');
    await userEvent.click(screen.getByRole('button', { name: 'Snapshot view' }));
    expect(screen.getByTestId('tmux-screen')).toBeInTheDocument();
    expect(useUIStore.getState().sessionsLiveTerminal).toBe(false);
    await userEvent.click(screen.getByRole('button', { name: 'Live terminal' }));
    expect(await screen.findByTestId('live-terminal')).toHaveAttribute('data-mode', 'write');
  });

  it('keeps the snapshot on a daemon without the live terminal', () => {
    render(<TmuxLivePanel id="itervox-a" tmux={tmux({ terminal: false })} />);
    expect(screen.getByTestId('tmux-screen')).toBeInTheDocument();
    expect(screen.queryByTestId('live-terminal')).toBeNull();
  });
});
