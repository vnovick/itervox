import { useEffect, useMemo, useRef } from 'react';
import { EmptyState } from '../../components/ui/EmptyState';
import { Button } from '../../components/ui/button';
import { useSessionTranscript } from '../../queries/sessions';
import type { TranscriptEntry } from '../../types/schemas';

// The transcript of a session started by hand, or continued under itervox
// (#178 step 3): read from the CLI's own transcript file by the daemon,
// redacted and abbreviated there, newest last. Older pages load on demand.

function entryLabel(e: TranscriptEntry): string {
  if (e.kind === 'tool') return 'tool';
  if (e.kind === 'tool_result') return 'result';
  return e.role === 'user' ? 'user' : 'agent';
}

function TranscriptItem({ entry }: { entry: TranscriptEntry }) {
  const tool = entry.kind !== 'text';
  return (
    <li
      data-testid="transcript-entry"
      data-kind={entry.kind}
      className="border-theme-line flex gap-2 border-b py-1.5 text-xs last:border-b-0"
    >
      <span
        className={
          entry.role === 'user' && !tool
            ? 'w-12 shrink-0 font-mono font-semibold text-orange-400'
            : 'text-theme-muted w-12 shrink-0 font-mono'
        }
      >
        {entryLabel(entry)}
      </span>
      <span
        className={
          tool
            ? 'text-theme-text-secondary font-mono break-all whitespace-pre-wrap'
            : 'text-theme-text break-words whitespace-pre-wrap'
        }
      >
        {entry.text || '—'}
        {entry.truncated && <span className="text-theme-muted"> (abbreviated)</span>}
      </span>
    </li>
  );
}

/** Content keys for entries (they carry no id); repeats get a counter. */
function entryKeys(entries: readonly TranscriptEntry[]): string[] {
  const seen = new Map<string, number>();
  return entries.map((e) => {
    const base = `${e.at ?? ''}|${e.role}|${e.kind}|${e.text.slice(0, 48)}`;
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    return `${base}|${String(n)}`;
  });
}

export function SessionTranscriptView({ id }: { id: string }) {
  const { entries, isLoading, isError, hasNextPage, fetchNextPage, isFetchingNextPage } =
    useSessionTranscript(id);
  const keys = useMemo(() => entryKeys(entries), [entries]);
  const endRef = useRef<HTMLLIElement>(null);
  const count = entries.length;
  const last = count > 0 ? entries[count - 1] : undefined;
  const lastKey = last ? `${last.at ?? ''}-${last.kind}-${last.text.slice(0, 32)}` : '';
  // Follow new entries at the end (not when an older page is prepended).
  useEffect(() => {
    const end: Partial<HTMLElement> | null = endRef.current;
    end?.scrollIntoView?.({ block: 'end' }); // absent in jsdom
  }, [lastKey]);

  if (count === 0) {
    return (
      <EmptyState
        title={
          isLoading
            ? 'Reading the transcript…'
            : isError
              ? 'The transcript could not be read'
              : 'No messages yet'
        }
      />
    );
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto" data-testid="transcript">
      {hasNextPage && (
        <Button
          size="sm"
          variant="secondary"
          disabled={isFetchingNextPage}
          onClick={() => {
            void fetchNextPage();
          }}
        >
          {isFetchingNextPage ? 'Loading…' : 'Load earlier messages'}
        </Button>
      )}
      <ol aria-label="Transcript" className="min-w-0">
        {entries.map((e, i) => (
          <TranscriptItem key={keys[i]} entry={e} />
        ))}
        <li ref={endRef} aria-hidden="true" />
      </ol>
    </div>
  );
}
