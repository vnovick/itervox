import { useRef, useState } from 'react';
import { Button } from '../../components/ui/button';
import type { TerminalMode } from '../../queries/sessionsTerminal';
import { useTmuxTerminal, type TerminalStatus } from './useTmuxTerminal';

// #298 — the live terminal of a tmux session: a real terminal (colours,
// cursor, every key, resize) over the daemon's `tmux attach-session`.
// Loaded lazily by TmuxLivePanel, so the renderer is fetched only when a
// live terminal opens. Read-only for operators (and for admins unless
// sessions.terminal_write is on); the snapshot view stays one click away.

const STATUS_TEXT: Record<TerminalStatus, string> = {
  connecting: 'Connecting…',
  live: 'Live',
  closed: 'Detached',
  error: 'Not attached',
};

export interface TmuxTerminalProps {
  id: string;
  mode: TerminalMode;
  /** Switch to the polled snapshot view. */
  onSnapshot: () => void;
}

export default function TmuxTerminal({ id, mode, onSnapshot }: TmuxTerminalProps) {
  const host = useRef<HTMLDivElement>(null);
  const [attempt, setAttempt] = useState(0);
  const { status, error } = useTmuxTerminal(host, id, mode, attempt);
  return (
    <div className="flex min-h-0 flex-1 flex-col" data-testid="tmux-terminal">
      <div className="flex flex-wrap items-center gap-2 px-4 pt-3 text-xs">
        <span data-testid="tmux-terminal-status" className="text-theme-muted font-mono">
          {STATUS_TEXT[status]}
        </span>
        <span
          data-testid="tmux-terminal-mode"
          className={
            mode === 'write' ? 'text-theme-warning-text font-mono' : 'text-theme-muted font-mono'
          }
        >
          {mode === 'write' ? 'typing goes to the session' : 'read-only'}
        </span>
        {(status === 'closed' || status === 'error') && (
          <Button
            size="sm"
            variant="glass"
            pill
            onClick={() => {
              setAttempt((n) => n + 1);
            }}
          >
            Reconnect
          </Button>
        )}
        <Button size="sm" variant="glass" pill className="ml-auto" onClick={onSnapshot}>
          Snapshot view
        </Button>
      </div>
      {status === 'error' && (
        <p data-testid="tmux-terminal-error" className="text-theme-danger-text px-4 pt-2 text-sm">
          {error} The snapshot view still works.
        </p>
      )}
      {status === 'closed' && error && (
        <p
          data-testid="tmux-terminal-closed-reason"
          role="status"
          className="text-theme-warning-text px-4 pt-2 text-sm"
        >
          {error}
        </p>
      )}
      <div
        ref={host}
        role="application"
        aria-label={mode === 'write' ? 'Session terminal' : 'Session terminal (read-only)'}
        className="text-theme-text min-h-[240px] flex-1 overflow-hidden px-3 py-2"
      />
    </div>
  );
}
