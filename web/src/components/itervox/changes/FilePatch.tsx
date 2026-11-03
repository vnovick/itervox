import { useEffect, useMemo, useState } from 'react';
import { useFileDiff, type DiffSource } from '../../../queries/changes';
import { usePendingComments } from '../../../store/reviewDraftStore';
import type { DiffLayout } from '../../../store/diffPrefsStore';
import { DiffViewer, type RowNote } from './DiffViewer';
import { EditorLink } from './EditorLink';
import { LineCommentBox } from './LineCommentBox';
import { parseUnifiedDiff, type DiffLine } from './diffParse';
import { nextSelection, selectionTarget, type LineSelection } from './lineSelection';

function fmtKB(bytes: number): string {
  return `${String(Math.round(bytes / 1024))} KB`;
}

/** The row a comment ends on: its side's line number in the patch. */
function rowOf(lines: readonly DiffLine[], line: number, side: 'LEFT' | 'RIGHT'): number | null {
  const hit = lines.find((l) =>
    side === 'LEFT'
      ? l.kind === 'del' && l.oldLine === line
      : (l.kind === 'add' || l.kind === 'ctx') && l.newLine === line,
  );
  return hit ? hit.n : null;
}

/**
 * #215 / #238 — one changed file: its diff (unified or side by side), an
 * open-in-editor link, and, when `review` is on, line or range selection
 * with a comment box that adds to the issue's draft review. Draft comments
 * on this file show under their lines; `focusCommentId` scrolls one into
 * view (next/previous comment).
 */
export function FilePatch({
  identifier,
  file,
  source,
  worktreePath,
  review,
  layout = 'unified',
  focusCommentId,
}: {
  identifier: string;
  file: string;
  source?: DiffSource;
  worktreePath?: string;
  /** The daemon serves change review and this token may comment. */
  review: boolean;
  layout?: DiffLayout;
  focusCommentId?: string | null;
}) {
  const { data, isLoading, error } = useFileDiff(identifier, file, source);
  const lines = useMemo(() => parseUnifiedDiff(data?.patch ?? ''), [data?.patch]);
  const [sel, setSel] = useState<LineSelection | null>(null);
  const picked = sel ? selectionTarget(lines, sel, file) : null;
  const comments = usePendingComments(identifier);
  const notes = useMemo(() => {
    const m = new Map<number, RowNote[]>();
    for (const c of comments) {
      if (c.path !== file) continue;
      const n = rowOf(lines, c.line, c.side);
      if (n === null) continue;
      m.set(n, [...(m.get(n) ?? []), { id: c.id, body: c.body }]);
    }
    return m;
  }, [comments, file, lines]);

  useEffect(() => {
    if (!focusCommentId || !data) return;
    document.getElementById(`draft-comment-${focusCommentId}`)?.scrollIntoView({ block: 'center' });
  }, [focusCommentId, data, layout]);

  if (isLoading) return <p className="text-theme-muted text-xs">Loading diff…</p>;
  if (error) return <p className="text-theme-danger-text text-xs">{error.message}</p>;
  if (!data) return null;
  if (data.file.binary) {
    return <p className="text-theme-muted text-xs italic">Binary file — no text diff.</p>;
  }
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
      {review && !picked && (
        <p className="text-theme-muted text-xs">
          Click a line number to comment; shift-click or drag to select a range.
        </p>
      )}
      <DiffViewer
        patch={data.patch}
        lines={lines}
        selection={sel}
        layout={layout}
        notes={notes}
        onLineClick={
          review
            ? (n, extend) => {
                setSel((prev) => nextSelection(lines, prev, n, extend));
              }
            : undefined
        }
      />
      {review && picked && (
        <LineCommentBox
          key={`${String(picked.target.startLine ?? '')}-${String(picked.target.line)}-${picked.target.side}`}
          identifier={identifier}
          target={picked.target}
          quote={picked.quote}
          onDone={() => {
            setSel(null);
          }}
        />
      )}
    </div>
  );
}
