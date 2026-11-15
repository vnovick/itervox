// Memory page, Lessons tab (#217): the enforcement Itervox suggests once a
// lesson has been seen memory.lessons.enforce_threshold times — the
// strongest check it could draft (lint rule, type, test, CLAUDE.md /
// AGENTS.md rule, Itervox rule). Accepting creates an "Enforce: <lesson>"
// issue carrying the draft and the evidence, so an agent implements it like
// any other issue; nothing is committed from here.

import { Button } from '../../components/ui/button';
import { useAcceptEnforcement, useDismissEnforcement } from '../../queries/memory';
import type { MemoryEntry } from '../../types/schemas';
import { ENFORCEMENT_KIND_LABELS } from './memoryModel';

export function EnforcementCard({ entry }: { entry: MemoryEntry }) {
  const accept = useAcceptEnforcement();
  const dismiss = useDismissEnforcement();
  const enf = entry.lesson?.enforcement;
  if (!enf || enf.status === 'dismissed') return null;
  const kind = ENFORCEMENT_KIND_LABELS[enf.kind] ?? enf.kind;
  const busy = accept.isPending || dismiss.isPending;

  return (
    <section
      className="border-theme-line bg-theme-bg-soft mt-3 rounded border px-3 py-2"
      aria-label="Enforcement suggestion"
      data-testid={`enforcement-${entry.id}`}
    >
      <p className="text-theme-text text-xs font-semibold">
        {enf.status === 'accepted' ? 'Enforcement accepted' : 'Suggested enforcement'} · {kind}
      </p>
      {enf.title && <p className="text-theme-text mt-0.5 text-sm">{enf.title}</p>}
      <p className="text-theme-text-secondary mt-1 text-xs whitespace-pre-wrap">{enf.draft}</p>
      <p className="text-theme-muted mt-1 text-xs">
        Seen {String(entry.lesson?.evidence ?? 0)}× · drafted by {enf.draftedBy ?? 'template'}
      </p>
      {enf.status === 'accepted' ? (
        <p className="text-theme-text mt-2 text-xs" data-testid="enforcement-issue">
          Tracked in{' '}
          {enf.issueUrl ? (
            <a
              className="text-theme-accent-strong underline"
              href={enf.issueUrl}
              target="_blank"
              rel="noreferrer"
            >
              {enf.issue}
            </a>
          ) : (
            (enf.issue ?? 'an issue')
          )}
        </p>
      ) : (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="primary"
            disabled={busy}
            onClick={() => {
              accept.mutate(entry.id);
            }}
          >
            Create Enforce issue
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={() => {
              dismiss.mutate(entry.id);
            }}
          >
            Dismiss
          </Button>
          <span className="text-theme-muted text-xs">
            Creates a tracker issue for an agent to implement; never commits code.
          </span>
        </div>
      )}
    </section>
  );
}
