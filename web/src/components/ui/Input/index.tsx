import type { InputHTMLAttributes, Ref } from 'react';
import { cn } from '../cn';
import { FIELD_CLASS } from './fieldClass';

// The shared field well lives in fieldClass.ts (no class-merge dependency).
export { FIELD_CLASS } from './fieldClass';

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
