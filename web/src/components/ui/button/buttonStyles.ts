import { cn } from '../cn';
import {
  BUTTON_BASE,
  BUTTON_SIZE,
  BUTTON_VARIANT,
  FOCUS_RING,
  type ButtonSize,
  type ButtonVariant,
} from './buttonRecipe';

// The recipe (variants, sizes, focus ring) lives in ./buttonRecipe so shell
// modules can use it without tailwind-merge; this adds the merge.
export { FOCUS_RING };
export type { ButtonSize, ButtonVariant };

export function buttonClass(
  variant: ButtonVariant,
  size: ButtonSize,
  className?: string,
  pill = false,
): string {
  return cn(
    BUTTON_BASE,
    'rounded-[var(--radius-sm)]',
    FOCUS_RING,
    BUTTON_VARIANT[variant],
    BUTTON_SIZE[size],
    pill && 'rounded-full',
    className,
  );
}
