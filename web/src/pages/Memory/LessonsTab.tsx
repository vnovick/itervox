// Memory page, Lessons tab (#203): the review queue for the lessons Itervox
// proposes from corrections and failed-then-succeeded runs (approve into a
// scope, edit then approve, reject), and the approved lessons that are not
// helping. Proposed entries are never used by a run until approved.

import { EmptyState } from '../../components/ui/EmptyState';
import { ToggleChip } from '../../components/ui/ToggleChip';
import { useUIStore } from '../../store/uiStore';
import type { MemoryView } from '../../types/schemas';
import { LessonCard } from './LessonCard';
import { SCOPE_INFO, notHelpingEntries, proposedEntries } from './memoryModel';

function LessonsConfigNote({ view }: { view: MemoryView }) {
  const l = view.lessons;
  if (!l) return null;
  if (!l.enabled) {
    return (
      <p className="text-theme-muted text-xs" data-testid="lessons-config">
        Lesson proposals are off (<code>memory.lessons.enabled</code>).
      </p>
    );
  }
  const auto = [
    l.autoApprovePrivate ? SCOPE_INFO.private.label : '',
    l.autoApproveProject ? SCOPE_INFO.project.label : '',
  ].filter((s) => s !== '');
  return (
    <p className="text-theme-muted text-xs" data-testid="lessons-config">
      Drafted by {l.drafter === 'template' ? 'the template' : l.drafter.replace(/^profile:/, '')} ·
      proposed into {SCOPE_INFO[l.scope].label.toLowerCase()} memory · at most {l.maxPerIssue} per
      issue every {l.rateWindowHours}h ·{' '}
      {auto.length > 0 ? `auto-approved: ${auto.join(', ')}` : 'every lesson waits for review'} ·{' '}
      {l.rejected} rejected
    </p>
  );
}

export function LessonsTab({ view }: { view: MemoryView }) {
  const mode = useUIStore((s) => s.memoryLessonsView);
  const setMode = useUIStore((s) => s.setMemoryLessonsView);
  const proposed = proposedEntries(view.entries);
  const stale = notHelpingEntries(view.entries);

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Lessons view">
        <ToggleChip
          pressed={mode === 'queue'}
          onPressedChange={() => {
            setMode('queue');
          }}
        >
          Review queue ({proposed.length})
        </ToggleChip>
        <ToggleChip
          pressed={mode === 'not-helping'}
          onPressedChange={() => {
            setMode('not-helping');
          }}
        >
          Not helping ({stale.length})
        </ToggleChip>
      </div>
      <LessonsConfigNote view={view} />
      {mode === 'queue' &&
        (proposed.length === 0 ? (
          <EmptyState
            data-testid="lessons-empty"
            title="No proposed lessons"
            description="Lessons Itervox proposes from corrections and failed-then-fixed runs will wait here for your review. They are not used by any run until approved."
          />
        ) : (
          <ul
            className="divide-theme-line border-theme-line divide-y rounded border"
            data-testid="lessons-list"
          >
            {proposed.map((e) => (
              <LessonCard key={`${e.scope}-${e.id}`} entry={e} />
            ))}
          </ul>
        ))}
      {mode === 'not-helping' &&
        (stale.length === 0 ? (
          <EmptyState
            data-testid="lessons-helping"
            title="Every approved lesson is helping"
            description={`Approved lessons that none of the last ${String(view.lessons?.staleAfterRuns ?? 20)} runs used, or whose runs mostly failed, are listed here so you can edit or archive them in Entries.`}
          />
        ) : (
          <ul
            className="divide-theme-line border-theme-line divide-y rounded border"
            data-testid="lessons-not-helping"
          >
            {stale.map((e) => (
              <li key={`${e.scope}-${e.id}`} className="px-3 py-2 text-sm">
                <p className="text-theme-text">{e.text}</p>
                <p className="text-theme-warning-text mt-0.5 text-xs">
                  {e.notHelping === 'failing' ? 'Failing' : 'Unused'}: {e.notHelpingReason}
                </p>
              </li>
            ))}
          </ul>
        ))}
    </div>
  );
}
