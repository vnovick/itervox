// Memory page (#202): the git diff a promote returns, so the operator sees
// what the commit of .itervox/memory.json will add.

import type { MemoryPromoteResult } from '../../types/schemas';

export function PromoteDiff({ result }: { result: MemoryPromoteResult }) {
  return (
    <section aria-label="Promote diff" className="space-y-1">
      <p className="text-theme-muted text-xs">
        Moved to <code className="break-all">{result.path}</code>. Commit it to share it with the
        team.
        {result.diffNote ? ` (${result.diffNote})` : ''}
      </p>
      {result.diff && (
        <pre
          data-testid="memory-promote-diff"
          className="bg-theme-panel-strong max-h-64 overflow-auto rounded p-2 font-mono text-[11px]"
        >
          {result.diff}
        </pre>
      )}
    </section>
  );
}
