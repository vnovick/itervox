import { useMemo } from 'react';
import { useSearchParams } from 'react-router';
import PageMeta from '../../components/common/PageMeta';
import { EmptyState } from '../../components/ui/EmptyState';
import { Terminal } from '../../components/ui/Terminal/Terminal';
import { useIssueLogs } from '../../queries/logs';
import { sessionKey, useSessions } from '../../queries/sessions';
import { useItervoxStore } from '../../store/itervoxStore';
import type { SessionRow } from '../../types/schemas';
import { issueLogToTerminal } from '../../utils/logFormatting';
import { SessionComposer } from './SessionComposer';
import { SessionTabs } from './SessionTabs';

// Sessions panel (#178): one tab per agent session of this daemon, sessions
// waiting for input first. `?id=` selects a tab (the Mac app's Sessions
// window opens this route with it).

function SessionView({ session }: { session: SessionRow }) {
  const inlineInput = useItervoxStore((s) => s.snapshot?.inlineInput ?? false);
  const { data: entries } = useIssueLogs(session.identifier, session.status === 'running');
  const lines = useMemo(() => entries.map(issueLogToTerminal), [entries]);
  return (
    <section
      id="session-panel"
      role="tabpanel"
      aria-labelledby={`session-tab-${sessionKey(session)}`}
      className="bg-theme-panel border-theme-line flex min-h-0 min-w-0 flex-1 flex-col gap-3 rounded-lg border p-3"
    >
      <header className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 className="text-theme-text font-mono text-sm font-semibold">{session.identifier}</h2>
        {session.title && (
          <span className="text-theme-text-secondary text-sm">{session.title}</span>
        )}
        <span className="text-theme-muted font-mono text-xs" data-testid="session-id">
          {session.id ? `session ${session.id}` : 'session starting…'}
        </span>
      </header>
      <div className="flex min-h-[240px] flex-1 flex-col overflow-hidden">
        {lines.length === 0 ? (
          <EmptyState title="No transcript yet" />
        ) : (
          <Terminal entries={lines} follow showTime={false} resetKey={sessionKey(session)} />
        )}
      </div>
      <SessionComposer key={sessionKey(session)} session={session} inlineInput={inlineInput} />
    </section>
  );
}

export default function Sessions() {
  const { sessions, isLoading } = useSessions();
  const [params, setParams] = useSearchParams();
  const wanted = params.get('id') ?? '';
  const selected: SessionRow | undefined =
    sessions.find((s) => sessionKey(s) === wanted) ??
    (sessions.length > 0 ? sessions[0] : undefined);
  const waiting = sessions.filter((s) => s.status === 'input_required').length;

  return (
    <>
      <PageMeta title="Sessions" description="Agent sessions of this daemon" />
      <div className="flex flex-col gap-3 md:flex-row">
        <aside className="w-full shrink-0 md:w-64">
          <h1 className="text-theme-text mb-2 text-sm font-semibold">
            Sessions{' '}
            {waiting > 0 && (
              <span data-testid="sessions-waiting" className="text-orange-400">
                · {waiting} waiting
              </span>
            )}
          </h1>
          <SessionTabs
            sessions={sessions}
            selectedKey={selected ? sessionKey(selected) : ''}
            onSelect={(key) => {
              setParams({ id: key }, { replace: true });
            }}
          />
        </aside>
        {selected ? (
          <SessionView session={selected} />
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
