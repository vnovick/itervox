import type { InputHTMLAttributes, Ref } from 'react';
import { cn } from '../cn';

/**
 * Shared field styling for Input, Select and Textarea: a card-white well
 * (translucent in both themes) with a hairline rim and 12px corners. Pass
 * `className="rounded-full"` for a pill field (search boxes, switchers).
 */
export const FIELD_CLASS =
  'w-full rounded-[var(--radius-sm)] border border-theme-line-strong bg-theme-panel px-3 text-sm text-theme-text placeholder:text-theme-muted focus:border-theme-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-theme-accent/40 disabled:opacity-50 aria-[invalid=true]:border-theme-danger';

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  /** Marks the field invalid (aria-invalid) and draws the danger border. */
  invalid?: boolean;
  ref?: Ref<HTMLInputElement>;
}

/** CORE-094 — text-like input. */
export function Input({ invalid, className, ref, ...rest }: InputProps) {
  return (
    <input
      ref={ref}
      aria-invalid={invalid || undefined}
      className={cn(FIELD_CLASS, 'h-9', className)}
      {...rest}
    />
  );
}
