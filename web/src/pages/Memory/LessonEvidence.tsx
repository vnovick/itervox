// Memory page, Lessons tab (#203): a lesson's review record and evidence —
// what it applies to, what went wrong, what worked, the issue, runs and
// links it came from, confidence and how often it was seen.

import type { MemoryEntry } from '../../types/schemas';
import { TRIGGER_LABELS, confidenceLabel } from './memoryModel';

export function LessonEvidence({ entry }: { entry: MemoryEntry }) {
  const l = entry.lesson;
  const src = entry.source;
  const rows: [string, string | undefined][] = [
    ['Trigger', l ? (TRIGGER_LABELS[l.trigger] ?? l.trigger) : undefined],
    ['Situation', l?.situation],
    ['Went wrong', l?.wentWrong],
    ['Worked', l?.worked],
    ['Verified by', l?.verification],
    ['Issue', src?.issue],
    ['Runs', src?.runIds?.join(', ')],
    ['Confidence', l ? confidenceLabel(l.confidence) : undefined],
    ['Seen', l?.evidence ? `${String(l.evidence)}×` : undefined],
    ['Drafted by', l?.draftedBy],
  ];
  return (
    <dl
      className="mt-2 grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-2 gap-y-0.5 text-xs"
      aria-label="Evidence"
    >
      {rows
        .filter(([, v]) => v !== undefined && v !== '')
        .map(([k, v]) => (
          <span key={k} className="contents">
            <dt className="text-theme-muted">{k}</dt>
            <dd className="text-theme-text-secondary break-words">{v}</dd>
          </span>
        ))}
      {(src?.links ?? []).map((link) => (
        <span key={link} className="contents">
          <dt className="text-theme-muted">Link</dt>
          <dd className="break-all">
            <a
              className="text-theme-accent-strong underline"
              href={link}
              target="_blank"
              rel="noreferrer"
            >
              {link}
            </a>
          </dd>
        </span>
      ))}
    </dl>
  );
}
