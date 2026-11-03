// The runs of the selected prompt version (#200), read from
// GET /api/v1/history?prompt=<digest>, newest first.

import { usePromptVersionRuns } from '../../../queries/repo';
import { fmtMs } from '../../../utils/format';
import { shortDigest } from '../../../utils/promptVersions';
import { fmtSeen } from './promptVersionGroups';

interface PromptVersionRunsProps {
  digest: string;
  onClear: () => void;
}

export function PromptVersionRuns({ digest, onClear }: PromptVersionRunsProps) {
  const { data: runs, isLoading, isError } = usePromptVersionRuns(digest);
  const newestFirst = [...(runs ?? [])].reverse();
  return (
    <div
      className="border-theme-line bg-theme-panel rounded-lg border p-4"
      aria-label="Runs of the selected prompt version"
      role="region"
    >
      <div className="mb-2 flex items-center justify-between gap-3">
        <p className="text-theme-text text-sm font-medium">
          Runs of <span className="font-mono">{shortDigest(digest)}</span>
        </p>
        <button
          type="button"
          onClick={onClear}
          className="border-theme-line text-theme-text rounded border px-2 py-1 text-[11px] hover:bg-[var(--bg-soft)]"
        >
          Clear filter
        </button>
      </div>
      {isLoading && <p className="text-theme-muted text-xs">Loading runs…</p>}
      {isError && <p className="text-theme-danger-text text-xs">Could not load the runs.</p>}
      {!isLoading && !isError && newestFirst.length === 0 && (
        <p className="text-theme-muted text-xs">No runs of this version in the history.</p>
      )}
      {newestFirst.length > 0 && (
        <ul className="space-y-1">
          {newestFirst.map((r) => (
            <li
              key={`${r.identifier}-${r.runId ?? r.startedAt}`}
              className="flex flex-wrap items-center justify-between gap-2 text-xs"
            >
              <span className="text-theme-text font-mono">{r.identifier}</span>
              <span className="text-theme-muted">
                {r.status} · {fmtSeen(r.startedAt)} · {fmtMs(r.elapsedMs)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
