import type { ButtonProps } from '../button';
import { Button } from '../button';
import { cn } from '../cn';

export interface IconButtonProps extends Omit<ButtonProps, 'aria-label'> {
  /** Accessible name — required, since the visible content is only an icon. */
  label: string;
}

/** CORE-094 — square icon-only button with a mandatory accessible name. */
export function IconButton({
  label,
  variant = 'ghost',
  size = 'sm',
  className,
  ...rest
}: IconButtonProps) {
  return (
    <Button
      aria-label={label}
      title={rest.title ?? label}
      variant={variant}
      size={size}
      className={cn('aspect-square px-0', className)}
      {...rest}
    />
  );
}
