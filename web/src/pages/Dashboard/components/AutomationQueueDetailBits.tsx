import type { ReactNode } from 'react';

export function DetailSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section>
      <h3 className="eyebrow text-theme-text-secondary mb-3">{title}</h3>
      <dl className="kv-grid">{children}</dl>
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
    <>
      <dt>{label}</dt>
      <dd>{isAbsent(value) ? <span className="text-theme-muted">-</span> : value}</dd>
    </>
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
      ? 'pill-accent'
      : tone === 'warning'
        ? 'pill-warn'
        : tone === 'danger'
          ? 'pill-danger'
          : tone === 'success'
            ? 'pill-ok'
            : '';
  return (
    <span title={title} className={`tag min-h-6 ${cls}`}>
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
