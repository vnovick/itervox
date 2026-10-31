/**
 * Shared field styling for Input, Select and Textarea: a card-white well
 * (translucent in both themes) with a hairline rim and 12px corners. Pass
 * `className="rounded-full"` for a pill field (search boxes, switchers).
 * Kept free of class-merge helpers so initial-bundle screens and shell
 * components (sign in, the issue slide) can use it directly.
 */
export const FIELD_CLASS =
  'w-full rounded-[var(--radius-sm)] border border-theme-line-strong bg-theme-panel px-3 text-sm text-theme-text placeholder:text-theme-muted focus:border-theme-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-theme-accent/40 disabled:opacity-50 aria-[invalid=true]:border-theme-danger';
