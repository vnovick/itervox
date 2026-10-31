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

/** A labelled sidebar row (`.nav-item` in index.css): icon, label, count. */
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
      data-active={match ? 'true' : undefined}
      className="nav-item"
    >
      <span
        aria-hidden="true"
        className="flex h-[18px] w-[18px] shrink-0 items-center justify-center"
      >
        {icon}
      </span>
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {badge > 0 && (
        <span data-testid="nav-badge" aria-hidden="true" className="nav-count">
          {badge > 99 ? '99+' : badge}
        </span>
      )}
    </Link>
  );
}
