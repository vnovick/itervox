import type { ButtonHTMLAttributes, Ref } from 'react';
import { buttonClass, type ButtonSize, type ButtonVariant } from './buttonStyles';

// CORE-094 — the general button. Lives beside ConfirmButton in the existing
// lowercase `ui/button/` directory (a case-only `ui/Button/` sibling would
// collide on case-insensitive filesystems).

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  ref?: Ref<HTMLButtonElement>;
}

export function Button({
  variant = 'secondary',
  size = 'md',
  type = 'button',
  className,
  ref,
  ...rest
}: ButtonProps) {
  return (
    <button ref={ref} type={type} className={buttonClass(variant, size, className)} {...rest} />
  );
}
