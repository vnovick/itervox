import { useMemo, useState } from 'react';
import { useFileDiff, type DiffSource } from '../../../queries/changes';
import { DiffViewer } from './DiffViewer';
import { EditorLink } from './EditorLink';
import { LineCommentBox } from './LineCommentBox';
import { parseUnifiedDiff } from './diffParse';
import { nextSelection, selectionTarget, type LineSelection } from './lineSelection';

function fmtKB(bytes: number): string {
  return `${String(Math.round(bytes / 1024))} KB`;
}

/**
 * #215 / #238 — one changed file: its unified diff, an open-in-editor link,
 * and, when `review` is on, line selection with a comment box under it.
 */
export function FilePatch({
  identifier,
  file,
  source,
  worktreePath,
  review,
  canAgent,
  canPR,
}: {
  identifier: string;
  file: string;
  source?: DiffSource;
  worktreePath?: string;
  /** The daemon serves change review (#238). */
  review: boolean;
  canAgent: boolean;
  canPR: boolean;
}) {
  const { data, isLoading, error } = useFileDiff(identifier, file, source);
  const lines = useMemo(() => parseUnifiedDiff(data?.patch ?? ''), [data?.patch]);
  const [sel, setSel] = useState<LineSelection | null>(null);
  const picked = sel ? selectionTarget(lines, sel, file) : null;

  if (isLoading) return <p className="text-theme-muted text-xs">Loading diff…</p>;
  if (error) return <p className="text-theme-danger-text text-xs">{error.message}</p>;
  if (!data) return null;
  if (data.file.binary) {
    return <p className="text-theme-muted text-xs italic">Binary file — no text diff.</p>;
  }
  const commenting = review && (canAgent || canPR);
  return (
    <div className="space-y-1">
      <div className="flex min-w-0 items-center justify-between gap-2">
        <code className="text-theme-text min-w-0 truncate font-mono text-xs">{file}</code>
        <EditorLink root={worktreePath} path={file} line={picked?.target.line} />
      </div>
      {data.truncated && (
        <p className="text-theme-warning-text text-xs">
          Diff truncated at {fmtKB(data.maxBytes)} of {fmtKB(data.bytes)}.
        </p>
      )}
      {commenting && !picked && (
        <p className="text-theme-muted text-xs">
          Click a line number to comment; shift-click to select a range.
        </p>
      )}
      <DiffViewer
        patch={data.patch}
        lines={lines}
        selection={sel}
        onLineClick={
          commenting
            ? (n, extend) => {
                setSel((prev) => nextSelection(lines, prev, n, extend));
              }
            : undefined
        }
      />
      {commenting && picked && (
        <LineCommentBox
          key={`${String(picked.target.startLine ?? '')}-${String(picked.target.line)}-${picked.target.side}`}
          identifier={identifier}
          target={picked.target}
          quote={picked.quote}
          canAgent={canAgent}
          canPR={canPR}
          onDone={() => {
            setSel(null);
          }}
        />
      )}
    </div>
  );
}
