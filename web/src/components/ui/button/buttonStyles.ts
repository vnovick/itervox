import { cn } from '../cn';

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger';
export type ButtonSize = 'xs' | 'sm' | 'md';

const VARIANT: Record<ButtonVariant, string> = {
  primary: 'bg-theme-accent text-white hover:opacity-90',
  secondary:
    'border border-theme-line bg-theme-bg-elevated text-theme-text hover:bg-theme-panel-strong',
  ghost: 'text-theme-text-secondary hover:bg-theme-panel-strong hover:text-theme-text',
  danger: 'bg-theme-danger-soft text-theme-danger-text hover:opacity-90',
};

const SIZE: Record<ButtonSize, string> = {
  xs: 'h-6 px-2 text-[11px]',
  sm: 'h-8 px-2 text-xs',
  md: 'h-9 px-3 text-sm',
};

/** Shared focus ring for every interactive primitive (keyboard focus only). */
export const FOCUS_RING =
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-theme-accent focus-visible:ring-offset-1 focus-visible:ring-offset-theme-bg';

export function buttonClass(variant: ButtonVariant, size: ButtonSize, className?: string): string {
  return cn(
    'inline-flex items-center justify-center gap-1.5 rounded-[var(--radius-sm)] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50',
    FOCUS_RING,
    VARIANT[variant],
    SIZE[size],
    className,
  );
}
