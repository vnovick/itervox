import { lazy, Suspense, useEffect, type ReactNode } from 'react';
import { useCallerScope } from '../../hooks/useCallerScope';
import { sessionLabel, useSessions } from '../../queries/sessions';
import { useItervoxStore } from '../../store/itervoxStore';
import { ConsoleSurface } from '../../components/ui/Console/ConsoleSurface';
import { terminalModeFor } from '../Sessions/terminalMode';
import { terminalWindowSession } from './terminalWindowPath';

// #278 phase 2 — the desktop app's terminal window: the live terminal
// (#298) of ONE tmux session, filling the window, and nothing else. The app
// loads it from the daemon's own origin (a local daemon's address or a
// remote daemon's tunnel end), with the token handed to sessionStorage by
// an initialization script — never in the URL. The terminal is the
// Sessions page's own (TmuxTerminal: ticket through authedFetch, socket
// through auth/terminalSocket), loaded lazily so the renderer stays in its
// chunk. Closing the window closes the socket: the daemon detaches and the
// tmux session keeps running.

const TmuxTerminal = lazy(() => import('../Sessions/TmuxTerminal'));

function Notice({ title, detail }: { title: string; detail?: string }) {
  return (
    <div data-testid="terminal-window-notice" className="px-4 py-3.5 text-sm">
      <p className="text-theme-text font-semibold">{title}</p>
      {detail && <p className="text-theme-muted mt-1">{detail}</p>}
    </div>
  );
}

export default function TerminalWindow() {
  const id = terminalWindowSession();
  const refreshSnapshot = useItervoxStore((s) => s.refreshSnapshot);
  const snapshotLoaded = useItervoxStore((s) => s.snapshot !== null);
  const project = useItervoxStore((s) => s.snapshot?.projectName ?? '');
  const scope = useCallerScope();
  const { sessions, tmux, isLoading, isError } = useSessions({ poll: false });

  // One read of the state for the caller's scope; no event stream here.
  useEffect(() => {
    void refreshSnapshot();
  }, [refreshSnapshot]);

  const session = id ? sessions.find((s) => s.id === id) : undefined;
  const label = session ? sessionLabel(session) : id;

  useEffect(() => {
    document.title = [label, project, 'Live terminal'].filter(Boolean).join(' — ');
  }, [label, project]);

  let body: ReactNode;
  if (!id) {
    body = <Notice title="No session chosen" detail="Open a terminal from the Sessions window." />;
  } else if (isError) {
    body = <Notice title="The daemon's sessions could not be read" />;
  } else if (isLoading || !snapshotLoaded) {
    body = <Notice title="Connecting…" />;
  } else if (!session) {
    body = <Notice title={`No session ${id}`} detail="It may have ended." />;
  } else if (!session.controllable) {
    body = (
      <Notice
        title="This session has no live terminal"
        detail="It is not running in a tmux pane itervox may control (see sessions.tmux_control)."
      />
    );
  } else {
    const mode = terminalModeFor(scope, tmux);
    body = mode ? (
      <Suspense fallback={<Notice title="Loading the terminal…" />}>
        <TmuxTerminal key={`${id}-${mode}`} id={id} mode={mode} />
      </Suspense>
    ) : (
      <Notice
        title="No live terminal for this daemon"
        detail={
          tmux?.terminal
            ? 'This token cannot open one (it needs the operator scope).'
            : 'The daemon has no live terminal: tmux is missing, it runs on Windows, or it predates it.'
        }
      />
    );
  }

  return (
    <div data-testid="terminal-window" className="flex h-screen flex-col p-2">
      <header className="flex items-baseline gap-2 px-2 pb-2 text-sm">
        <span data-testid="terminal-window-session" className="text-theme-text font-semibold">
          {label || 'Terminal'}
        </span>
        {project && (
          <span data-testid="terminal-window-project" className="text-theme-muted">
            {project}
          </span>
        )}
      </header>
      <ConsoleSurface className="flex min-h-0 flex-1 flex-col rounded-[14px]">
        {body}
      </ConsoleSurface>
    </div>
  );
}
