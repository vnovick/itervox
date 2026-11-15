export type Status = 'live' | 'success' | 'warning' | 'error' | 'idle';
type Size = 'sm' | 'md' | 'lg';

interface LiveIndicatorProps {
  status: Status;
  size?: Size;
  label?: string;
}

const DOT_SIZE: Record<Size, string> = {
  sm: 'h-2 w-2',
  md: 'h-2.5 w-2.5',
  lg: 'h-3.5 w-3.5',
};

const STATUS_COLOR: Record<Status, string> = {
  live: 'var(--dot-ok)',
  success: 'var(--dot-ok)',
  warning: 'var(--dot-warn)',
  error: 'var(--danger)',
  idle: 'var(--dot-idle)',
};

const PULSE_STATUS = new Set<Status>(['live']);

export function LiveIndicator({ status, size = 'md', label }: LiveIndicatorProps) {
  const sizeClass = DOT_SIZE[size];
  const color = STATUS_COLOR[status];
  const showPulse = PULSE_STATUS.has(status);

  return (
    <span className="inline-flex items-center gap-1.5" data-status={status} data-size={size}>
      <span className={`relative flex ${sizeClass}`}>
        {showPulse && (
          <span
            className={`absolute inline-flex h-full w-full animate-ping rounded-full opacity-75`}
            style={{ background: color }}
          />
        )}
        <span
          className={`relative inline-flex rounded-full ${sizeClass}`}
          style={{ background: color }}
        />
      </span>
      {label !== undefined && <span data-label="">{label}</span>}
    </span>
  );
}
