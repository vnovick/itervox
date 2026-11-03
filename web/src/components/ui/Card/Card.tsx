/* eslint-disable react-refresh/only-export-components */
import type { ReactNode } from 'react';

/**
 * Surface recipes (index.css):
 * - `panel`   top-level glass section (`.glass-panel`, radius 24, blurred)
 * - `card`    an item inside a panel (`.glass-card`, radius 16)
 * - `subtle`  an inset well (`.subtle-fill`, radius 16)
 * - `outline` transparent with a hairline rim
 * `default` and `elevated` are the earlier names for `card` and `panel`.
 */
type CardVariant = 'panel' | 'card' | 'subtle' | 'outline' | 'default' | 'elevated';
type CardPadding = 'none' | 'sm' | 'md' | 'lg';

interface CardProps {
  children: ReactNode;
  variant?: CardVariant;
  padding?: CardPadding;
  className?: string;
}

const PADDING_CLASS: Record<CardPadding, string> = {
  none: '',
  sm: 'p-3',
  md: 'p-4',
  lg: 'p-5',
};

const VARIANT_CLASS: Record<CardVariant, string> = {
  panel: 'glass-panel',
  elevated: 'glass-panel',
  card: 'glass-card',
  default: 'glass-card',
  subtle: 'subtle-fill rounded-[var(--radius-md)]',
  outline: 'rounded-[var(--radius-md)] border border-theme-line-strong',
};

function CardRoot({ children, variant = 'default', padding = 'md', className }: CardProps) {
  return (
    <div
      data-variant={variant}
      data-padding={padding}
      className={[VARIANT_CLASS[variant], PADDING_CLASS[padding], className ?? ''].join(' ')}
    >
      {children}
    </div>
  );
}

const Header = ({ children, className }: { children: ReactNode; className?: string }) => (
  <div className={['border-theme-line mb-3 border-b pb-3', className ?? ''].join(' ')}>
    {children}
  </div>
);

const Body = ({ children, className }: { children: ReactNode; className?: string }) => (
  <div className={className}>{children}</div>
);

const Footer = ({ children, className }: { children: ReactNode; className?: string }) => (
  <div className={['border-theme-line mt-3 border-t pt-3', className ?? ''].join(' ')}>
    {children}
  </div>
);

export const Card = Object.assign(CardRoot, { Header, Body, Footer });
