/* eslint-disable react-refresh/only-export-components */
import type { ReactNode } from 'react';

type CardVariant = 'default' | 'elevated' | 'outline';
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
  lg: 'p-6',
};

const VARIANT_STYLE: Record<CardVariant, React.CSSProperties> = {
  default: { background: 'var(--panel)', border: '1px solid var(--line)' },
  elevated: { background: 'var(--bg-elevated)', boxShadow: 'var(--shadow-md)' },
  outline: { background: 'transparent', border: '1px solid var(--line-strong)' },
};

function CardRoot({ children, variant = 'default', padding = 'md', className }: CardProps) {
  return (
    <div
      data-variant={variant}
      data-padding={padding}
      className={['rounded-[var(--radius-md)]', PADDING_CLASS[padding], className ?? ''].join(' ')}
      style={VARIANT_STYLE[variant]}
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
