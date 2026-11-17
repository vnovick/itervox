import { lazy, Suspense } from 'react';
import { useCallerScope } from '../../hooks/useCallerScope';
import { Button } from '../../components/ui/button';
import { useUIStore } from '../../store/uiStore';
import type { TmuxStatus } from '../../types/schemas';
import { terminalModeFor } from './terminalMode';
import { TmuxScreen } from './TmuxSessionView';

// #298 — the screen of a controllable tmux session: the live terminal when
// the caller may have one, else (and on request) the polled snapshot.

const TmuxTerminal = lazy(() => import('./TmuxTerminal'));

export function TmuxLivePanel({ id, tmux }: { id: string; tmux: TmuxStatus | undefined }) {
  const scope = useCallerScope();
  const live = useUIStore((s) => s.sessionsLiveTerminal);
  const setLive = useUIStore((s) => s.setSessionsLiveTerminal);
  const mode = terminalModeFor(scope, tmux);
  if (!mode) return <TmuxScreen id={id} />;
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
