import { parseUnifiedDiff, type DiffLine } from './diffParse';

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
 */
export function DiffViewer({ patch }: { patch: string }) {
  const lines = parseUnifiedDiff(patch);
  return (
    <pre
      className="border-theme-line max-h-[60vh] overflow-auto rounded-[var(--radius-sm)] border text-[11px] leading-[1.45]"
      data-testid="diff-viewer"
    >
      <code className="block min-w-max">
        {lines.map((l) => (
          <span key={`${String(l.n)}:${l.kind}`} className={`block px-2 ${LINE_TONE[l.kind]}`}>
            {l.text === '' ? ' ' : l.text}
          </span>
        ))}
      </code>
    </pre>
  );
}
