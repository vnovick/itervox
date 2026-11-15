import type { HTMLAttributes, ReactNode } from 'react';
import { cn } from '../cn';

interface ConsoleSurfaceProps extends HTMLAttributes<HTMLElement> {
  /** Rendered element: a `section` for a page pane, a `div` inside other panels. */
  as?: 'section' | 'div';
  children: ReactNode;
}

/**
 * The dark log / terminal surface (`.console-surface`). It stays dark in both
 * themes: `data-theme="dark"` scopes the dark token set to this subtree, so
 * the Terminal, chips, banners and buttons inside pick up their dark
 * colours without per-component overrides.
 */
export function ConsoleSurface({ as = 'div', className, children, ...rest }: ConsoleSurfaceProps) {
  const Tag = as;
  return (
    <Tag data-theme="dark" className={cn('console-surface', className)} {...rest}>
      {children}
    </Tag>
  );
}
