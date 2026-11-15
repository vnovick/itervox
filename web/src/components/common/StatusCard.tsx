import type { ReactNode } from 'react';

interface StatusCardProps {
  /** Small uppercase label above the title ("Daemon unreachable", "404"). */
  eyebrow?: ReactNode;
  title: ReactNode;
  /** Heading level of the title; screens that own the page use 1. */
  level?: 1 | 2;
  /** Shows the brand tile above the title (sign-in). */
  brand?: boolean;
  children?: ReactNode;
  /** Width and layout extras; replaces the default max-w-[650px]. */
  className?: string;
  /** Rendered element; a form for screens that submit. */
  as?: 'section' | 'div';
}

/**
 * The glass card used by full-screen system states: sign in, daemon
 * unreachable, pairing, page crashed and not found. Eyebrow, title, body,
 * then the actions the caller passes as children.
 */
export function StatusCard({
  eyebrow,
  title,
  level = 1,
  brand = false,
  children,
  className,
  as: Tag = 'section',
}: StatusCardProps) {
  const Heading = level === 1 ? 'h1' : 'h2';
  return (
    <Tag
      className={`glass-panel text-theme-text w-full rounded-[var(--radius-shell)] p-7 text-left ${
        className ?? 'max-w-[650px]'
      }`}
    >
      {brand && <span aria-hidden="true" className="brand-tile mb-4 h-11 w-11 rounded-[12px]" />}
      {eyebrow && <p className="eyebrow text-theme-text-secondary mb-2.5">{eyebrow}</p>}
      <Heading className="text-theme-text text-[22px] leading-tight font-bold tracking-tight">
        {title}
      </Heading>
      {children && (
        <div className="text-theme-text-secondary mt-3 space-y-3 text-sm leading-relaxed">
          {children}
        </div>
      )}
    </Tag>
  );
}

/** Centres a StatusCard (or a form shaped like one) on an otherwise empty screen. */
export function StatusScreen({ children }: { children: ReactNode }) {
  return <div className="flex min-h-screen items-center justify-center p-4 sm:p-6">{children}</div>;
}
