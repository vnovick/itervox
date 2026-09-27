import type { ReactNode } from 'react';
import { Link, useMatch, useResolvedPath } from 'react-router';

interface NavLinkProps {
  to: string;
  icon: ReactNode;
  label: string;
  /**
   * CORE-078 — optional count badge (the Dashboard link's attention count).
   * Visual only (aria-hidden); the count is folded into the link's
   * accessible name instead of a live region, so snapshot pushes never
   * interrupt a screen reader.
   */
  badge?: number;
  badgeLabel?: string;
}

export function NavLink({
  to,
  icon,
  label,
  badge = 0,
  badgeLabel = 'need attention',
}: NavLinkProps) {
  const resolved = useResolvedPath(to);
  const match = useMatch({ path: resolved.pathname, end: true });

  return (
    <Link
      to={to}
      aria-label={badge > 0 ? `${label}, ${String(badge)} ${badgeLabel}` : label}
      aria-current={match ? 'page' : undefined}
      title={label}
      data-active={match ? 'true' : undefined}
      style={
        match
          ? {
              background: 'var(--accent-soft)',
              color: 'var(--accent-strong)',
              border: '1px solid var(--accent)',
            }
          : { color: 'var(--text-secondary)', border: '1px solid transparent' }
      }
      className="relative flex h-10 w-10 items-center justify-center rounded-[var(--radius-md)] transition-colors hover:bg-[var(--bg-elevated)] hover:text-[var(--text)]"
    >
      <span aria-hidden="true" className="flex h-4 w-4 items-center justify-center">
        {icon}
      </span>
      {badge > 0 && (
        <span
          data-testid="nav-badge"
          aria-hidden="true"
          className="bg-theme-warning absolute -top-1 -right-1 min-w-4 rounded-full px-1 text-center text-[10px] leading-4 font-bold text-black"
        >
          {badge > 99 ? '99+' : badge}
        </span>
      )}
    </Link>
  );
}
