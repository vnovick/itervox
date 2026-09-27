import type { ButtonHTMLAttributes, Ref } from 'react';
import { cn } from '../cn';
import { FOCUS_RING } from '../button/buttonStyles';

export interface ToggleChipProps extends Omit<
  ButtonHTMLAttributes<HTMLButtonElement>,
  'onChange' | 'aria-pressed'
> {
  pressed: boolean;
  onPressedChange: (next: boolean) => void;
  ref?: Ref<HTMLButtonElement>;
}

/** CORE-094 — a filter chip: a toggle button whose state is `aria-pressed`. */
export function ToggleChip({
  pressed,
  onPressedChange,
  className,
  onClick,
  ref,
  ...rest
}: ToggleChipProps) {
  return (
    <button
      ref={ref}
      type="button"
      aria-pressed={pressed}
      onClick={(event) => {
        onClick?.(event);
        if (!event.defaultPrevented) onPressedChange(!pressed);
      }}
      className={cn(
        'rounded-[var(--radius-sm)] px-2 py-0.5 font-mono text-[11px] transition-colors',
        FOCUS_RING,
        pressed
          ? 'bg-theme-accent-soft text-theme-accent-strong'
          : 'text-theme-muted hover:text-theme-text line-through',
        className,
      )}
      {...rest}
    />
  );
}
