import { useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { Link } from 'react-router';
import { useIssues } from '../../../queries/issues';
import { useItervoxStore } from '../../../store/itervoxStore';
import { Button } from '../../../components/ui/button';
import { cn } from '../../../components/ui/cn';

/**
 * CORE-097 — first-run guidance on an empty dashboard. Renders only while the
 * UNFILTERED issue list (the same useIssues query the board uses; dashboard
 * search and state filters never reach it) and the run history are both
 * empty. States are honest: the tracker is 'configured' from the snapshot,
 * 'verified' only once the issues query has succeeded this page session (an
 * empty list still counts), 'error' when it failed. Dismissal is per browser
 * (localStorage, falling back to memory).
 */

const DISMISS_KEY = 'itervox.firstRun.dismissed';

function readDismissed(): boolean {
  try {
    return localStorage.getItem(DISMISS_KEY) === '1';
  } catch {
    return false;
  }
}

type ItemState = 'done' | 'configured' | 'verified' | 'todo' | 'error' | 'info';

const STATE_LABEL: Record<ItemState, string> = {
  done: 'Done',
  configured: 'Configured — not verified yet',
  verified: 'Verified',
  todo: 'To do',
  error: 'Error',
  info: 'Info',
};

const STATE_CLASS: Record<ItemState, string> = {
  done: 'bg-theme-success-soft text-theme-success-text',
  verified: 'bg-theme-success-soft text-theme-success-text',
  configured: 'bg-theme-warning-soft text-theme-warning-text',
  todo: 'bg-theme-bg-soft text-theme-text-secondary',
  error: 'bg-theme-danger-soft text-theme-danger-text',
  info: 'bg-theme-accent-soft text-theme-accent-strong',
};

export function FirstRunChecklist() {
  const [dismissed, setDismissed] = useState(readDismissed);
  const issuesQuery = useIssues();
  const snap = useItervoxStore(
    useShallow((s) => ({
      loaded: s.snapshot !== null,
      trackerKind: s.snapshot?.trackerKind,
      activeStates: s.snapshot?.activeStates,
      projectFilter: s.snapshot?.activeProjectFilter,
      profiles: s.snapshot?.availableProfiles,
      historyCount: s.snapshot?.history?.length ?? 0,
      runningCount: s.snapshot?.running.length ?? 0,
    })),
  );

  const issueCount = issuesQuery.data?.length ?? 0;
  if (dismissed || !snap.loaded || issueCount > 0 || snap.historyCount > 0 || snap.runningCount > 0)
    return null;

  const trackerConfigured = Boolean(snap.trackerKind) && (snap.activeStates?.length ?? 0) > 0;
  const trackerState: ItemState = issuesQuery.isError
    ? 'error'
    : issuesQuery.isSuccess
      ? 'verified'
      : trackerConfigured
        ? 'configured'
        : 'todo';
  const activeStates = snap.activeStates ?? [];
  const items: { id: string; title: string; detail: string; state: ItemState }[] = [
    {
      id: 'tracker',
      title: `Tracker${snap.trackerKind ? ` (${snap.trackerKind})` : ''}`,
      detail:
        trackerState === 'error'
          ? 'The daemon could not list issues from the tracker. Check the tracker token and run `itervox doctor`.'
          : trackerState === 'verified'
            ? 'The tracker answered; it has no issues in an active state yet.'
            : trackerConfigured
              ? 'Configured in WORKFLOW.md; waiting for the first issue list.'
              : 'Set tracker.kind and tracker.active_states in WORKFLOW.md.',
      state: trackerState,
    },
    {
      id: 'project',
      title: 'Project scope',
      detail:
        (snap.projectFilter?.length ?? 0) > 0
          ? `Limited to ${(snap.projectFilter ?? []).join(', ')}.`
          : 'All projects: no project filter is set.',
      state: 'info',
    },
    {
      id: 'profile',
      title: 'Agent profile',
      detail:
        (snap.profiles?.length ?? 0) > 0
          ? `${String(snap.profiles?.length ?? 0)} profile(s) available.`
          : 'Add an agent profile on the Agents page.',
      state: (snap.profiles?.length ?? 0) > 0 ? 'done' : 'todo',
    },
    {
      id: 'dispatch',
      title: 'First run',
      detail: `No dispatch this session. Move an issue to ${
        activeStates.length > 0 ? activeStates.map((s) => `“${s}”`).join(' or ') : 'an active state'
      } and it is picked up on the next poll.`,
      state: 'todo',
    },
  ];

  return (
    <section
      aria-labelledby="first-run-heading"
      data-testid="first-run-checklist"
      className="border-theme-line bg-theme-bg-elevated rounded-[var(--radius-lg)] border px-4 py-3"
    >
      <div className="mb-2 flex items-center justify-between gap-3">
        <h2 id="first-run-heading" className="text-theme-text text-sm font-semibold">
          Getting started
        </h2>
        <Button
          variant="ghost"
          size="xs"
          onClick={() => {
            try {
              localStorage.setItem(DISMISS_KEY, '1');
            } catch {
              // in-memory only
            }
            setDismissed(true);
          }}
        >
          Dismiss
        </Button>
      </div>
      <ol className="space-y-2">
        {items.map((item) => (
          <li
            key={item.id}
            data-testid={`first-run-${item.id}`}
            data-state={item.state}
            className="flex flex-wrap items-start gap-2 text-xs"
          >
            <span
              className={cn(
                'rounded px-1.5 py-0.5 text-[10px] font-semibold',
                STATE_CLASS[item.state],
              )}
            >
              {STATE_LABEL[item.state]}
            </span>
            <span className="min-w-0 flex-1">
              <span className="text-theme-text font-medium">{item.title}</span>{' '}
              <span className="text-theme-text-secondary">{item.detail}</span>
            </span>
          </li>
        ))}
      </ol>
      <p className="text-theme-muted mt-2 text-[11px]">
        Profiles live on the{' '}
        <Link to="/agents" className="text-theme-accent-text underline underline-offset-2">
          Agents page
        </Link>
        ; tracker states and scope in{' '}
        <Link to="/settings" className="text-theme-accent-text underline underline-offset-2">
          Settings
        </Link>
        .
      </p>
    </section>
  );
}
