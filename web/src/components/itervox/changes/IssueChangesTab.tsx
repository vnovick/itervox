import { useState } from 'react';
import { useIssueDiff, type DiffSource } from '../../../queries/changes';
import { DiffFileList } from './DiffFileList';
import { FilePatch } from './FilePatch';
import { PendingReview } from './PendingReview';

const SOURCE_LABEL: Record<string, string> = { pr: 'PR files', worktree: 'Live worktree' };

/**
 * #215 — the issue detail's "Changes" tab: the worktree's files against
 * the merge base with the base branch, and a unified diff of the selected
 * file. Lazy-loaded by IssueDetailSlide. Hunks need the operator scope
 * (the daemon refuses them to viewers, whose list is read-only).
 *
 * #238 — once the issue has a PR the daemon serves the PR's files
 * (base...head); the live worktree stays one click away while a run is
 * live. Operators comment on lines (to the agent or the PR) and review the
 * PR here.
 */
export default function IssueChangesTab({
  identifier,
  live = false,
}: {
  identifier: string;
  /** The issue's run is live (comments can go to the agent). */
  live?: boolean;
}) {
  const [source, setSource] = useState<DiffSource | undefined>(undefined);
  const { data, isLoading, error } = useIssueDiff(identifier, true, source);
  const [selected, setSelected] = useState<string | null>(null);

  if (isLoading) return <p className="text-theme-muted text-sm">Loading changes…</p>;
  if (error) return <p className="text-theme-danger-text text-sm">{error.message}</p>;
  if (!data) {
    return (
      <p className="text-theme-muted text-sm italic">
        No worktree for this issue — nothing to diff.
      </p>
    );
  }
  const isPR = data.source === 'pr';
  const review = data.source !== undefined && data.hunksAllowed;
  const shownSource = (data.source ?? 'worktree') as DiffSource;
  return (
    <div className="space-y-3" data-testid="issue-changes-tab">
      {data.sources.length > 1 && (
        <div role="group" aria-label="Diff source" className="flex gap-1 text-xs">
          {data.sources.map((s) => (
            <button
              key={s}
              type="button"
              aria-pressed={shownSource === s}
              onClick={() => {
                setSource(s as DiffSource);
                setSelected(null);
              }}
              className={`rounded px-2 py-0.5 ${shownSource === s ? 'bg-theme-bg-soft text-theme-text font-medium' : 'text-theme-muted hover:opacity-80'}`}
            >
              {SOURCE_LABEL[s] ?? s}
            </button>
          ))}
        </div>
      )}
      <p className="text-theme-muted text-xs">
        {isPR ? (
          <>
            Pull request against <code className="font-mono">{data.base}</code> (head{' '}
            <code className="font-mono">{(data.headSha ?? '').slice(0, 12)}</code>)
          </>
        ) : (
          <>
            Against <code className="font-mono">{data.base}</code> (merge base{' '}
            <code className="font-mono">{data.mergeBase.slice(0, 12)}</code>)
          </>
        )}{' '}
        · {data.files.length} {data.files.length === 1 ? 'file' : 'files'}
        {data.truncated ? ' (list truncated)' : ''}
      </p>
      {data.prError && (
        <p className="text-theme-warning-text text-xs">
          The PR&apos;s files could not be read from GitHub ({data.prError}); showing the worktree.
        </p>
      )}
      {data.files.length === 0 ? (
        <p className="text-theme-muted text-sm italic">No changes against the base branch.</p>
      ) : (
        <DiffFileList
          files={data.files}
          selected={selected}
          onSelect={data.hunksAllowed ? setSelected : undefined}
        />
      )}
      {!data.hunksAllowed && data.files.length > 0 && (
        <p className="text-theme-muted text-xs italic">
          File diffs need an operator token; this token can see the file list only.
        </p>
      )}
      {data.hunksAllowed && selected && (
        <FilePatch
          key={`${shownSource}:${selected}`}
          identifier={identifier}
          file={selected}
          source={source}
          worktreePath={data.worktreePath}
          review={review}
          canAgent={live}
          canPR={isPR && Boolean(data.prUrl)}
        />
      )}
      {review && isPR && data.prUrl && <PendingReview identifier={identifier} prUrl={data.prUrl} />}
    </div>
  );
}
