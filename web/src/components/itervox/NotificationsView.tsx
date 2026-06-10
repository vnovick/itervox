// Notifications dashboard tab — rolls up every "needs my attention" item
// from the snapshot into one scannable list. Pure rendering: subscribes to
// `useItervoxStore` for the snapshot and `useIssues` for tracker rows, then
// derives the grouped queue via `buildOperatorQueueItems` (pure helper).
//
// Design notes:
// - Existing panels (PendingResumePanel, ReviewQueueSection, RetryQueueTable)
//   are NOT replaced — they stay visible. Notifications is a higher-level
//   rollup view.
// - Empty state: friendly "All caught up" headline.
// - Click on a select-issue row dispatches onSelect(identifier). Click on a
//   navigate row uses react-router useNavigate.

import { useMemo } from 'react';
import { useNavigate } from 'react-router';
import { useItervoxStore } from '../../store/itervoxStore';
import { useIssues } from '../../queries/issues';
import { buildOperatorQueueItems, type OperatorQueueItem } from '../../lib/operatorQueue';
import { ReviewSourcePill } from './ReviewSourcePill';

const EMPTY_ISSUES: never[] = [];

interface NotificationsViewProps {
  onSelect: (identifier: string) => void;
}

export function NotificationsView({ onSelect }: NotificationsViewProps) {
  const snapshot = useItervoxStore((s) => s.snapshot);
  const issuesQuery = useIssues();
  const issues = issuesQuery.data ?? EMPTY_ISSUES;

  const queue = useMemo(() => {
    if (!snapshot) return { groups: [], total: 0 };
    return buildOperatorQueueItems(snapshot, issues);
  }, [snapshot, issues]);

  const navigate = useNavigate();

  const handleClick = (item: OperatorQueueItem) => {
    if (item.clickAction.type === 'select-issue') {
      onSelect(item.clickAction.identifier);
    } else {
      void navigate(item.clickAction.path);
    }
  };

  if (queue.total === 0) {
    return (
      <div className="border-theme-line bg-theme-bg-elevated rounded-[var(--radius-md)] border px-6 py-12 text-center">
        <p className="text-theme-text text-base font-semibold">All caught up</p>
        <p className="text-theme-text-secondary mt-1 text-sm">
          Nothing needs your attention right now.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {queue.groups.map((group) => (
        <section key={group.group} aria-labelledby={`notifications-group-${group.group}`}>
          <h2
            id={`notifications-group-${group.group}`}
            className="mb-2 text-xs font-semibold tracking-widest uppercase"
          >
            {group.label} · {group.items.length}
          </h2>
          <ul className="border-theme-line bg-theme-bg-elevated divide-theme-line divide-y rounded-[var(--radius-md)] border">
            {group.items.map((item) => (
              <li key={item.id}>
                <button
                  type="button"
                  onClick={() => {
                    handleClick(item);
                  }}
                  className="hover:bg-theme-panel flex w-full items-center gap-3 px-4 py-3 text-left transition-colors"
                >
                  <span
                    aria-hidden="true"
                    className={`block h-8 w-1 rounded-full ${toneStripeClass(item.tone)}`}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="text-theme-text font-mono text-sm font-medium">
                        {item.title}
                      </span>
                      {item.reviewSource && <ReviewSourcePill source={item.reviewSource} />}
                    </div>
                    {item.subtitle && (
                      <p className="text-theme-text-secondary mt-0.5 truncate text-xs">
                        {item.subtitle}
                      </p>
                    )}
                  </div>
                  {item.meta && !item.reviewSource && (
                    <span className="text-theme-text-secondary text-xs">{item.meta}</span>
                  )}
                  <span className="text-theme-text-secondary" aria-hidden="true">
                    ›
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

function toneStripeClass(tone: 'info' | 'warning' | 'danger'): string {
  switch (tone) {
    case 'danger':
      return 'bg-theme-danger';
    case 'warning':
      return 'bg-amber-500';
    case 'info':
    default:
      return 'bg-theme-accent';
  }
}
