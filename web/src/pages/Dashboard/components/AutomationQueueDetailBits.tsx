import type { ReactNode } from 'react';

export function DetailSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="border-theme-line bg-theme-bg-elevated rounded-[var(--radius-md)] border p-4">
      <h3 className="text-theme-text mb-3 text-xs font-semibold tracking-[0.08em] uppercase">
        {title}
      </h3>
      {children}
    </section>
  );
}

/**
 * isAbsent — v0.2.0 audit P1-6 + P1-5 combined. Distinguishes a missing
 * value from a meaningful zero/false/empty-string. The old `value || -`
 * rule rendered `0` and `false` as the dash placeholder, hiding legitimate
 * "no batch limit" and "feature disabled" signals. Year-0001 strings from
 * never-set timestamps are also coerced to absent (defensive — the Zod
 * refine should already have stripped them, but this guard catches values
 * that arrive through other paths).
 */
const YEAR_ZERO_PREFIX = '0001-';
function isAbsent(value: ReactNode): boolean {
  if (value === undefined || value === null) return true;
  if (typeof value === 'string') {
    if (value === '') return true;
    if (value.startsWith(YEAR_ZERO_PREFIX)) return true;
  }
  return false;
}

export function DetailRow({ label, value }: { label: string; value?: ReactNode }) {
  return (
    <div className="grid gap-1 py-1.5 sm:grid-cols-[150px_1fr] sm:gap-3">
      <span className="text-theme-muted text-xs">{label}</span>
      <span className="text-theme-text-secondary min-w-0 text-xs break-words">
        {isAbsent(value) ? <span className="text-theme-muted">-</span> : value}
      </span>
    </div>
  );
}

export function DetailChip({
  children,
  tone = 'neutral',
  title,
}: {
  children: ReactNode;
  tone?: 'neutral' | 'accent' | 'warning' | 'danger' | 'success';
  title?: string;
}) {
  const cls =
    tone === 'accent'
      ? 'bg-theme-accent-soft text-theme-accent-strong'
      : tone === 'warning'
        ? 'bg-theme-warning-soft text-theme-warning'
        : tone === 'danger'
          ? 'bg-theme-danger-soft text-theme-danger'
          : tone === 'success'
            ? 'bg-theme-success-soft text-theme-success'
            : 'bg-theme-bg-soft text-theme-text-secondary';
  return (
    <span
      title={title}
      className={`inline-flex min-h-6 items-center rounded px-2 py-0.5 text-[11px] font-medium ${cls}`}
    >
      {children}
    </span>
  );
}

function permissionTone(action: string): 'neutral' | 'warning' | 'danger' {
  if (action === 'create_issue' || action === 'move_state') return 'danger';
  if (action === 'comment_pr' || action === 'provide_input') return 'warning';
  return 'neutral';
}

function permissionTitle(action: string): string {
  switch (action) {
    case 'comment_pr':
      return 'Can write pull request comments.';
    case 'provide_input':
      return 'Can answer blocked agent input prompts.';
    case 'create_issue':
      return 'Can create tracker issues.';
    case 'move_state':
      return 'Can move tracker issue state.';
    default:
      return 'Can comment on tracker issues.';
  }
}

export function PermissionChip({ action }: { action: string }) {
  return (
    <DetailChip tone={permissionTone(action)} title={permissionTitle(action)}>
      {action}
    </DetailChip>
  );
}
