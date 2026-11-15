import { useRef, type KeyboardEvent } from 'react';
import { cn } from '../../components/ui/cn';
import { sessionKey, sessionLabel } from '../../queries/sessions';
import type { SessionRow } from '../../types/schemas';

export interface SessionTabsProps {
  sessions: readonly SessionRow[];
  selectedKey: string;
  onSelect: (key: string) => void;
}

const STATUS_LABEL: Record<string, string> = {
  input_required: 'needs input',
  pending_input_resume: 'resuming',
  running: 'running',
  exited: 'exited',
};

/** The second line of a tab: who owns it and how it runs. */
function tabDetail(s: SessionRow): string {
  if (s.owner === 'external') return 'started outside itervox';
  if (s.kind === 'tmux') return ['started in tmux', s.backend].filter(Boolean).join(' · ');
  return [s.profile, s.backend, s.workerHost].filter(Boolean).join(' · ') || 'default';
}

/** One tab per session, in the daemon's order (waiting for input first). */
export function SessionTabs({ sessions, selectedKey, onSelect }: SessionTabsProps) {
  const refs = useRef(new Map<string, HTMLButtonElement>());

  // Roving focus (WAI-ARIA tabs): arrows move and select, Home/End jump.
  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (sessions.length === 0) return;
    const at = Math.max(
      0,
      sessions.findIndex((s) => sessionKey(s) === selectedKey),
    );
    let next = -1;
    if (e.key === 'ArrowDown' || e.key === 'ArrowRight') next = (at + 1) % sessions.length;
    else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft')
      next = (at - 1 + sessions.length) % sessions.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = sessions.length - 1;
    if (next < 0) return;
    e.preventDefault();
    const key = sessionKey(sessions[next]);
    onSelect(key);
    refs.current.get(key)?.focus();
  };

  return (
    <div
      role="tablist"
      aria-label="Agent sessions"
      aria-orientation="vertical"
      className="flex flex-col gap-1"
    >
      {sessions.map((s) => {
        const key = sessionKey(s);
        const selected = key === selectedKey;
        const waiting = s.status === 'input_required';
        const external = s.owner === 'external';
        return (
          <button
            key={`${s.owner}-${s.identifier}-${key}`}
            ref={(el) => {
              if (el) refs.current.set(key, el);
              else refs.current.delete(key);
            }}
            type="button"
            role="tab"
            id={`session-tab-${key}`}
            aria-selected={selected}
            aria-controls="session-panel"
            tabIndex={selected ? 0 : -1}
            data-testid="session-tab"
            data-owner={s.owner}
            onKeyDown={onKeyDown}
            onClick={() => {
              onSelect(key);
            }}
            className={cn(
              'flex w-full flex-col items-start gap-0.5 rounded-lg border px-3 py-2 text-left text-xs',
              selected
                ? 'border-theme-accent bg-theme-bg-elevated'
                : 'border-theme-line hover:bg-theme-bg-soft',
              external && !selected && 'border-dashed opacity-70',
            )}
          >
            <span className="flex w-full items-center justify-between gap-2">
              <span
                className={cn(
                  'truncate font-semibold',
                  s.identifier ? 'font-mono' : '',
                  external ? 'text-theme-text-secondary' : 'text-theme-text',
                )}
              >
                {sessionLabel(s)}
              </span>
              {waiting ? (
                <span
                  data-testid="session-badge-input"
                  className="rounded-full bg-orange-500/15 px-2 py-0.5 text-[10px] font-semibold text-orange-400"
                >
                  needs input
                </span>
              ) : (
                <span
                  data-testid="session-tab-status"
                  className="text-theme-muted shrink-0"
                  title={external && s.status === 'running' ? s.inUseReason : undefined}
                >
                  {STATUS_LABEL[s.status] ?? s.status}
                </span>
              )}
            </span>
            <span
              data-testid="session-tab-detail"
              className={cn(
                'truncate',
                external ? 'text-theme-muted italic' : 'text-theme-text-secondary',
              )}
            >
              {tabDetail(s)}
            </span>
            {(s.tmuxPane || s.kind === 'tmux') && (
              <span className="flex items-center gap-1">
                <span
                  data-testid="session-badge-tmux"
                  title={s.tmuxPane ? `tmux pane ${s.tmuxPane.label}` : 'tmux'}
                  className="bg-theme-bg-soft text-theme-text-secondary rounded px-1.5 py-0.5 font-mono text-[10px]"
                >
                  tmux
                </span>
                {s.controllable && (
                  <span
                    data-testid="session-badge-controllable"
                    className="text-theme-muted text-[10px]"
                  >
                    controllable
                  </span>
                )}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}
