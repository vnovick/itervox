import type { DiffFile } from '../../../types/schemas';

const STATUS_LABEL: Record<string, string> = {
  added: 'A',
  modified: 'M',
  deleted: 'D',
  typechange: 'T',
  unmerged: 'U',
  untracked: '?',
  renamed: 'R', // #238: PR files
  copied: 'C',
};

const STATUS_TONE: Record<string, string> = {
  added: 'text-theme-success-text',
  untracked: 'text-theme-success-text',
  deleted: 'text-theme-danger-text',
  unmerged: 'text-theme-warning-text',
};

/**
 * #215 — the changed files of a worktree: status letter, path and line
 * counts. `onSelect` makes each row a button (the Changes tab); without it
 * the list is read-only (the Now panel).
 */
export function DiffFileList({
  files,
  selected,
  onSelect,
  limit,
}: {
  files: readonly DiffFile[];
  selected?: string | null;
  onSelect?: (path: string) => void;
  limit?: number;
}) {
  const shown = limit === undefined ? files : files.slice(0, limit);
  return (
    <ul className="space-y-0.5 font-mono text-xs" aria-label="Changed files">
      {shown.map((f) => {
        const body = (
          <>
            <span
              className={`w-3 shrink-0 font-bold ${STATUS_TONE[f.status] ?? 'text-theme-warning-text'}`}
              title={f.status}
            >
              {STATUS_LABEL[f.status] ?? '•'}
            </span>
            <span className="text-theme-text min-w-0 flex-1 truncate" title={f.path}>
              {f.path}
            </span>
            {f.binary ? (
              <span className="text-theme-muted">binary</span>
            ) : (
              <span className="shrink-0">
                <span className="text-theme-success-text">+{f.additions}</span>{' '}
                <span className="text-theme-danger-text">−{f.deletions}</span>
              </span>
            )}
          </>
        );
        return (
          <li key={`${f.status}:${f.path}`}>
            {onSelect ? (
              <button
                type="button"
                onClick={() => {
                  onSelect(f.path);
                }}
                aria-pressed={selected === f.path}
                className={`flex w-full items-center gap-2 rounded px-1.5 py-0.5 text-left hover:opacity-80 ${
                  selected === f.path ? 'bg-theme-bg-soft' : ''
                }`}
              >
                {body}
              </button>
            ) : (
              <div className="flex items-center gap-2 px-1.5 py-0.5">{body}</div>
            )}
          </li>
        );
      })}
      {shown.length < files.length && (
        <li className="text-theme-muted px-1.5">+{files.length - shown.length} more</li>
      )}
    </ul>
  );
}
