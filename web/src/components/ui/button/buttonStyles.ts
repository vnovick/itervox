import { cn } from '../cn';

/**
 * Button looks (visual refresh):
 * - `primary`     accent fill, white text
 * - `secondary`   the glass chip: translucent white, hairline rim (alias `glass`)
 * - `glass-danger` the glass chip with red text (a quiet destructive trigger)
 * - `ghost`       text only, subtle fill on hover
 * - `danger`      soft red tint, red text (a destructive *trigger*)
 * - `destructive` solid red, white text (the confirming step)
 */
export type ButtonVariant =
  | 'primary'
  | 'secondary'
  | 'glass'
  | 'glass-danger'
  | 'ghost'
  | 'danger'
  | 'destructive';
export type ButtonSize = 'xs' | 'sm' | 'md' | 'lg';

const GLASS = 'glass-chip text-theme-text hover:bg-theme-panel-strong';

const VARIANT: Record<ButtonVariant, string> = {
  primary: 'bg-theme-accent text-white shadow-theme-sm hover:brightness-110',
  secondary: GLASS,
  glass: GLASS,
  'glass-danger': 'glass-chip text-theme-danger-text hover:bg-theme-danger-soft',
  ghost: 'text-theme-text-secondary hover:bg-theme-bg-soft hover:text-theme-text',
  danger: 'bg-theme-danger-soft text-theme-danger-text hover:opacity-90',
  destructive: 'bg-theme-danger text-white shadow-theme-sm hover:brightness-110',
};

const SIZE: Record<ButtonSize, string> = {
  xs: 'h-7 px-2.5 text-xs',
  sm: 'h-8 px-3 text-[13px]',
  md: 'h-9 px-3.5 text-sm',
  lg: 'h-10 px-4 text-[15px]',
};

/** Shared focus ring for every interactive primitive (keyboard focus only). */
export const FOCUS_RING =
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-theme-accent focus-visible:ring-offset-1 focus-visible:ring-offset-theme-bg';

export function buttonClass(
  variant: ButtonVariant,
  size: ButtonSize,
  className?: string,
  pill = false,
): string {
  return cn(
    'inline-flex items-center justify-center gap-1.5 rounded-[var(--radius-sm)] font-medium whitespace-nowrap transition-[background-color,filter,opacity] disabled:cursor-not-allowed disabled:opacity-50',
    FOCUS_RING,
    VARIANT[variant],
    SIZE[size],
    pill && 'rounded-full',
    className,
  );
}
