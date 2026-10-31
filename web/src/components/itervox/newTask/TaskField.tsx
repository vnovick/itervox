import type { ReactNode } from 'react';

/**
 * A labelled form row for the New task form (#251). The hint and error sit
 * outside the label, so the control's accessible name is the label alone.
 */
export function TaskField({
  label,
  error,
  hint,
  children,
}: {
  label: string;
  error?: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div>
      <label className="block">
        <span className="text-theme-text-secondary mb-1 block text-xs font-medium">{label}</span>
        {children}
      </label>
      {hint && !error && <span className="text-theme-muted mt-1 block text-[11px]">{hint}</span>}
      {error && (
        <span role="alert" className="text-theme-danger-text mt-1 block text-[11px]">
          {error}
        </span>
      )}
    </div>
  );
}
