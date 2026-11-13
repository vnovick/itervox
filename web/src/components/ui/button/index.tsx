import type { ButtonHTMLAttributes, Ref } from 'react';
import { buttonClass, type ButtonSize, type ButtonVariant } from './buttonStyles';

// CORE-094 — the general button. Lives beside ConfirmButton in the existing
// lowercase `ui/button/` directory (a case-only `ui/Button/` sibling would
// collide on case-insensitive filesystems).

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** Fully rounded (toolbar / chip-row buttons). Default: 12px corners. */
  pill?: boolean;
  ref?: Ref<HTMLButtonElement>;
}

export function Button({
  variant = 'secondary',
  size = 'md',
  pill = false,
  type = 'button',
  className,
  ref,
  ...rest
}: ButtonProps) {
  return (
    <button
      ref={ref}
      type={type}
      className={buttonClass(variant, size, className, pill)}
      {...rest}
    />
  );
}
