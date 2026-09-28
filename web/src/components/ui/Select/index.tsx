import type { Ref, SelectHTMLAttributes } from 'react';
import { cn } from '../cn';
import { FIELD_CLASS } from '../Input';

export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  invalid?: boolean;
  ref?: Ref<HTMLSelectElement>;
}

/** CORE-094 — native select (keeps the platform's keyboard and screen-reader behaviour). */
export function Select({ invalid, className, ref, ...rest }: SelectProps) {
  return (
    <select
      ref={ref}
      aria-invalid={invalid || undefined}
      className={cn(FIELD_CLASS, 'h-9 pr-8', className)}
      {...rest}
    />
  );
}
