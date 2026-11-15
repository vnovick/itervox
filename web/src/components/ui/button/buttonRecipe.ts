/**
 * Button looks (visual refresh), as plain class strings with no
 * tailwind-merge: screens in the initial bundle (sign in, daemon unreachable,
 * error boundaries) and modules that load with the app shell (the issue
 * slide, confirm dialogs) compose these directly so the merge library stays
 * out of the initial bundle. `buttonClass` in ./buttonStyles layers merging
 * on top.
 *
 * - `primary`     accent fill, white text
 * - `secondary`   the glass chip: translucent white, hairline rim (alias `glass`)
 * - `glass-danger` the glass chip with red text: THE destructive trigger look
 * - `ghost`       text only, subtle fill on hover
 * - `danger`      alias of `glass-danger` (one destructive-trigger style)
 * - `destructive` solid red, white text (only the confirming step)
 *
 * The chip variants also carry `.btn-chip`, which gives them a visible
 * hairline rim on the light theme, where a white chip on a white card
 * would otherwise vanish.
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

// Filled buttons (primary / destructive) do not fade when disabled — a
// washed-out accent reads as broken. They drop to the chip surface with
// muted text and a hairline rim instead, which reads as "not available yet".
const FILLED_DISABLED =
  'disabled:bg-theme-chip disabled:text-theme-muted disabled:border disabled:border-theme-hairline disabled:shadow-none disabled:filter-none';
// Everything else fades.
const FADE_DISABLED = 'disabled:opacity-50';

const GLASS = `glass-chip btn-chip text-theme-text hover:bg-theme-panel-strong ${FADE_DISABLED}`;
const GLASS_DANGER = `glass-chip btn-chip text-theme-danger-text hover:bg-theme-danger-soft ${FADE_DISABLED}`;

export const BUTTON_VARIANT: Record<ButtonVariant, string> = {
  primary: `bg-theme-accent text-white shadow-theme-sm hover:brightness-110 ${FILLED_DISABLED}`,
  secondary: GLASS,
  glass: GLASS,
  'glass-danger': GLASS_DANGER,
  ghost: `text-theme-text-secondary hover:bg-theme-bg-soft hover:text-theme-text ${FADE_DISABLED}`,
  danger: GLASS_DANGER,
  destructive: `bg-theme-danger text-white shadow-theme-sm hover:brightness-110 ${FILLED_DISABLED}`,
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
  'inline-flex items-center justify-center gap-1.5 font-medium whitespace-nowrap transition-[background-color,filter,opacity] disabled:cursor-not-allowed';

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
