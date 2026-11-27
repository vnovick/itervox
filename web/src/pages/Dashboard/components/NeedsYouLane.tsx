import { useState } from 'react';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useDismissInput, usePendingIssueIds, useProvideInput } from '../../../queries/issues';
import { useNow } from '../../../hooks/useNow';
import { waitOf } from '../../../lib/needsYou';
import { EMPTY_NEEDS_YOU } from '../../../utils/constants';
import { NeedsYouItem } from './NeedsYouItem';

// The wait column moves on its own between snapshot pushes; minutes are its
// finest unit, so a coarse clock is enough.
const WAIT_TICK_MS = 15_000;

// The "Needs you" lane, at the top of the dashboard: every issue blocked on
// a person (a question, a guardrail approval, a held pipeline change),
// oldest first as the daemon sends it (snapshot `needsYou`), with how long
// each has waited. Hidden when nothing waits. Rows are never removed
// optimistically: an item leaves when the next snapshot drops it. Not a
// live region — the clock and snapshot pushes would re-announce it.
export function NeedsYouLane({ onSelectIssue }: { onSelectIssue: (identifier: string) => void }) {
  const rows = useItervoxStore((s) => s.snapshot?.needsYou ?? EMPTY_NEEDS_YOU);
  const inlineInput = useItervoxStore((s) => s.snapshot?.inlineInput ?? false);
  const now = useNow(WAIT_TICK_MS);
  const [replyOpenFor, setReplyOpenFor] = useState<string | null>(null);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  // One instance of each mutation for every row (BH-M5-3): drafts clear from
  // the hook-level onSent, in-flight state comes from the mutation cache.
  const provideInput = useProvideInput({
    onSent: (identifier) => {
      setDrafts((prev) => ({ ...prev, [identifier]: '' }));
    },
  });
  const dismissInput = useDismissInput();
  const sendingIds = usePendingIssueIds('provideInput');
  const dismissingIds = usePendingIssueIds('dismissInput');

  if (rows.length === 0) return null;

  return (
    <section
      id="needs-you"
      aria-labelledby="needs-you-heading"
      data-testid="needs-you-lane"
      className="glass-panel border-theme-warning/30 scroll-mt-20 px-4 py-4 sm:px-[18px]"
    >
      <h2
        id="needs-you-heading"
        className="text-theme-text mb-1 flex items-center gap-2 text-base font-semibold"
      >
        Needs you
        <span data-testid="needs-you-count" className="count-pill">
          {rows.length}
        </span>
      </h2>
      <p className="text-theme-text-secondary mb-2.5 text-xs">
        Agents blocked on a person, longest wait first.
      </p>
      <ul aria-labelledby="needs-you-heading" className="space-y-2">
        {rows.map((row) => {
          const id = row.identifier;
          return (
            <NeedsYouItem
              key={`${row.kind}-${id}-${row.sessionId ?? ''}`}
              row={row}
              wait={waitOf(row, now)}
              inlineInput={inlineInput}
              replyOpen={replyOpenFor === id}
              onToggleReply={() => {
                setReplyOpenFor(replyOpenFor === id ? null : id);
              }}
              onOpen={() => {
                onSelectIssue(id);
              }}
              draft={drafts[id] ?? ''}
              onDraftChange={(draft) => {
                setDrafts((prev) => ({ ...prev, [id]: draft }));
              }}
              provideInput={provideInput}
              dismissInput={dismissInput}
              sending={sendingIds.has(id)}
              dismissing={dismissingIds.has(id)}
            />
          );
        })}
      </ul>
    </section>
  );
}
