import type { ReactNode } from 'react';
import { Link, useMatch, useResolvedPath } from 'react-router';

interface NavLinkProps {
  to: string;
  icon: ReactNode;
  label: string;
}

export function NavLink({ to, icon, label }: NavLinkProps) {
  const resolved = useResolvedPath(to);
  const match = useMatch({ path: resolved.pathname, end: true });

  return (
    <Link
      to={to}
      aria-label={label}
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
      className="flex h-10 w-10 items-center justify-center rounded-[var(--radius-md)] transition-colors hover:bg-[var(--bg-elevated)] hover:text-[var(--text)]"
    >
      <span aria-hidden="true" className="flex h-4 w-4 items-center justify-center">
        {icon}
      </span>
    </Link>
  );
}
