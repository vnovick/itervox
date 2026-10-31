/**
 * Button looks (visual refresh) as plain class strings, with no
 * tailwind-merge: modules that load with the app shell (the issue slide,
 * confirm dialogs) compose these directly so the merge library stays out of
 * the initial bundle. `buttonClass` in ./buttonStyles layers merging on top.
 *
 * - `primary`     accent fill, white text
 * - `secondary`   the glass chip: translucent white, hairline rim (alias `glass`)
 * - `ghost`       text only, subtle fill on hover
 * - `danger`      soft red tint, red text (a destructive *trigger*)
 * - `destructive` solid red, white text (the confirming step)
 */
export type ButtonVariant = 'primary' | 'secondary' | 'glass' | 'ghost' | 'danger' | 'destructive';
export type ButtonSize = 'xs' | 'sm' | 'md' | 'lg';

const GLASS = 'glass-chip text-theme-text hover:bg-theme-panel-strong';

export const BUTTON_VARIANT: Record<ButtonVariant, string> = {
  primary: 'bg-theme-accent text-white shadow-theme-sm hover:brightness-110',
  secondary: GLASS,
  glass: GLASS,
  ghost: 'text-theme-text-secondary hover:bg-theme-bg-soft hover:text-theme-text',
  danger: 'bg-theme-danger-soft text-theme-danger-text hover:opacity-90',
  destructive: 'bg-theme-danger text-white shadow-theme-sm hover:brightness-110',
};

export const BUTTON_SIZE: Record<ButtonSize, string> = {
  xs: 'h-7 px-2.5 text-xs',
  sm: 'h-8 px-3 text-[13px]',
  md: 'h-9 px-3.5 text-sm',
  lg: 'h-10 px-4 text-[15px]',
};

/** Shared focus ring for every interactive primitive (keyboard focus only). */
export const FOCUS_RING =
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-theme-accent focus-visible:ring-offset-1 focus-visible:ring-offset-theme-bg';

export const BUTTON_BASE =
  'inline-flex items-center justify-center gap-1.5 font-medium whitespace-nowrap transition-[background-color,filter,opacity] disabled:cursor-not-allowed disabled:opacity-50';

/**
 * A button class without tailwind-merge. Pass only classes that do not
 * clash with the recipe (layout, margins, widths); use `buttonClass` when a
 * caller needs to override a recipe utility.
 */
export function plainButtonClass(
  variant: ButtonVariant,
  size: ButtonSize,
  { pill = false, extra = '' }: { pill?: boolean; extra?: string } = {},
): string {
  const radius = pill ? 'rounded-full' : 'rounded-[var(--radius-sm)]';
  return `${BUTTON_BASE} ${FOCUS_RING} ${radius} ${BUTTON_VARIANT[variant]} ${BUTTON_SIZE[size]}${
    extra ? ` ${extra}` : ''
  }`;
}
