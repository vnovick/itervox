import { cn } from '../cn';
import {
  BUTTON_BASE,
  BUTTON_SIZE,
  BUTTON_VARIANT,
  FOCUS_RING,
  type ButtonSize,
  type ButtonVariant,
} from './buttonRecipe';

// The recipes live in buttonRecipe.ts (no class-merge dependency); this
// module adds tailwind-merge so a caller's className overrides cleanly.
export { FOCUS_RING, type ButtonSize, type ButtonVariant };

export function buttonClass(
  variant: ButtonVariant,
  size: ButtonSize,
  className?: string,
  pill = false,
): string {
  return cn(
    BUTTON_BASE,
    FOCUS_RING,
    BUTTON_VARIANT[variant],
    BUTTON_SIZE[size],
    pill && 'rounded-full',
    className,
  );
}
