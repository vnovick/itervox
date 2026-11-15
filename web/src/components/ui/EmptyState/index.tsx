import type { ReactNode } from 'react';
import { cn } from '../cn';

export interface EmptyStateProps {
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
  'data-testid'?: string;
}

/** CORE-094 — centred placeholder for a pane with nothing to show. */
export function EmptyState({ title, description, action, className, ...rest }: EmptyStateProps) {
  return (
    <div
      data-testid={rest['data-testid']}
      className={cn(
        'text-theme-muted flex flex-1 flex-col items-center justify-center gap-1.5 p-8 text-center text-[13px]',
        className,
      )}
    >
      <p className="text-theme-text-secondary text-sm font-medium">{title}</p>
      {description && <p>{description}</p>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  );
}
