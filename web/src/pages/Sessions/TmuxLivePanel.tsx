import { lazy, Suspense, useState } from 'react';
import { UnauthorizedError } from '../../auth/UnauthorizedError';
import { scopeAllows } from '../../auth/scope';
import { useCallerScope } from '../../hooks/useCallerScope';
import { Button } from '../../components/ui/button';
import { fetchCallerScope } from '../../queries/sessionsTerminal';
import { useToastStore } from '../../store/toastStore';
import { useUIStore } from '../../store/uiStore';
import type { TmuxStatus } from '../../types/schemas';
import { terminalModeFor } from './terminalMode';
import { TmuxScreen } from './TmuxSessionView';

// #298 — the screen of a controllable tmux session: the live terminal when
// the caller may have one, else (and on request) the polled snapshot.
//
// The caller scope comes from the snapshot stream and is the one it
// connected with, so after the daemon refuses a read-write terminal for
// scope (a `scope_insufficient` close or a 403 on the ticket) the panel
// holds the terminal read-only: Reconnect asks for mode "read", and
// read-write is offered again only once a fresh read of the caller's
// scope (fetchCallerScope) says admin.

const TmuxTerminal = lazy(() => import('./TmuxTerminal'));

export function TmuxLivePanel({ id, tmux }: { id: string; tmux: TmuxStatus | undefined }) {
  const scope = useCallerScope();
  const live = useUIStore((s) => s.sessionsLiveTerminal);
  const setLive = useUIStore((s) => s.setSessionsLiveTerminal);
  const [writeHeld, setWriteHeld] = useState(false);
  const allowed = terminalModeFor(scope, tmux);
  if (!allowed) return <TmuxScreen id={id} />;
  const held = writeHeld && allowed === 'write';
  const mode = held ? 'read' : allowed;
  const retryWrite = () => {
    fetchCallerScope()
      .then((fresh) => {
        if (scopeAllows(fresh, 'admin')) setWriteHeld(false);
        else
          useToastStore
            .getState()
            .addToast('Your access token still does not allow a read-write terminal.', 'info');
      })
      .catch((err: unknown) => {
        if (err instanceof UnauthorizedError) return; // AuthGate handles 401
        useToastStore
          .getState()
          .addToast(err instanceof Error ? err.message : 'Could not check your access.', 'error');
      });
  };
  if (live) {
    return (
      <Suspense
        fallback={
          <p data-testid="tmux-terminal-loading" className="text-theme-muted px-4 py-3.5 text-sm">
            Loading the terminal…
          </p>
        }
      >
        <TmuxTerminal
          key={`${id}-${mode}`}
          id={id}
          mode={mode}
          onSnapshot={() => {
            setLive(false);
          }}
          onWriteLost={() => {
            setWriteHeld(true);
          }}
          onRetryWrite={held ? retryWrite : undefined}
        />
      </Suspense>
    );
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex px-4 pt-3">
        <Button
          size="sm"
          variant="glass"
          pill
          className="ml-auto"
          onClick={() => {
            setLive(true);
          }}
        >
          Live terminal
        </Button>
      </div>
      <TmuxScreen id={id} />
    </div>
  );
}
