// Memory page, Lessons tab (#202): the review queue for proposed lessons.
// Proposed entries are never used by a run until approved. v1 lists them
// read-only; lesson proposals and the approve/reject actions arrive with
// #203.

import { EmptyState } from '../../components/ui/EmptyState';
import type { MemoryView } from '../../types/schemas';
import { SCOPE_INFO, proposedEntries } from './memoryModel';

export function LessonsTab({ view }: { view: MemoryView }) {
  const proposed = proposedEntries(view.entries);
  if (proposed.length === 0) {
    return (
      <EmptyState
        data-testid="lessons-empty"
        title="No proposed lessons"
        description="Lessons Itervox proposes from corrections and failed-then-fixed runs will wait here for your review. They are not used by any run until approved."
      />
    );
  }
  return (
    <ul
      className="divide-theme-line border-theme-line divide-y rounded border"
      data-testid="lessons-list"
    >
      {proposed.map((e) => (
        <li key={`${e.scope}-${e.id}`} className="px-3 py-2 text-sm">
          <p className="text-theme-text">{e.text}</p>
          <p className="text-theme-muted mt-0.5 text-xs">
            {SCOPE_INFO[e.scope].label} · proposed · not used until approved
            {e.source?.issue ? ` · from ${e.source.issue}` : ''}
          </p>
        </li>
      ))}
    </ul>
  );
}
