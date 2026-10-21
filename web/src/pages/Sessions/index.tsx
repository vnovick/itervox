import { useMemo } from 'react';
import { useSearchParams } from 'react-router';
import PageMeta from '../../components/common/PageMeta';
import { EmptyState } from '../../components/ui/EmptyState';
import { Terminal } from '../../components/ui/Terminal/Terminal';
import { useIssueLogs } from '../../queries/logs';
import { Button } from '../../components/ui/button';
import { sessionKey, sessionLabel, useSessions } from '../../queries/sessions';
import { useItervoxStore } from '../../store/itervoxStore';
import type { SessionRow, TmuxStatus } from '../../types/schemas';
import { issueLogToTerminal } from '../../utils/logFormatting';
import { NewSessionForm } from './NewSessionForm';
import { SessionComposer } from './SessionComposer';
import { SessionTabs } from './SessionTabs';
import { SessionTranscriptView } from './SessionTranscript';
import { TMUX_MISSING, TmuxMissingBanner } from './TmuxNotice';
import { TmuxControls, TmuxScreen } from './TmuxSessionView';

// Sessions panel (#178): one tab per agent session of this daemon, sessions
// waiting for input first, sessions started outside itervox last (greyed,
// read-only unless they run in tmux and sessions.tmux_control is on).
// `?id=` selects a tab (the Mac app's Sessions window opens this route with
// it); `?new=1` opens the New session form.

// Issue runs show the issue's live log; sessions started by hand show the
// CLI's own transcript instead.
function IssueLogTranscript({ session }: { session: SessionRow }) {
  const { data: entries } = useIssueLogs(session.identifier, session.status === 'running');
  const lines = useMemo(() => entries.map(issueLogToTerminal), [entries]);
  if (lines.length === 0) return <EmptyState title="No transcript yet" />;
  return <Terminal entries={lines} follow showTime={false} resetKey={sessionKey(session)} />;
}

function SessionBody({ session }: { session: SessionRow }) {
  if (session.controllable) return <TmuxScreen id={session.id} />;
  if (session.owner === 'external') return <SessionTranscriptView id={session.id} />;
  if (session.kind === 'tmux')
    return <EmptyState title="The screen is shown once tmux is available" />;
  return <IssueLogTranscript session={session} />;
}

function SessionView({ session, tmux }: { session: SessionRow; tmux?: TmuxStatus }) {
  const inlineInput = useItervoxStore((s) => s.snapshot?.inlineInput ?? false);
  return (
    <section
      id="session-panel"
      role="tabpanel"
      aria-labelledby={`session-tab-${sessionKey(session)}`}
      className="bg-theme-panel border-theme-line flex min-h-0 min-w-0 flex-1 flex-col gap-3 rounded-lg border p-3"
    >
      <header className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 className="text-theme-text font-mono text-sm font-semibold">{sessionLabel(session)}</h2>
        {session.title && session.identifier && (
          <span className="text-theme-text-secondary text-sm">{session.title}</span>
        )}
        {session.cwd && (
          <span data-testid="session-cwd" className="text-theme-muted font-mono text-xs">
            {session.cwd}
          </span>
        )}
        <span className="text-theme-muted font-mono text-xs" data-testid="session-id">
          {session.id ? `session ${session.id}` : 'session starting…'}
        </span>
        {session.tmuxPane && (
          <span data-testid="session-tmux-pane" className="text-theme-muted font-mono text-xs">
            tmux {session.tmuxPane.label}
          </span>
        )}
      </header>
      <div className="flex min-h-[240px] flex-1 flex-col overflow-hidden">
        <SessionBody session={session} />
      </div>
      {session.controllable ? (
        <TmuxControls key={sessionKey(session)} session={session} />
      ) : (
        <SessionComposer
          key={sessionKey(session)}
          session={session}
          inlineInput={inlineInput}
          tmux={tmux}
        />
      )}
    </section>
  );
}

export default function Sessions() {
  const { sessions, isLoading, tmux, newSession } = useSessions();
  const [params, setParams] = useSearchParams();
  const formOpen = params.get('new') === '1';
  const wanted = params.get('id') ?? '';
  const selected: SessionRow | undefined =
    sessions.find((s) => sessionKey(s) === wanted) ??
    (sessions.length > 0 ? sessions[0] : undefined);
  const waiting = sessions.filter((s) => s.status === 'input_required').length;

  return (
    <>
      <PageMeta title="Sessions" description="Agent sessions of this daemon" />
      {tmux && !tmux.available && <TmuxMissingBanner tmux={tmux} />}
      {formOpen && newSession && tmux?.available && (
        <div className="mt-3">
          <NewSessionForm
            options={newSession}
            onClose={() => {
              setParams(selected ? { id: sessionKey(selected) } : {}, { replace: true });
            }}
            onStarted={(id) => {
              setParams({ id }, { replace: true });
            }}
          />
        </div>
      )}
      <div className="mt-3 flex flex-col gap-3 md:flex-row">
        <aside className="w-full shrink-0 md:w-64">
          <div className="mb-2 flex items-center justify-between gap-2">
            <h1 className="text-theme-text text-sm font-semibold">
              Sessions{' '}
              {waiting > 0 && (
                <span data-testid="sessions-waiting" className="text-orange-400">
                  · {waiting} waiting
                </span>
              )}
            </h1>
            {tmux && (
              <Button
                size="sm"
                data-testid="new-session-button"
                disabled={!tmux.available}
                title={tmux.available ? 'Start an agent in a new tmux session' : TMUX_MISSING}
                onClick={() => {
                  setParams({ new: '1' }, { replace: true });
                }}
              >
                New session
              </Button>
            )}
          </div>
          <SessionTabs
            sessions={sessions}
            selectedKey={selected ? sessionKey(selected) : ''}
            onSelect={(key) => {
              setParams({ id: key }, { replace: true });
            }}
          />
        </aside>
        {selected ? (
          <SessionView session={selected} tmux={tmux} />
        ) : (
          <EmptyState
            data-testid="sessions-empty"
            title={isLoading ? 'Loading sessions…' : 'No agent sessions are running'}
          />
        )}
      </div>
    </>
  );
}
