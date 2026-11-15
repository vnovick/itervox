interface ModalFooterProps {
  children: React.ReactNode;
  /** Alignment of footer buttons. Default: 'end'. */
  align?: 'start' | 'center' | 'end';
  /** Draws a hairline above the footer (long forms). Default: false. */
  divider?: boolean;
  /**
   * Pins the footer to the bottom of the scrolling sheet, so the actions stay
   * reachable while a long body scrolls inside the dialog. Defaults to
   * `divider` (the long-form footer). Assumes the sheet's 24px side padding.
   */
  sticky?: boolean;
}

/**
 * Standard modal footer with consistent spacing and alignment.
 * Wraps action buttons (Cancel + primary action) in a flex row.
 */
export function ModalFooter({
  children,
  align = 'end',
  divider = false,
  sticky = divider,
}: ModalFooterProps) {
  const justifyClass =
    align === 'start' ? 'justify-start' : align === 'center' ? 'justify-center' : 'justify-end';
  const dividerClass = divider ? 'border-theme-line-strong border-t pt-3' : '';
  return (
    <div
      data-divider={divider || undefined}
      data-sticky={sticky || undefined}
      className={['mt-5 flex flex-wrap gap-2', justifyClass, dividerClass, sticky && 'sheet-footer']
        .filter(Boolean)
        .join(' ')}
    >
      {children}
    </div>
  );
}
