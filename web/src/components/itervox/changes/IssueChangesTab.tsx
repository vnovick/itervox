import { useState } from 'react';
import { useFileDiff, useIssueDiff } from '../../../queries/changes';
import { DiffFileList } from './DiffFileList';
import { DiffViewer } from './DiffViewer';

function fmtKB(bytes: number): string {
  return `${String(Math.round(bytes / 1024))} KB`;
}

function FilePatch({ identifier, file }: { identifier: string; file: string }) {
  const { data, isLoading, error } = useFileDiff(identifier, file);
  if (isLoading) return <p className="text-theme-muted text-xs">Loading diff…</p>;
  if (error) return <p className="text-theme-danger-text text-xs">{error.message}</p>;
  if (!data) return null;
  if (data.file.binary) {
    return <p className="text-theme-muted text-xs italic">Binary file — no text diff.</p>;
  }
  return (
    <div className="space-y-1">
      {data.truncated && (
        <p className="text-theme-warning-text text-xs">
          Diff truncated at {fmtKB(data.maxBytes)} of {fmtKB(data.bytes)}.
        </p>
      )}
      <DiffViewer patch={data.patch} />
    </div>
  );
}

/**
 * #215 — the issue detail's "Changes" tab: the worktree's files against
 * the merge base with the base branch, and a unified diff of the selected
 * file. Lazy-loaded by IssueDetailSlide. Hunks need the operator scope
 * (the daemon refuses them to viewers, whose list is read-only).
 */
export default function IssueChangesTab({ identifier }: { identifier: string }) {
  const { data, isLoading, error } = useIssueDiff(identifier, true);
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
  return (
    <div className="space-y-3" data-testid="issue-changes-tab">
      <p className="text-theme-muted text-xs">
        Against <code className="font-mono">{data.base}</code> (merge base{' '}
        <code className="font-mono">{data.mergeBase.slice(0, 12)}</code>) · {data.files.length}{' '}
        {data.files.length === 1 ? 'file' : 'files'}
        {data.truncated ? ' (list truncated)' : ''}
      </p>
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
      {data.hunksAllowed && selected && <FilePatch identifier={identifier} file={selected} />}
    </div>
  );
}
