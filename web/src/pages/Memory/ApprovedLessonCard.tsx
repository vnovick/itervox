// Memory page, Lessons tab (#217): one approved lesson with its analytics
// (times applied, the runs that used it and how they ended), its
// enforcement suggestion when it has one, and promotion to a rule.

import type { MemoryEntry } from '../../types/schemas';
import { EnforcementCard } from './EnforcementCard';
import { LessonAnalytics } from './LessonAnalytics';
import { LessonPromoteRule } from './LessonPromoteRule';
import { NOT_HELPING_LABELS, SCOPE_INFO, TRIGGER_LABELS } from './memoryModel';

export function ApprovedLessonCard({ entry }: { entry: MemoryEntry }) {
  const trigger = entry.lesson
    ? (TRIGGER_LABELS[entry.lesson.trigger] ?? entry.lesson.trigger)
    : '';
  return (
    <li className="px-3 py-3 text-sm" data-testid={`approved-lesson-${entry.id}`}>
      <p className="text-theme-text">{entry.text}</p>
      <p className="text-theme-muted mt-0.5 text-xs">
        {SCOPE_INFO[entry.scope].label}
        {trigger ? ` · ${trigger}` : ''}
        {entry.notHelping ? ` · ${NOT_HELPING_LABELS[entry.notHelping] ?? entry.notHelping}` : ''}
      </p>
      <LessonAnalytics entry={entry} />
      <EnforcementCard entry={entry} />
      <LessonPromoteRule entry={entry} />
    </li>
  );
}
