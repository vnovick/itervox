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

import { useNavigate } from 'react-router';
import { useOperatorQueue } from '../../hooks/useOperatorQueue';
import { operatorToneDotClass, type OperatorQueueItem } from '../../lib/operatorQueue';
import { ReviewSourcePill } from './ReviewSourcePill';

interface NotificationsViewProps {
  onSelect: (identifier: string) => void;
}

export function NotificationsView({ onSelect }: NotificationsViewProps) {
  const queue = useOperatorQueue();

  const navigate = useNavigate();

  const handleClick = (item: OperatorQueueItem) => {
    if (item.clickAction.type === 'select-issue') {
      onSelect(item.clickAction.identifier);
    } else if (item.clickAction.type === 'navigate') {
      void navigate(item.clickAction.path);
    }
  };

  if (queue.total === 0) {
    return (
      <div data-testid="notifications-view" className="glass-card px-6 py-12 text-center">
        <p className="text-theme-text text-base font-semibold">All caught up</p>
        <p className="text-theme-text-secondary mt-1 text-sm">
          Nothing needs your attention right now.
        </p>
      </div>
    );
  }

  return (
    <div data-testid="notifications-view" className="space-y-4">
      {queue.groups.map((group) => (
        <section key={group.group} aria-labelledby={`notifications-group-${group.group}`}>
          <h2
            id={`notifications-group-${group.group}`}
            className="text-theme-text-secondary mb-1.5 text-xs font-semibold"
          >
            {group.label} · {group.items.length}
          </h2>
          <ul className="space-y-2">
            {group.items.map((item) => (
              <li key={item.id}>
                <button
                  type="button"
                  onClick={() => {
                    handleClick(item);
                  }}
                  className="glass-card hover:bg-theme-panel-strong flex w-full items-center gap-2.5 px-3 py-2.5 text-left transition-colors"
                >
                  <span
                    aria-hidden="true"
                    className={`status-dot ${operatorToneDotClass(item.tone)}`}
                  />
                  <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2.5 gap-y-0.5">
                    <span className="text-theme-text font-mono text-xs font-medium">
                      {item.title}
                    </span>
                    {item.subtitle && (
                      <span className="text-theme-text min-w-0 truncate text-sm font-medium">
                        {item.subtitle}
                      </span>
                    )}
                    {item.reviewSource && <ReviewSourcePill source={item.reviewSource} />}
                  </div>
                  {item.meta && !item.reviewSource && (
                    <span className="text-theme-muted text-xs">{item.meta}</span>
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
