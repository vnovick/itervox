import type { Ref, TextareaHTMLAttributes } from 'react';
import { cn } from '../cn';
import { FIELD_CLASS } from '../Input';

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean;
  ref?: Ref<HTMLTextAreaElement>;
}

/** CORE-094 — multi-line field (InputRequiredPanel / IssueCommentComposer are next to migrate). */
export function Textarea({ invalid, className, ref, ...rest }: TextareaProps) {
  return (
    <textarea
      ref={ref}
      aria-invalid={invalid || undefined}
      className={cn(FIELD_CLASS, 'py-2', className)}
      {...rest}
    />
  );
}
