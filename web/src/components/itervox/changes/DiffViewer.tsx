import { parseUnifiedDiff, type DiffLine } from './diffParse';
import { inSelection, isCommentable, type LineSelection } from './lineSelection';

const LINE_TONE: Record<DiffLine['kind'], string> = {
  meta: 'text-theme-muted',
  hunk: 'text-theme-accent-text bg-theme-bg-soft',
  add: 'text-theme-success-text bg-theme-success-soft',
  del: 'text-theme-danger-text bg-theme-danger-soft',
  ctx: 'text-theme-text',
};

/**
 * #215 — a unified diff, one row per line. The patch is rendered as text
 * nodes only (React escapes them); it never goes through innerHTML, so a
 * diff that contains markup stays text.
 *
 * #238 — with `onLineClick`, each content line gets a line-number button
 * that selects it (shift-click extends the selection) for a comment.
 */
export function DiffViewer({
  patch,
  lines,
  selection = null,
  onLineClick,
}: {
  patch: string;
  /** The parsed patch, when the caller already has it. */
  lines?: readonly DiffLine[];
  selection?: LineSelection | null;
  onLineClick?: (n: number, extend: boolean) => void;
}) {
  const rows = lines ?? parseUnifiedDiff(patch);
  return (
    <pre
      className="border-theme-line max-h-[60vh] overflow-auto rounded-[var(--radius-sm)] border text-[11px] leading-[1.45]"
      data-testid="diff-viewer"
    >
      <code className="block min-w-max">
        {rows.map((l) => {
          const selected = inSelection(selection, l.n);
          const num = l.newLine ?? l.oldLine;
          return (
            <span
              key={`${String(l.n)}:${l.kind}`}
              className={`flex ${selected ? 'ring-theme-accent ring-1 ring-inset' : ''}`}
              data-selected={selected || undefined}
            >
              {onLineClick && (
                <span className="text-theme-muted w-10 shrink-0 pr-1 text-right select-none">
                  {isCommentable(l) && num !== undefined ? (
                    <button
                      type="button"
                      aria-label={`Select ${l.kind === 'del' ? 'base ' : ''}line ${String(num)}`}
                      aria-pressed={selected}
                      className="hover:text-theme-accent-text w-full text-right"
                      onClick={(e) => {
                        onLineClick(l.n, e.shiftKey);
                      }}
                    >
                      {num}
                    </button>
                  ) : null}
                </span>
              )}
              <span className={`block flex-1 px-2 ${LINE_TONE[l.kind]}`}>
                {l.text === '' ? ' ' : l.text}
              </span>
            </span>
          );
        })}
      </code>
    </pre>
  );
}
