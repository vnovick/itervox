// Memory page, Lessons tab (#217): how runs used an approved lesson — how
// often it was applied, and how the runs in history that had it ended.

import type { MemoryEntry } from '../../types/schemas';

export function LessonAnalytics({ entry }: { entry: MemoryEntry }) {
  const a = entry.analytics;
  if (!a) return null;
  const other = Math.max(0, a.runs - a.succeeded - a.failed);
  return (
    <p className="text-theme-muted mt-1 text-xs" data-testid={`analytics-${entry.id}`}>
      Applied {String(a.applied)}× · {String(a.runs)} {a.runs === 1 ? 'run' : 'runs'} in history
      {a.runs > 0 && (
        <>
          : <span className="text-theme-success-text">{String(a.succeeded)} succeeded</span>,{' '}
          <span className={a.failed > 0 ? 'text-theme-danger-text' : undefined}>
            {String(a.failed)} failed
          </span>
          {other > 0 ? `, ${String(other)} other` : ''}
        </>
      )}
    </p>
  );
}
